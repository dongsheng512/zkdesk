package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

// TestWorkerHelperProcess acts as a mock JSON-RPC worker subprocess when invoked
// with GO_WANT_HELPER_PROCESS=1 by Manager tests.
func TestWorkerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{"id": "__ready__", "ok": true})

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		switch req.Action {
		case "shutdown":
			return
		case "echo":
			_ = enc.Encode(map[string]any{
				"id":      req.ID,
				"event":   "progress",
				"current": 1,
				"total":   2,
				"message": "working",
			})
			_ = enc.Encode(map[string]any{
				"id":   req.ID,
				"ok":   true,
				"data": req.Params,
			})
		case "hang":
			// Block until killed by Manager on ctx timeout.
			time.Sleep(30 * time.Second)
		case "crash":
			os.Exit(2)
		default:
			_ = enc.Encode(map[string]any{
				"id":    req.ID,
				"ok":    false,
				"error": fmt.Sprintf("unknown action: %s", req.Action),
			})
		}
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := &Manager{}
	m.startCmd = func() (*exec.Cmd, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerHelperProcess$")
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd, nil
	}
	t.Cleanup(m.Shutdown)
	return m
}

func TestManagerCallAndProgress(t *testing.T) {
	m := newTestManager(t)

	var gotProg atomic.Int32
	var out struct {
		Value string `json:"value"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := m.CallWithProgress(ctx, "echo", map[string]any{"value": "ok"}, &out, func(cur, total int, msg string) {
		if cur == 1 && total == 2 && msg == "working" {
			gotProg.Add(1)
		}
	})
	if err != nil {
		t.Fatalf("CallWithProgress failed: %v", err)
	}
	if out.Value != "ok" {
		t.Fatalf("unexpected value: %q", out.Value)
	}
	if gotProg.Load() != 1 {
		t.Fatalf("expected 1 progress callback, got %d", gotProg.Load())
	}
}

func TestManagerTimeoutNoPanicAndCallsOnReset(t *testing.T) {
	m := newTestManager(t)

	var resetCount atomic.Int32
	m.SetOnReset(func() {
		resetCount.Add(1)
		// Verify onReset is invoked outside m.mu (would deadlock if m.mu were held).
		m.mu.Lock()
		m.mu.Unlock()
	})

	// Run multiple timeout cycles to stress race detector & nil-scanner regression.
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		err := m.Call(ctx, "hang", nil, nil)
		cancel()
		if err == nil {
			t.Fatalf("iteration %d: expected timeout error, got nil", i)
		}

		// Recovery call after reset must spawn a fresh worker and succeed.
		var out struct {
			Step int `json:"step"`
		}
		okCtx, okCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = m.Call(okCtx, "echo", map[string]any{"step": i}, &out)
		okCancel()
		if err != nil {
			t.Fatalf("iteration %d: recovery call failed: %v", i, err)
		}
		if out.Step != i {
			t.Fatalf("iteration %d: expected step=%d, got %d", i, i, out.Step)
		}
	}

	if got := resetCount.Load(); got != 5 {
		t.Fatalf("expected 5 OnReset notifications, got %d", got)
	}
}

func TestManagerCrashCallsOnResetAndRecovers(t *testing.T) {
	m := newTestManager(t)

	var resetCount atomic.Int32
	m.SetOnReset(func() {
		resetCount.Add(1)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Call(ctx, "crash", nil, nil); err == nil {
		t.Fatalf("expected error on worker crash, got nil")
	}
	if got := resetCount.Load(); got != 1 {
		t.Fatalf("expected 1 OnReset notification after crash, got %d", got)
	}

	var out struct {
		Alive bool `json:"alive"`
	}
	if err := m.Call(ctx, "echo", map[string]any{"alive": true}, &out); err != nil {
		t.Fatalf("expected recovery after crash, got: %v", err)
	}
	if !out.Alive {
		t.Fatalf("expected alive=true")
	}
}

func TestManagerShutdownDoesNotTriggerOnReset(t *testing.T) {
	m := newTestManager(t)

	var resetCount atomic.Int32
	m.SetOnReset(func() {
		resetCount.Add(1)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Call(ctx, "echo", map[string]any{"a": 1}, nil); err != nil {
		t.Fatalf("initial call failed: %v", err)
	}

	m.Shutdown()
	if got := resetCount.Load(); got != 0 {
		t.Fatalf("expected 0 OnReset notifications on clean Shutdown, got %d", got)
	}
}

func TestLocateWineBinaryPrefersBundledRuntime(t *testing.T) {
	t.Setenv("ZK_MAC_WINE_BIN", "")
	rtDir := t.TempDir()
	bundled := rtDir + "/wine/bin/wine"
	if err := os.MkdirAll(rtDir+"/wine/bin", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundled, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	got := locateWineBinary(rtDir)
	if got != bundled {
		t.Fatalf("locateWineBinary(%q) = %q, want bundled %q", rtDir, got, bundled)
	}
}
