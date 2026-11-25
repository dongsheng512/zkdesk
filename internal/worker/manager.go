package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type rpcRequest struct {
	ID     string         `json:"id"`
	Action string         `json:"action"`
	Params map[string]any `json:"params,omitempty"`
}

type rpcResponse struct {
	ID      string          `json:"id"`
	Event   string          `json:"event,omitempty"`
	Current int             `json:"current,omitempty"`
	Total   int             `json:"total,omitempty"`
	Message string          `json:"message,omitempty"`
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// ProgressFunc receives live per-item progress updates streamed by the worker.
type ProgressFunc func(current, total int, message string)

// Manager supervises the persistent 64-bit Wine + Go zkemkeeper worker process.
// All calls are strictly serialized to respect ZKTeco device single-session constraints.
type Manager struct {
	mu         sync.Mutex
	dataDir    string
	runtimeDir string
	winePrefix string
	wineBin    string

	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	logFile  *os.File
	scanner  *bufio.Scanner
	reqSeq   atomic.Uint64
	onReset  func()
	startCmd func() (*exec.Cmd, error) // optional test hook; nil uses Wine + zk_worker.exe
}

func NewManager(dataDir string) *Manager {
	runtimeDir := locateRuntimeDir()
	return &Manager{
		dataDir:    dataDir,
		runtimeDir: runtimeDir,
		winePrefix: filepath.Join(dataDir, "wine64"),
		wineBin:    locateWineBinary(runtimeDir),
	}
}

// SetOnReset registers a callback invoked (outside internal locks) whenever an active
// worker process is killed or unexpectedly disconnects and gets reset.
func (m *Manager) SetOnReset(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onReset = fn
}

func locateWineBinary(runtimeDir string) string {
	if env := strings.TrimSpace(os.Getenv("ZK_MAC_WINE_BIN")); env != "" {
		if fi, err := os.Stat(env); err == nil && !fi.IsDir() {
			return env
		}
	}

	var candidates []string
	// 1. Prefer bundled standalone Wine inside app runtime/wine
	if runtimeDir != "" {
		candidates = append(candidates, filepath.Join(runtimeDir, "wine", "bin", "wine"))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "..", "Resources", "runtime", "wine", "bin", "wine"),
			filepath.Join(exeDir, "runtime", "wine", "bin", "wine"),
		)
	}
	// 2. Fall back to system-installed Wine Stable / Homebrew Wine
	candidates = append(candidates,
		"/Applications/Wine Stable.app/Contents/Resources/wine/bin/wine",
		"/opt/homebrew/bin/wine",
		"/usr/local/bin/wine",
	)
	for _, p := range candidates {
		if abs, err := filepath.Abs(p); err == nil {
			if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
				return abs
			}
		}
	}
	if p, err := exec.LookPath("wine"); err == nil {
		return p
	}
	return ""
}

func locateRuntimeDir() string {
	if env := strings.TrimSpace(os.Getenv("ZK_MAC_RUNTIME_DIR")); env != "" {
		if isValidRuntime(env) {
			return env
		}
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "..", "Resources", "runtime"),
			filepath.Join(exeDir, "runtime"),
			filepath.Join(exeDir, "..", "runtime"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "runtime"),
			filepath.Join(wd, "..", "runtime"),
		)
	}

	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil && isValidRuntime(abs) {
			return abs
		}
	}
	return ""
}

func isValidRuntime(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "zk_worker.exe")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "sdk_x64", "zkemkeeper.dll")); err != nil {
		return false
	}
	return true
}

func (m *Manager) wineEnv() []string {
	env := os.Environ()
	wineDir := filepath.Dir(m.wineBin)
	hasPath := false
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + wineDir + ":/opt/homebrew/bin:/usr/local/bin:" + strings.TrimPrefix(kv, "PATH=")
			hasPath = true
		}
	}
	if !hasPath {
		env = append(env, "PATH="+wineDir+":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin")
	}
	env = append(env,
		"WINEPREFIX="+m.winePrefix,
		"WINEARCH=win64",
		"WINEDLLOVERRIDES=mscoree,mshtml=",
		"WINELOADER="+m.wineBin,
		"WINESERVER="+filepath.Join(wineDir, "wineserver"),
		"WINEDEBUG=-all",
		"DISPLAY=",
		"MVK_CONFIG_LOG_LEVEL=0",
	)
	return env
}

