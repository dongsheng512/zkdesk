package service

import (
	"os"
	"strconv"
	"testing"

	"zk_mac/internal/models"
	"zk_mac/internal/store"
	"zk_mac/internal/worker"
)

// TestLiveReadOnly runs a STRICTLY READ-ONLY integration test against a single configured device.
// Set ZK_LIVE_TEST=1 and ZK_LIVE_IP=<device-ip> (optional: ZK_LIVE_PASSWORD=<num>).
func TestLiveReadOnly(t *testing.T) {
	if os.Getenv("ZK_LIVE_TEST") != "1" || os.Getenv("ZK_LIVE_IP") == "" {
		t.Skip("set ZK_LIVE_TEST=1 and ZK_LIVE_IP=<ip> to run live read-only integration test")
	}

	pwd, _ := strconv.Atoi(os.Getenv("ZK_LIVE_PASSWORD"))
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := st.SaveDevices([]models.DeviceConfig{{
		ID:            "live_dev",
		Name:          "Live Test Terminal",
		IP:            os.Getenv("ZK_LIVE_IP"),
		Port:          4370,
		Password:      pwd,
		MachineNumber: 1,
		Enabled:       true,
	}}); err != nil {
		t.Fatalf("SaveDevices: %v", err)
	}

	wm := worker.NewManager(dir)
	defer wm.Shutdown()

	svc := NewZKService(st, wm)

	health := svc.CheckRuntime()
	t.Logf("RuntimeHealth: ready=%v wine=%s err=%s", health.Ready, health.WineVersion, health.Error)
	if !health.Ready {
		t.Fatalf("runtime not ready: %s", health.Error)
	}

	stDev, err := svc.ConnectDevice("live_dev")
	if err != nil || !stDev.Connected {
		t.Fatalf("ConnectDevice failed: %+v err=%v", stDev, err)
	}
	defer func() { _, _ = svc.DisconnectDevice("live_dev") }()

	matrix, err := svc.RefreshUsers([]string{"live_dev"}, true)
	if err != nil {
		t.Fatalf("RefreshUsers: %v", err)
	}
	t.Logf("users read: %d", matrix.TotalUsers)

	logs, err := svc.PullAttendanceLogs(models.PullLogsRequest{
		DeviceIDs: []string{"live_dev"},
		StartDate: "2026-11-20",
		EndDate:   "2026-11-22",
	})
	if err != nil {
		t.Fatalf("PullAttendanceLogs: %v", err)
	}
	t.Logf("logs: total=%d addedNew=%d archiveTotal=%d dailyRows=%d",
		logs.Total, logs.AddedNew, logs.ArchiveTotal, len(logs.DailySummary))
}
