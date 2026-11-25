package service

import (
	"testing"
	"time"

	"zk_mac/internal/exporter"
	"zk_mac/internal/models"
	"zk_mac/internal/store"
)

func TestP0SmartSyncPreviewAndArchiveAndAudit(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewZKService(st, nil)

	// 1. Setup 3 devices + snapshots with mixed diffs
	devs := []models.DeviceConfig{
		{ID: "230", Name: "D230", Enabled: true},
		{ID: "231", Name: "D231", Enabled: true},
		{ID: "232", Name: "D232", Enabled: true},
	}
	_ = st.SaveDevices(devs)
	_ = st.SaveSnapshot(models.DeviceUserSnapshot{
		DeviceID: "230",
		Users: []models.DeviceUser{
			{PIN: "10", Name: "张三", HasFace: true, FPCount: 2}, // richest for PIN 10
		},
	})
	_ = st.SaveSnapshot(models.DeviceUserSnapshot{
		DeviceID: "231",
		Users: []models.DeviceUser{
			{PIN: "10", Name: "张三", HasFace: false, FPCount: 1}, // missing face & 1 fp
			{PIN: "20", Name: "李四", HasFace: true, FPCount: 0},  // only on 231
		},
	})
	_ = st.SaveSnapshot(models.DeviceUserSnapshot{
		DeviceID: "232",
		Users:    []models.DeviceUser{}, // missing both 10 & 20
	})

	// Smart mode preview (pure read-only)
	plan, err := svc.PreviewSmartSync("smart", "", nil, true, true, true)
	if err != nil {
		t.Fatalf("PreviewSmartSync smart failed: %v", err)
	}
	if plan.TotalAffected != 2 {
		t.Fatalf("expected 2 affected items, got %d", plan.TotalAffected)
	}
	if plan.Items[0].PIN != "10" || plan.Items[0].SourceDeviceID != "230" || len(plan.Items[0].TargetDeviceIDs) != 2 {
		t.Fatalf("unexpected PIN 10 smart plan: %+v", plan.Items[0])
	}
	if plan.Items[1].PIN != "20" || plan.Items[1].SourceDeviceID != "231" || len(plan.Items[1].TargetDeviceIDs) != 2 {
		t.Fatalf("unexpected PIN 20 smart plan: %+v", plan.Items[1])
	}

	// Master mode preview with 230 as master -> PIN 20 not on 230 so skipped
	masterPlan, err := svc.PreviewSmartSync("master", "230", nil, true, true, true)
	if err != nil {
		t.Fatalf("PreviewSmartSync master failed: %v", err)
	}
	if masterPlan.TotalAffected != 1 || masterPlan.Skipped != 1 {
		t.Fatalf("unexpected master plan: affected=%d skipped=%d", masterPlan.TotalAffected, masterPlan.Skipped)
	}

	// 2. Test persistent attendance archive incremental merge + dedup + daily summary
	added1, total1, err := st.MergeAttendanceArchive([]models.AttendanceRecord{
		{DeviceID: "231", DeviceName: "D231", PIN: "10", Name: "张三", Timestamp: "2026-11-21 08:30:00"},
		{DeviceID: "231", DeviceName: "D231", PIN: "10", Name: "张三", Timestamp: "2026-11-21 18:05:00"},
		{DeviceID: "231", DeviceName: "D231", PIN: "20", Name: "李四", Timestamp: "2026-11-21 09:00:00"},
	})
	if err != nil || added1 != 3 || total1 != 3 {
		t.Fatalf("first merge unexpected: added=%d total=%d err=%v", added1, total1, err)
	}
	// Second merge overlaps 2 records and adds 1 new
	added2, total2, err := st.MergeAttendanceArchive([]models.AttendanceRecord{
		{DeviceID: "231", DeviceName: "D231", PIN: "10", Name: "张三", Timestamp: "2026-11-21 08:30:00"},
		{DeviceID: "231", DeviceName: "D231", PIN: "10", Name: "张三", Timestamp: "2026-11-22 08:45:00"},
	})
	if err != nil || added2 != 1 || total2 != 4 {
		t.Fatalf("second merge unexpected: added=%d total=%d err=%v", added2, total2, err)
	}

	q := svc.QueryArchivedLogs("2026-11-21", "2026-11-21", []string{"231"})
	if q.Total != 3 || q.ArchiveTotal != 4 || len(q.DailySummary) != 2 {
		t.Fatalf("unexpected archive query: total=%d archive=%d daily=%d", q.Total, q.ArchiveTotal, len(q.DailySummary))
	}
	if q.DailySummary[0].PIN != "10" || q.DailySummary[0].Abnormal || q.DailySummary[0].WorkDuration != "9h 35m" {
		t.Fatalf("unexpected normal daily summary: %+v", q.DailySummary[0])
	}
	if q.DailySummary[1].PIN != "20" || !q.DailySummary[1].Abnormal {
		t.Fatalf("expected single-punch abnormal for PIN 20: %+v", q.DailySummary[1])
	}

	// 3. Test time drift helper
	ref := time.Date(2026, 11, 22, 12, 0, 30, 0, time.Local)
	drift, ok := computeTimeDrift("2026-11-22 12:00:04", ref)
	if !ok || drift != -26 {
		t.Fatalf("expected drift=-26, got %d (ok=%v)", drift, ok)
	}

	// 4. Test operation audit logs
	svc.recordAudit("pull_logs", "拉取考勤", "D231", "拉取 3 条", true, "", "")
	svc.recordAudit("sync_time", "同步时间", "D231", "校准完成", true, "", "")
	logs := svc.GetOperationLogs()
	if len(logs) != 2 || logs[0].Action != "sync_time" {
		t.Fatalf("unexpected audit logs: %+v", logs)
	}
	if err := svc.ClearOperationLogs(); err != nil {
		t.Fatal(err)
	}
	if len(svc.GetOperationLogs()) != 0 {
		t.Fatal("expected empty audit logs after clear")
	}

	// 5. Verify dual-sheet Excel export contains both sheets
	daily := exporter.BuildDailySummary(q.Records)
	if len(daily) != 2 {
		t.Fatalf("expected 2 daily rows, got %d", len(daily))
	}
}