func (m *Manager) WinePath() string { return m.wineBin }

func (m *Manager) WineVersion() string {
	if m.wineBin == "" {
		return ""
	}
	out, err := exec.Command(m.wineBin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (m *Manager) ensurePrefixLocked() error {
	if m.wineBin == "" {
		return errors.New("未找到 Wine（请确认已安装 Wine Stable.app 并完成放行）")
	}
	if m.runtimeDir == "" {
		return errors.New("未找到内置 runtime 目录（缺少 zk_worker.exe / sdk_x64）")
	}

	marker := filepath.Join(m.winePrefix, ".sdk_6.3.1.54_ready")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}

	if err := os.MkdirAll(m.winePrefix, 0o700); err != nil {
		return fmt.Errorf("创建 Wine 目录失败: %w", err)
	}

	wineDir := filepath.Dir(m.wineBin)
	wineboot := filepath.Join(wineDir, "wineboot")
	wineserver := filepath.Join(wineDir, "wineserver")

	initCmd := exec.Command(wineboot, "--init")
	initCmd.Env = m.wineEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Wine 初始化失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	waitCmd := exec.Command(wineserver, "-w")
	waitCmd.Env = m.wineEnv()
	_ = waitCmd.Run()

	sys32 := filepath.Join(m.winePrefix, "drive_c", "windows", "system32")
	if err := os.MkdirAll(sys32, 0o755); err != nil {
		return fmt.Errorf("创建 system32 目录失败: %w", err)
	}

	sdkDir := filepath.Join(m.runtimeDir, "sdk_x64")
	entries, err := os.ReadDir(sdkDir)
	if err != nil {
		return fmt.Errorf("读取 SDK 目录失败: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".dll") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sdkDir, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(sys32, e.Name()), raw, 0o644); err != nil {
			return err
		}
	}

	regCmd := exec.Command(m.wineBin, "regsvr32", "/s", `C:\windows\system32\zkemkeeper.dll`)
	regCmd.Env = m.wineEnv()
	if out, err := regCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("注册 64 位 zkemkeeper.dll 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	return os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0o644)
}

func (m *Manager) openWorkerLogLocked() io.Writer {
	if m.dataDir == "" {
		return os.Stderr
	}
	logDir := filepath.Join(m.dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return os.Stderr
	}
	logPath := filepath.Join(logDir, "worker.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 2*1024*1024 {
		// Rotate if log exceeds 2 MiB
		_ = os.Rename(logPath, logPath+".prev")
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(logPath, flags, 0o600)
	if err != nil {
		return os.Stderr
	}
	_, _ = fmt.Fprintf(f, "\n[%s] === worker start ===\n", time.Now().Format("2006-01-02 15:04:05"))
	m.logFile = f
	return io.MultiWriter(os.Stderr, f)
}

func (m *Manager) ensureRunningLocked() error {
	if m.cmd != nil && m.cmd.Process != nil && m.cmd.ProcessState == nil {
		return nil
	}

	var cmd *exec.Cmd
	if m.startCmd != nil {
		var err error
		cmd, err = m.startCmd()
		if err != nil {
			return err
		}
	} else {
		if err := m.ensurePrefixLocked(); err != nil {
			return err
		}
		workerExe := filepath.Join(m.runtimeDir, "zk_worker.exe")
		cmd = exec.Command(m.wineBin, workerExe)
		cmd.Env = m.wineEnv()
		cmd.Stderr = m.openWorkerLogLocked()
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	// Use an explicit os.Pipe for stdout instead of cmd.StdoutPipe so closing pr in
	// resetLocked immediately unblocks scanner.Scan() without racing with cmd.Wait().
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	cmd.Stdout = pw

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = pr.Close()
		_ = pw.Close()
		return fmt.Errorf("启动 Wine SDK Worker 失败: %w", err)
	}
	// Close parent's write end so EOF is observed when child exits.
	_ = pw.Close()

	scanner := bufio.NewScanner(pr)
	// Allow large JSON payloads (e.g. batch user bundles with base64 face photos)
	scanner.Buffer(make([]byte, 256*1024), 64*1024*1024)

	// Wait for __ready__ handshake line (skipping any non-JSON Wine/MoltenVK noise)
	readyCh := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "{") {
				continue
			}
			var resp rpcResponse
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				continue
			}
			if resp.ID == "__ready__" && resp.OK {
				readyCh <- nil
				return
			}
			if resp.ID == "__init__" && !resp.OK {
				readyCh <- errors.New(resp.Error)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			readyCh <- err
			return
		}
		readyCh <- errors.New("Wine SDK Worker 启动后意外退出")
	}()

	select {
	case err := <-readyCh:
		if err != nil {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = pr.Close()
			_ = cmd.Wait()
			return err
		}
	case <-time.After(30 * time.Second):
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = pr.Close()
		<-readyCh
		_ = cmd.Wait()
		return errors.New("Wine SDK Worker 启动超时")
	}

	m.cmd = cmd
	m.stdin = stdin
	m.stdout = pr
	m.scanner = scanner
	return nil
}

