package service

import (
	"os"
	"path/filepath"
	"testing"

	"zk_mac/internal/models"
	"zk_mac/internal/store"
)

func TestBuildMatrixLocalMasterDiffDetection(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewZKService(st, nil)
	devs := []models.DeviceConfig{
		{ID: "230", Name: "D230", Enabled: true},
		{ID: "231", Name: "D231", Enabled: true},
		{ID: "232", Name: "D232", Enabled: true}, // intentionally has no snapshot
	}
	_ = st.SaveDevices(devs)

	// Seed authoritative Local Master DB
	if err := st.SaveMasterUser(models.MasterUser{
		PIN:        "1",
		Name:       "张三",
		Department: "研发部",
		WorkStatus: "active",
		Privilege:  0,
		Enabled:    true,
		Fingerprints: []models.FingerprintTemplate{
			{FingerID: 0, Flag: 1, Template: "TPL_0"},
			{FingerID: 1, Flag: 1, Template: "TPL_1"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFaceJPEG("master", "1", []byte{0xFF, 0xD8, 0xFF, 0xD9}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMasterUser(models.MasterUser{
		PIN:        "2",
		Name:       "李四（已改名）",
		Department: "运营部",
		WorkStatus: "active",
		Privilege:  0,
		Enabled:    true,
		Fingerprints: []models.FingerprintTemplate{
			{FingerID: 0, Flag: 1, Template: "TPL_0"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMasterUser(models.MasterUser{
		PIN:        "4",
		Name:       "离职老员工",
		Department: "市场部",
		WorkStatus: "resigned",
		Privilege:  0,
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMasterUser(models.MasterUser{
		PIN:        "5",
		Name:       "分部员工",
		Department: "业务部",
		WorkStatus: "active",
		Privilege:  0,
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}

	snaps := map[string]models.DeviceUserSnapshot{
		"230": {
			DeviceID:  "230",
			UpdatedAt: "2026-11-22 10:00:00",
			Users: []models.DeviceUser{
				{PIN: "1", Name: "张三", Enabled: true, HasFace: true, FPCount: 2},
				{PIN: "2", Name: "李四", Enabled: true, HasFace: false, FPCount: 1}, // old name -> need_push
				{PIN: "4", Name: "离职老员工", Enabled: true},                          // resigned_present
				{PIN: "5", Name: "分部员工", Enabled: true},                           // only assigned to 230
			},
		},
		"231": {
			DeviceID:  "231",
			UpdatedAt: "2026-11-22 10:00:01",
			Users: []models.DeviceUser{
				{PIN: "1", Name: "张三", Enabled: true, HasFace: true, FPCount: 2},
				{PIN: "2", Name: "李四（已改名）", Enabled: true, HasFace: true, FPCount: 2},   // extra face & fp -> need_pull
				{PIN: "3", Name: "王五(机上新录)", Enabled: true, HasFace: false, FPCount: 1}, // not in master -> need_pull
			},
		},
	}

	m := svc.buildMatrix(devs, snaps)
	if m.TotalUsers != 5 || m.MasterCount != 4 {
		t.Fatalf("expected total=5 master=4, got total=%d master=%d", m.TotalUsers, m.MasterCount)
	}
	// PIN 1: synced on 230 & 231, no_snapshot on 232 -> HasDiff == false!
	if m.Rows[0].PIN != "1" || m.Rows[0].HasDiff {
		t.Fatalf("PIN 1 should be synced with no diff: %+v", m.Rows[0])
	}
	if m.Rows[0].Devices["232"].SyncStatus != "no_snapshot" {
		t.Fatalf("PIN 1 on 232 should be no_snapshot, got %s", m.Rows[0].Devices["232"].SyncStatus)
	}
	// PIN 2: need_push on 230 (name), need_pull on 231 (new face + fp)
	if m.Rows[1].PIN != "2" || !m.Rows[1].HasPendingPush || !m.Rows[1].HasPendingPull {
		t.Fatalf("PIN 2 should have both pending push & pull: %+v", m.Rows[1])
	}
	if m.Rows[1].Devices["230"].SyncStatus != "need_push" || m.Rows[1].Devices["231"].SyncStatus != "need_pull" {
		t.Fatalf("PIN 2 device statuses unexpected: 230=%s 231=%s", m.Rows[1].Devices["230"].SyncStatus, m.Rows[1].Devices["231"].SyncStatus)
	}
	// PIN 3: device-only -> not in master, need_pull
	if m.Rows[2].PIN != "3" || m.Rows[2].InMaster || !m.Rows[2].HasPendingPull {
		t.Fatalf("PIN 3 should be device-only need_pull: %+v", m.Rows[2])
	}
	// PIN 4: resigned -> resigned_present on 230, synced on 231
	if m.Rows[3].PIN != "4" || !m.Rows[3].HasResignedInDev || m.Rows[3].Devices["230"].SyncStatus != "resigned_present" || m.Rows[3].Devices["231"].SyncStatus != "synced" {
		t.Fatalf("PIN 4 resigned state unexpected: %+v", m.Rows[3])
	}
	// PIN 5: active in master, present on 230, absent on 231 -> not_assigned (NOT a diff!)
	if m.Rows[4].PIN != "5" || m.Rows[4].HasDiff || m.Rows[4].HasPendingPush || m.Rows[4].Devices["231"].SyncStatus != "not_assigned" {
		t.Fatalf("PIN 5 absent on 231 should be not_assigned with no diff: %+v", m.Rows[4])
	}
}

func TestMasterUsersDurableBackupAndRecovery(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewZKService(st, nil)
	_ = st.SaveDevices([]models.DeviceConfig{{ID: "231", Name: "D231", Enabled: true}})

	// Initial save with fingerprint preserved across subsequent edits
	if err := st.SaveMasterUser(models.MasterUser{
		PIN:      "908",
		Name:     "测试员工",
		Card:     "12345678",
		Password: "secret",
		Enabled:  true,
		Fingerprints: []models.FingerprintTemplate{
			{FingerID: 0, Flag: 1, Template: "FP_TEMPLATE_DATA"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Edit locally via SaveMasterUser with blank card/password & Preserve*=true
	_, err = svc.SaveMasterUser(models.SaveMasterUserRequest{
		PIN:                 "908",
		Name:                "测试员工(新)",
		Department:          "技术部",
		WorkStatus:          "active",
		Enabled:             true,
		PreserveCardIfEmpty: true,
		PreservePwdIfEmpty:  true,
	})
	if err != nil {
		t.Fatalf("SaveMasterUser failed: %v", err)
	}
	users, _ := st.LoadMasterUsers()
	u := users["908"]
	if u.Name != "测试员工(新)" || u.Card != "12345678" || u.Password != "secret" || len(u.Fingerprints) != 1 {
		t.Fatalf("SaveMasterUser lost preserved fields: %+v", u)
	}

	// Export backup, then corrupt primary master_users.json and verify automatic .bak fallback recovery!
	bakExport := filepath.Join(dir, "exported_master.json")
	cnt, err := st.ExportMasterUsersBackup(bakExport)
	if err != nil || cnt != 1 {
		t.Fatalf("ExportMasterUsersBackup failed: cnt=%d err=%v", cnt, err)
	}
	if err := os.WriteFile(st.MasterUsersPath(), []byte("{corrupted json"), 0o644); err != nil {
		t.Fatal(err)
	}
	recovered, _ := st.LoadMasterUsers()
	if len(recovered) != 1 || recovered["908"].PIN != "908" {
		t.Fatalf("expected automatic .bak recovery, got %+v", recovered)
	}

	// Restore from exported backup
	restoredCnt, preBak, err := st.ImportMasterUsersBackup(bakExport)
	if err != nil || restoredCnt != 1 || preBak == "" {
		t.Fatalf("ImportMasterUsersBackup failed: cnt=%d preBak=%q err=%v", restoredCnt, preBak, err)
	}
}

func TestRequireConnectedBlocksAllHardwareOps(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewZKService(st, nil) // worker is nil: any bypass would panic!
	_ = st.SaveDevices([]models.DeviceConfig{
		{ID: "230", Name: "考勤机 230", Enabled: true},
		{ID: "231", Name: "考勤机 231", Enabled: true},
	})
	_ = st.SaveMasterUser(models.MasterUser{PIN: "908", Name: "测试员工", Enabled: true})

	checkBlocked := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected error when device not connected, got nil", name)
		}
	}

	_, err = svc.ProbeDevice("230")
	checkBlocked("ProbeDevice", err)
	_, err = svc.ProbeAllDevices()
	checkBlocked("ProbeAllDevices", err)
	_, err = svc.SyncDeviceTime("230")
	checkBlocked("SyncDeviceTime", err)
	_, err = svc.SyncAllDeviceTimes()
	checkBlocked("SyncAllDeviceTimes", err)
	_, err = svc.RefreshUsers([]string{"230"}, true)
	checkBlocked("RefreshUsers", err)
	_, err = svc.PullDevicesToMaster(models.PullDevicesToMasterRequest{DeviceIDs: []string{"230"}, IncludeInfo: true})
	checkBlocked("PullDevicesToMaster", err)
	_, err = svc.PushMasterToDevices(models.PushMasterToDevicesRequest{PINs: []string{"908"}, DeviceIDs: []string{"230"}, SyncInfo: true})
	checkBlocked("PushMasterToDevices", err)
	_, err = svc.UpsertUser(models.UpsertUserRequest{PIN: "908", Name: "测试员工", DeviceIDs: []string{"230"}})
	checkBlocked("UpsertUser", err)
	_, err = svc.ClearUserBiometric(models.ClearBiometricRequest{PIN: "908", Target: "face", DeviceIDs: []string{"230"}})
	checkBlocked("ClearUserBiometric", err)
	_, err = svc.DeleteUsers(models.DeleteUsersRequest{PINs: []string{"908"}, DeviceIDs: []string{"230"}})
	checkBlocked("DeleteUsers", err)
	_, err = svc.DownloadAllFaces("230", nil)
	checkBlocked("DownloadAllFaces", err)
	_, err = svc.UploadFacePhoto("908", "/tmp/nonexistent.jpg", []string{"230"})
	checkBlocked("UploadFacePhoto", err)
	_, err = svc.DeleteFacePhoto("908", []string{"230"})
	checkBlocked("DeleteFacePhoto", err)
	_, err = svc.PullAttendanceLogs(models.PullLogsRequest{DeviceIDs: []string{"230"}, StartDate: "2026-11-01", EndDate: "2026-11-22"})
	checkBlocked("PullAttendanceLogs", err)

	// Pure local operations MUST still succeed when zero devices are connected!
	if _, err := svc.SaveMasterUser(models.SaveMasterUserRequest{PIN: "909", Name: "离线员工", Enabled: true}); err != nil {
		t.Fatalf("SaveMasterUser should work offline, got %v", err)
	}
	q := svc.QueryArchivedLogs("2026-11-01", "2026-11-22", nil)
	if q.Total != 0 {
		t.Fatalf("QueryArchivedLogs should work offline")
	}
}
