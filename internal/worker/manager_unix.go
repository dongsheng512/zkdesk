//go:build !windows

package worker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func initPlatformManager(m *Manager) {
	m.winePrefix = filepath.Join(m.dataDir, "wine64")
	m.wineBin = locateWineBinary(m.runtimeDir)
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
		return errors.New("未找到 Wine（请使用内置 Wine 独立版，或确认已安装并放行 Wine Stable.app）")
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

func (m *Manager) buildWorkerCmdLocked() (*exec.Cmd, error) {
	if err := m.ensurePrefixLocked(); err != nil {
		return nil, err
	}
	workerExe := filepath.Join(m.runtimeDir, "zk_worker.exe")
	cmd := exec.Command(m.wineBin, workerExe)
	cmd.Env = m.wineEnv()
	cmd.Stderr = m.openWorkerLogLocked()
	return cmd, nil
}