func (m *Manager) resetLocked() bool {
	wasRunning := m.cmd != nil || m.stdin != nil || m.stdout != nil || m.scanner != nil
	if m.stdin != nil {
		_ = m.stdin.Close()
		m.stdin = nil
	}
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	if m.stdout != nil {
		_ = m.stdout.Close()
		m.stdout = nil
	}
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Wait()
	}
	if m.logFile != nil {
		_ = m.logFile.Close()
		m.logFile = nil
	}
	m.cmd = nil
	m.scanner = nil
	return wasRunning
}

// Call sends one JSON-RPC request to the persistent Wine worker and decodes its data field.
func (m *Manager) Call(ctx context.Context, action string, params map[string]any, dst any) error {
	return m.CallWithProgress(ctx, action, params, dst, nil)
}

// CallWithProgress sends one JSON-RPC request and forwards any streamed progress lines.
func (m *Manager) CallWithProgress(ctx context.Context, action string, params map[string]any, dst any, onProgress ProgressFunc) error {
	var resetNotify func()
	defer func() {
		if resetNotify != nil {
			resetNotify()
		}
	}()

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureRunningLocked(); err != nil {
		return err
	}

	reqID := fmt.Sprintf("%d", m.reqSeq.Add(1))
	payload, err := json.Marshal(rpcRequest{
		ID:     reqID,
		Action: action,
		Params: params,
	})
	if err != nil {
		return err
	}
	if _, err := m.stdin.Write(append(payload, '\n')); err != nil {
		if m.resetLocked() {
			resetNotify = m.onReset
		}
		return fmt.Errorf("发送指令到 Worker 失败: %w", err)
	}

	type readResult struct {
		resp rpcResponse
		err  error
	}
	// Capture scanner locally so resetLocked() setting m.scanner = nil cannot cause a
	// data race or nil-pointer dereference in the reader goroutine.
	scanner := m.scanner
	ch := make(chan readResult, 1)
	go func() {
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "{") {
				continue
			}
			var resp rpcResponse
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				continue
			}
			if resp.ID != reqID {
				continue
			}
			if resp.Event == "progress" {
				if onProgress != nil {
					onProgress(resp.Current, resp.Total, resp.Message)
				}
				continue
			}
			ch <- readResult{resp: resp}
			return
		}
		if err := scanner.Err(); err != nil {
			ch <- readResult{err: err}
			return
		}
		ch <- readResult{err: errors.New("Worker 连接已断开")}
	}()

	select {
	case <-ctx.Done():
		if m.resetLocked() {
			resetNotify = m.onReset
		}
		// Wait for reader goroutine to exit cleanly (m.stdout was closed by resetLocked).
		<-ch
		return fmt.Errorf("操作超时或已取消 (%s)", action)
	case r := <-ch:
		if r.err != nil {
			if m.resetLocked() {
				resetNotify = m.onReset
			}
			return r.err
		}
		if !r.resp.OK {
			return errors.New(r.resp.Error)
		}
		if dst != nil && len(r.resp.Data) > 0 {
			if err := json.Unmarshal(r.resp.Data, dst); err != nil {
				return fmt.Errorf("解析 Worker 响应失败: %w", err)
			}
		}
		return nil
	}
}

// Shutdown cleanly terminates the persistent worker process on app exit.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stdin != nil && m.cmd != nil {
		_, _ = m.stdin.Write([]byte(`{"id":"bye","action":"shutdown"}` + "\n"))
		time.Sleep(150 * time.Millisecond)
	}
	m.resetLocked()
}
