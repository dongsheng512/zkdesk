package service

import (
	"strings"
	"testing"

	"zk_mac/internal/models"
	"zk_mac/internal/store"
	"zk_mac/internal/worker"
)

func newTestService(t *testing.T) (*ZKService, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}
	// Manager with empty wineBin / runtimeDir guarantees hardware calls fail locally
	// without spawning Wine or touching any network device.
	wm := &worker.Manager{}
	svc := NewZKService(st, wm)
	return svc, st
}

func TestHandleWorkerResetClearsConnectedAndStatuses(t *testing.T) {
	svc, st := newTestService(t)

	if err := st.SaveStatus(models.DeviceStatus{
		DeviceID:  "230",
		Online:    true,
		Connected: true,
	}); err != nil {
		t.Fatalf("SaveStatus failed: %v", err)
	}

	svc.connMu.Lock()
	svc.connected["230"] = true
	svc.connMu.Unlock()

	svc.handleWorkerReset()

	if svc.isDeviceConnected("230") {
		t.Fatalf("expected device 230 to be marked disconnected after worker reset")
	}
	statuses := svc.GetCachedStatuses()
	if statuses["230"].Connected {
		t.Fatalf("expected cached status for 230 to have Connected=false after worker reset")
	}
}

func TestRefreshUsersDoesNotSwallowErrorWhenMasterHasUsers(t *testing.T) {
	svc, st := newTestService(t)

	// Seed local master roster so matrix.Rows is non-empty.
	if err := st.SaveMasterUser(models.MasterUser{
		PIN: "101", Name: "测试员工", Enabled: true,
	}); err != nil {
		t.Fatalf("SaveMasterUser failed: %v", err)
	}
	if err := st.SaveDevices([]models.DeviceConfig{
		{ID: "230", Name: "考勤机A", IP: "127.0.0.1", Port: 4370, Enabled: true},
	}); err != nil {
		t.Fatalf("SaveDevices failed: %v", err)
	}

	svc.connMu.Lock()
	svc.connected["230"] = true
	svc.connMu.Unlock()

	_, err := svc.RefreshUsers([]string{"230"}, true)
	if err == nil {
		t.Fatalf("expected RefreshUsers to return error when device scan fails even though master has rows")
	}
	if !strings.Contains(err.Error(), "考勤机A") {
		t.Fatalf("expected error message to include device name, got: %v", err)
	}
}

func TestPullAttendanceLogsFailsWhenAllDevicesFail(t *testing.T) {
	svc, st := newTestService(t)

	if err := st.SaveDevices([]models.DeviceConfig{
		{ID: "230", Name: "考勤机A", IP: "127.0.0.1", Port: 4370, Enabled: true},
	}); err != nil {
		t.Fatalf("SaveDevices failed: %v", err)
	}
	svc.connMu.Lock()
	svc.connected["230"] = true
	svc.connMu.Unlock()

	_, err := svc.PullAttendanceLogs(models.PullLogsRequest{
		DeviceIDs: []string{"230"},
		StartDate: "2026-11-01",
		EndDate:   "2026-11-25",
	})
	if err == nil {
		t.Fatalf("expected PullAttendanceLogs to fail when all target devices fail")
	}
	if !strings.Contains(err.Error(), "全部所选考勤机拉取失败") {
		t.Fatalf("unexpected error: %v", err)
	}

	logs := st.LoadOperationLogs()
	if len(logs) == 0 || logs[0].Success {
		t.Fatalf("expected failed audit log to be recorded, got: %+v", logs)
	}
}

