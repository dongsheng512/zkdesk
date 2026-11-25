package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zk_mac/internal/models"
)

func TestValidateSafeSegmentRejectsPathTraversal(t *testing.T) {
	bad := []string{
		"",
		"   ",
		".",
		"..",
		"../etc/passwd",
		`..\windows`,
		"101/202",
		`101\202`,
		"a..b",
		"101\x00evil",
		strings.Repeat("a", 65),
	}
	for _, v := range bad {
		if err := ValidateDeviceID(v); err == nil {
			t.Errorf("ValidateDeviceID(%q): expected error, got nil", v)
		}
		if err := ValidatePIN(v); err == nil {
			t.Errorf("ValidatePIN(%q): expected error, got nil", v)
		}
	}

	good := []string{"230", "dev-01", "A32_door", "178", "master", "custom"}
	for _, v := range good {
		if err := ValidateDeviceID(v); err != nil {
			t.Errorf("ValidateDeviceID(%q): unexpected error: %v", v, err)
		}
		if err := ValidatePIN(v); err != nil {
			t.Errorf("ValidatePIN(%q): unexpected error: %v", v, err)
		}
	}
}

func TestStorePermissionsAndTraversalProtection(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}

	// Verify directory permissions are 0700.
	for _, sub := range []string{dir, st.FacesDir(), st.BackupsDir(), filepath.Join(dir, "cache"), filepath.Join(dir, "logs")} {
		fi, err := os.Stat(sub)
		if err != nil {
			t.Fatalf("stat %s failed: %v", sub, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o700 {
			t.Errorf("dir %s perm = %o, want 0700", sub, perm)
		}
	}

	// Verify devices.json permission is 0600.
	fi, err := os.Stat(st.devicesPath())
	if err != nil {
		t.Fatalf("stat devices.json failed: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("devices.json perm = %o, want 0600", perm)
	}

	// SaveFaceJPEG rejects traversal attempts.
	if _, err := st.SaveFaceJPEG("../evil", "101", []byte("jpg")); err == nil {
		t.Fatalf("expected SaveFaceJPEG to reject traversal deviceID")
	}
	if _, err := st.SaveFaceJPEG("230", "../../etc", []byte("jpg")); err == nil {
		t.Fatalf("expected SaveFaceJPEG to reject traversal PIN")
	}

	// Valid face save writes with 0600.
	facePath, err := st.SaveFaceJPEG("230", "101", []byte("fakejpg"))
	if err != nil {
		t.Fatalf("SaveFaceJPEG valid failed: %v", err)
	}
	fi, err = os.Stat(facePath)
	if err != nil {
		t.Fatalf("stat face failed: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("face file perm = %o, want 0600", perm)
	}

	// FindLocalFace rejects traversal PIN.
	if p, _, _ := st.FindLocalFace("../101", "230"); p != "" {
		t.Errorf("FindLocalFace should reject traversal PIN, got %q", p)
	}

	// WriteBackup sanitizes traversal label and stays strictly inside BackupsDir.
	bkPath, err := st.WriteBackup("../../escape/attempt", map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("WriteBackup failed: %v", err)
	}
	if filepath.Dir(bkPath) != st.BackupsDir() {
		t.Fatalf("backup escaped BackupsDir: %q (want dir %q)", bkPath, st.BackupsDir())
	}
	fi, err = os.Stat(bkPath)
	if err != nil {
		t.Fatalf("stat backup failed: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup perm = %o, want 0600", perm)
	}
}

func TestDeleteDeviceCacheRemovesStatusAndSnapshot(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}
	_ = st.SaveStatus(models.DeviceStatus{DeviceID: "230", Online: true, Connected: true})
	_ = st.SaveStatus(models.DeviceStatus{DeviceID: "231", Online: true, Connected: true})
	_ = st.SaveSnapshot(models.DeviceUserSnapshot{DeviceID: "230", Users: []models.DeviceUser{{PIN: "1"}}})
	_ = st.SaveSnapshot(models.DeviceUserSnapshot{DeviceID: "231", Users: []models.DeviceUser{{PIN: "2"}}})

	if err := st.DeleteDeviceCache("230"); err != nil {
		t.Fatalf("DeleteDeviceCache failed: %v", err)
	}
	statuses := st.LoadStatuses()
	if _, ok := statuses["230"]; ok {
		t.Errorf("expected status 230 to be deleted")
	}
	if _, ok := statuses["231"]; !ok {
		t.Errorf("expected status 231 to remain")
	}
	snaps := st.LoadSnapshots()
	if _, ok := snaps["230"]; ok {
		t.Errorf("expected snapshot 230 to be deleted")
	}
	if _, ok := snaps["231"]; !ok {
		t.Errorf("expected snapshot 231 to remain")
	}
}
