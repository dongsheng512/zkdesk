//go:build !windows

package worker

import (
	"os"
	"testing"
)

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