func TestSaveAndDeleteDeviceDisconnectsAndCleansCache(t *testing.T) {
	svc, st := newTestService(t)

	// Reject traversal device ID.
	if _, err := svc.SaveDevice(models.DeviceConfig{ID: "../bad", IP: "127.0.0.1", Enabled: true}); err == nil {
		t.Fatalf("expected SaveDevice to reject traversal device ID")
	}

	if _, err := svc.SaveDevice(models.DeviceConfig{
		ID: "230", Name: "设备A", IP: "192.168.1.10", Port: 4370, Password: 123, Enabled: true,
	}); err != nil {
		t.Fatalf("SaveDevice failed: %v", err)
	}
	_ = st.SaveStatus(models.DeviceStatus{DeviceID: "230", Online: true, Connected: true})
	_ = st.SaveSnapshot(models.DeviceUserSnapshot{DeviceID: "230", Users: []models.DeviceUser{{PIN: "101"}}})
	svc.connMu.Lock()
	svc.connected["230"] = true
	svc.connMu.Unlock()

	// Changing IP must disconnect existing session and clear cached Connected flag.
	if _, err := svc.SaveDevice(models.DeviceConfig{
		ID: "230", Name: "设备A", IP: "192.168.1.99", Port: 4370, Password: 123, Enabled: true,
	}); err != nil {
		t.Fatalf("SaveDevice update failed: %v", err)
	}
	if svc.isDeviceConnected("230") {
		t.Fatalf("expected device 230 to disconnect when IP changed")
	}
	if svc.GetCachedStatuses()["230"].Connected {
		t.Fatalf("expected cached status Connected=false after IP change")
	}

	// DeleteDevice must clear connected state, status cache, and snapshot cache.
	svc.connMu.Lock()
	svc.connected["230"] = true
	svc.connMu.Unlock()
	if _, err := svc.DeleteDevice("230"); err != nil {
		t.Fatalf("DeleteDevice failed: %v", err)
	}
	if svc.isDeviceConnected("230") {
		t.Fatalf("expected device 230 to be removed from connected map")
	}
	if _, ok := st.LoadStatuses()["230"]; ok {
		t.Fatalf("expected device 230 status cache to be removed")
	}
	if _, ok := st.LoadSnapshots()["230"]; ok {
		t.Fatalf("expected device 230 snapshot cache to be removed")
	}
}

func TestOpenDirectoryGuardsAndURLEncoding(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.OpenDataDirectory(); err == nil {
		t.Fatalf("expected OpenDataDirectory to fail cleanly when app is nil")
	}
	if err := svc.OpenBackupsDirectory(); err == nil {
		t.Fatalf("expected OpenBackupsDirectory to fail cleanly when app is nil")
	}
	got := fileDirURL("/Users/test/Library/Application Support/zk_mac")
	want := "file:///Users/test/Library/Application%20Support/zk_mac"
	if got != want {
		t.Fatalf("fileDirURL = %q, want %q", got, want)
	}
	gotWin := fileDirURL("C:/Users/test/AppData/Roaming/zk_mac")
	wantWin := "file:///C:/Users/test/AppData/Roaming/zk_mac"
	if gotWin != wantWin {
		t.Fatalf("fileDirURL(win) = %q, want %q", gotWin, wantWin)
	}
}

func TestFaceGalleryIsLazyAndLoadsOnDemand(t *testing.T) {
	svc, st := newTestService(t)

	if err := st.SaveMasterUser(models.MasterUser{PIN: "178", Name: "示例员工", Enabled: true}); err != nil {
		t.Fatalf("SaveMasterUser failed: %v", err)
	}
	if _, err := st.SaveFaceJPEG("master", "178", []byte{0xFF, 0xD8, 0xFF, 0xD9}); err != nil {
		t.Fatalf("SaveFaceJPEG failed: %v", err)
	}

	items, err := svc.GetFaceGallery()
	if err != nil {
		t.Fatalf("GetFaceGallery failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 gallery item, got %d", len(items))
	}
	if items[0].LocalPath == "" {
		t.Fatalf("expected LocalPath to be populated")
	}
	if items[0].DataURL != "" {
		t.Fatalf("expected GetFaceGallery to be lazy (empty DataURL), got len=%d", len(items[0].DataURL))
	}

	dataURL, err := svc.GetLocalFaceDataURL("178")
	if err != nil {
		t.Fatalf("GetLocalFaceDataURL failed: %v", err)
	}
	if !strings.HasPrefix(dataURL, "data:image/jpeg;base64,") {
		t.Fatalf("unexpected dataURL: %q", dataURL)
	}
	if _, err := svc.GetLocalFaceDataURL("../178"); err == nil {
		t.Fatalf("expected GetLocalFaceDataURL to reject traversal PIN")
	}
}
