//go:build windows

package worker

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func initPlatformManager(_ *Manager) {}

func (m *Manager) WinePath() string {
	if m.runtimeDir == "" {
		return ""
	}
	return filepath.Join(m.runtimeDir, "zk_worker.exe")
}

func (m *Manager) WineVersion() string {
	return "Windows 原生 64 位"
}

func (m *Manager) windowsWorkerEnv() []string {
	env := os.Environ()
	sdkDir := filepath.Join(m.runtimeDir, "sdk_x64")
	hasPath := false
	for i, kv := range env {
		if len(kv) >= 5 && strings.EqualFold(kv[:5], "PATH=") {
			env[i] = kv[:5] + sdkDir + ";" + kv[5:]
			hasPath = true
			break
		}
	}
	if !hasPath {
		env = append(env, "PATH="+sdkDir)
	}
	return env
}

// tryAutoRegisterSDK attempts a silent 64-bit regsvr32 registration if zkemkeeper.ZKEM.1
// is not yet registered. Succeeds automatically when launched elevated; otherwise the
// worker reports a clear Chinese hint pointing to the included one-click .bat script.
func (m *Manager) tryAutoRegisterSDK() {
	check := exec.Command("reg", "query", `HKCR\zkemkeeper.ZKEM.1`)
	check.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := check.Run(); err == nil {
		return
	}
	dllPath := filepath.Join(m.runtimeDir, "sdk_x64", "zkemkeeper.dll")
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	regsvr := filepath.Join(sysRoot, "System32", "regsvr32.exe")
	reg := exec.Command(regsvr, "/s", dllPath)
	reg.Env = m.windowsWorkerEnv()
	reg.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = reg.Run()
}

func (m *Manager) buildWorkerCmdLocked() (*exec.Cmd, error) {
	if m.runtimeDir == "" {
		return nil, errors.New("未找到内置 runtime 目录（缺少 zk_worker.exe / sdk_x64）")
	}
	m.tryAutoRegisterSDK()

	workerExe := filepath.Join(m.runtimeDir, "zk_worker.exe")
	cmd := exec.Command(workerExe)
	cmd.Env = m.windowsWorkerEnv()
	cmd.Dir = filepath.Join(m.runtimeDir, "sdk_x64")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Stderr = m.openWorkerLogLocked()
	return cmd, nil
}
