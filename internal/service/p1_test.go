package service

import (
	"os"
	"path/filepath"
	"testing"

	"zk_mac/internal/exporter"
	"zk_mac/internal/models"
	"zk_mac/internal/store"
)

func TestP1ProfilesFolderScanAndDeptEnrichment(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewZKService(st, nil)

	// 1. Setup devices & user snapshot
	_ = st.SaveDevices([]models.DeviceConfig{
		{ID: "231", Name: "D231", Enabled: true},
	})
	_ = st.SaveSnapshot(models.DeviceUserSnapshot{
		DeviceID: "231",
		Users: []models.DeviceUser{
			{PIN: "103", Name: "张三", HasFace: true, FPCount: 2},
			{PIN: "205", Name: "已离职员工", HasFace: false, FPCount: 1},
		},
	})

	// 2. Test CSV profile parsing + merge
	csvPath := filepath.Join(dir, "hr.csv")
	csvContent := "工号,部门,状态,备注\n103,研发部,在职,核心\n205,销售部,离职,待清理\n"
	if err := os.WriteFile(csvPath, []byte(csvContent), 0o644); err != nil {
		t.Fatal(err)
	}
	profiles, skipped, err := exporter.ParseProfilesFile(csvPath)
	if err != nil || len(profiles) != 2 || skipped != 0 {
		t.Fatalf("ParseProfilesFile unexpected: len=%d skipped=%d err=%v", len(profiles), skipped, err)
	}
	if _, err := st.MergeProfiles(profiles); err != nil {
		t.Fatal(err)
	}

	// Verify matrix shows departments + resigned-in-device counter
	matrix, err := svc.GetUserMatrix()
	if err != nil {
		t.Fatal(err)
	}
	if matrix.ResignedInDev != 1 || len(matrix.Departments) != 2 {
		t.Fatalf("unexpected matrix hr stats: resigned=%d depts=%v", matrix.ResignedInDev, matrix.Departments)
	}

	// 3. Verify attendance archive query enriches Department automatically
	_, _, _ = st.MergeAttendanceArchive([]models.AttendanceRecord{
		{DeviceID: "231", DeviceName: "D231", PIN: "103", Name: "张三", Timestamp: "2026-11-22 08:45:00"},
	})
	q := svc.QueryArchivedLogs("2026-11-22", "2026-11-22", nil)
	if len(q.Records) != 1 || q.Records[0].Department != "研发部" {
		t.Fatalf("expected enriched dept 研发部 on record, got %+v", q.Records)
	}
	if len(q.DailySummary) != 1 || q.DailySummary[0].Department != "研发部" {
		t.Fatalf("expected enriched dept 研发部 on daily summary, got %+v", q.DailySummary)
	}

	// 4. Test folder scan for batch face upload (pure local read-only)
	faceDir := filepath.Join(dir, "incoming_faces")
	_ = os.MkdirAll(faceDir, 0o755)
	_ = os.WriteFile(filepath.Join(faceDir, "103_张三.jpg"), []byte("fakejpg"), 0o644)
	_ = os.WriteFile(filepath.Join(faceDir, "999.png"), []byte("fakepng"), 0o644)
	_ = os.WriteFile(filepath.Join(faceDir, "readme.txt"), []byte("ignore"), 0o644)
	_ = os.WriteFile(filepath.Join(faceDir, "nonum.jpg"), []byte("ignore"), 0o644)

	cands, err := svc.ScanFaceFolder(faceDir)
	if err != nil {
		t.Fatalf("ScanFaceFolder failed: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 valid face candidates, got %d: %+v", len(cands), cands)
	}
	if cands[0].PIN != "103" || !cands[0].ExistsInDevs || cands[0].Name != "张三" {
		t.Fatalf("unexpected cand[0]: %+v", cands[0])
	}
	if cands[1].PIN != "999" || cands[1].ExistsInDevs {
		t.Fatalf("unexpected cand[1]: %+v", cands[1])
	}
}

func TestImportABTFileMergesAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewZKService(st, nil)
	_ = st.SaveDevices([]models.DeviceConfig{
		{ID: "232", Name: "考勤机 232", SensorID: "32", Enabled: true},
	})
	_, _ = st.MergeProfiles([]models.UserProfile{
		{PIN: "908", Department: "信息技术部", WorkStatus: "active"},
	})

	abtPath := filepath.Join(dir, "sample.abt")
	err = exporter.ExportABT(abtPath, []models.AttendanceRecord{
		{
			DeviceID:   "232",
			DeviceName: "考勤机 232",
			SensorID:   "32",
			PIN:        "908",
			Name:       "张三",
			Timestamp:  "2026-01-01 16:01:50",
			VerifyMode: 15,
			InOutMode:  0,
		},
		{
			DeviceID:   "sensor_21",
			DeviceName: "外部考勤机 (21)",
			SensorID:   "21",
			PIN:        "747",
			Name:       "李四",
			Timestamp:  "2026-01-01 08:46:37",
			VerifyMode: 1,
			InOutMode:  0,
		},
	})
	if err != nil {
		t.Fatalf("ExportABT failed: %v", err)
	}

	res1, err := svc.ImportABTFile(abtPath)
	if err != nil {
		t.Fatalf("ImportABTFile #1 failed: %v", err)
	}
	if res1.Total != 2 || res1.AddedNew != 2 || res1.ArchiveTotal != 2 {
		t.Fatalf("unexpected import #1 stats: total=%d added=%d archive=%d", res1.Total, res1.AddedNew, res1.ArchiveTotal)
	}
	if res1.Records[0].PIN != "908" || res1.Records[0].Department != "信息技术部" || res1.Records[0].DeviceID != "232" {
		t.Fatalf("unexpected record[0]: %+v", res1.Records[0])
	}
	if res1.Records[1].PIN != "747" || res1.Records[1].DeviceID != "sensor_21" {
		t.Fatalf("unexpected record[1]: %+v", res1.Records[1])
	}

	// Second import of same file should deduplicate (addedNew == 0)
	res2, err := svc.ImportABTFile(abtPath)
	if err != nil {
		t.Fatalf("ImportABTFile #2 failed: %v", err)
	}
	if res2.AddedNew != 0 || res2.ArchiveTotal != 2 {
		t.Fatalf("expected dedup on second import, got added=%d archive=%d", res2.AddedNew, res2.ArchiveTotal)
	}
}

func TestArchiveOverviewAndQuerySummariesAndSummaryCSV(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewZKService(st, nil)
	_ = st.SaveDevices([]models.DeviceConfig{
		{ID: "230", Name: "前门考勤机", SensorID: "30", Enabled: true},
		{ID: "231", Name: "后门考勤机", SensorID: "31", Enabled: true},
		{ID: "dev_f2", Name: "二楼考勤机", SensorID: "32", Enabled: true},
	})

	_, _, err = st.MergeAttendanceArchive([]models.AttendanceRecord{
		{DeviceID: "230", DeviceName: "前门考勤机", SensorID: "30", PIN: "101", Name: "张三", Timestamp: "2026-11-01 08:30:00", VerifyLabel: "人脸", InOutLabel: "上班签到"},
		{DeviceID: "230", DeviceName: "前门考勤机", SensorID: "30", PIN: "101", Name: "张三", Timestamp: "2026-11-01 18:00:00", VerifyLabel: "人脸", InOutLabel: "下班签退"},
		{DeviceID: "dev_f2", DeviceName: "二楼考勤机", SensorID: "32", PIN: "102", Name: "李四", Timestamp: "2026-11-15 09:05:00", VerifyLabel: "指纹", InOutLabel: "上班签到"},
		{DeviceID: "sensor_21", DeviceName: "外部考勤机 (21)", SensorID: "21", PIN: "103", Name: "王五", Timestamp: "2026-11-20 08:50:00", VerifyLabel: "刷卡", InOutLabel: "上班签到"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Archive overview should list all 3 configured devices (including 0-count 231) + 1 external sensor
	ov := svc.GetAttendanceArchiveOverview()
	if ov.TotalRecords != 4 || ov.EarliestDate != "2026-11-01" || ov.LatestDate != "2026-11-20" {
		t.Fatalf("unexpected archive overview totals/dates: %+v", ov)
	}
	if len(ov.Devices) != 4 {
		t.Fatalf("expected 4 devices in overview, got %d: %+v", len(ov.Devices), ov.Devices)
	}
	if ov.Devices[0].DeviceID != "230" || ov.Devices[0].Count != 2 || !ov.Devices[0].Configured {
		t.Fatalf("unexpected overview[0]: %+v", ov.Devices[0])
	}
	if ov.Devices[1].DeviceID != "231" || ov.Devices[1].Count != 0 || !ov.Devices[1].Configured {
		t.Fatalf("unexpected overview[1] (zero-count configured): %+v", ov.Devices[1])
	}
	if ov.Devices[3].DeviceID != "sensor_21" || ov.Devices[3].Count != 1 || ov.Devices[3].Configured {
		t.Fatalf("unexpected overview[3] (external sensor): %+v", ov.Devices[3])
	}

	// 2. QueryArchivedLogs should populate per-device Summaries cleanly in configured order
	q := svc.QueryArchivedLogs("2026-11-01", "2026-11-23", nil)
	if q.Total != 4 || len(q.Summaries) != 3 {
		t.Fatalf("expected 4 records & 3 device summaries, got total=%d summaries=%+v", q.Total, q.Summaries)
	}
	if q.Summaries[0].DeviceID != "230" || q.Summaries[0].Count != 2 {
		t.Fatalf("unexpected first summary: %+v", q.Summaries[0])
	}

	// 3. ExportDailySummaryCSV should write valid UTF-8 BOM CSV with header + summary rows
	csvPath := filepath.Join(dir, "summary.csv")
	if err := exporter.ExportDailySummaryCSV(csvPath, q.DailySummary); err != nil {
		t.Fatalf("ExportDailySummaryCSV failed: %v", err)
	}
	raw, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 3 || raw[0] != 0xEF || raw[1] != 0xBB || raw[2] != 0xBF {
		t.Fatalf("expected UTF-8 BOM in summary CSV")
	}
	if len(q.DailySummary) != 3 {
		t.Fatalf("expected 3 daily summary rows, got %d", len(q.DailySummary))
	}
}

func TestPruneAttendanceArchiveLocalOnly(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewZKService(st, nil) // worker is nil: proves strictly local-only without touching hardware!
	_ = st.SaveDevices([]models.DeviceConfig{
		{ID: "230", Name: "前门考勤机", SensorID: "30", Enabled: true},
		{ID: "231", Name: "后门考勤机", SensorID: "31", Enabled: true},
	})

	_, _, err = st.MergeAttendanceArchive([]models.AttendanceRecord{
		{DeviceID: "230", DeviceName: "前门考勤机", PIN: "101", Name: "张三", Timestamp: "2026-09-15 08:30:00"},
		{DeviceID: "230", DeviceName: "前门考勤机", PIN: "101", Name: "张三", Timestamp: "2026-10-31 23:59:59"},
		{DeviceID: "231", DeviceName: "后门考勤机", PIN: "102", Name: "李四", Timestamp: "2026-10-20 09:00:00"},
		{DeviceID: "230", DeviceName: "前门考勤机", PIN: "101", Name: "张三", Timestamp: "2026-11-01 00:00:00"}, // boundary: must be KEPT!
		{DeviceID: "231", DeviceName: "后门考勤机", PIN: "102", Name: "李四", Timestamp: "2026-11-24 18:00:00"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Preview single device before 2026-11-01 -> should match 2 records on 230, keeping 2026-11-01 00:00:00
	prevDev, err := svc.PreviewPruneAttendanceArchive(models.PruneArchiveRequest{
		BeforeDate: "2026-11-01",
		DeviceID:   "230",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prevDev.TotalBefore != 5 || prevDev.MatchCount != 2 || prevDev.RemainCount != 3 || prevDev.EarliestDate != "2026-09-15" || prevDev.LatestDate != "2026-10-31" {
		t.Fatalf("unexpected device preview: %+v", prevDev)
	}

	// 2. Prune all devices strictly before 2026-11-01 -> removes 3, keeps 2 (incl. 2026-11-01 00:00:00), writes backup & audit
	res, err := svc.PruneAttendanceArchive(models.PruneArchiveRequest{
		BeforeDate: "2026-11-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 3 || res.RemainCount != 2 || res.BackupPath == "" {
		t.Fatalf("unexpected prune response: %+v", res)
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Fatalf("expected safety backup file at %s: %v", res.BackupPath, err)
	}

	q := svc.QueryArchivedLogs("2026-01-01", "2026-12-31", nil)
	if q.Total != 2 || q.ArchiveTotal != 2 {
		t.Fatalf("expected 2 remaining records after prune, got total=%d archive=%d", q.Total, q.ArchiveTotal)
	}
	for _, r := range q.Records {
		if r.Timestamp < "2026-11-01 00:00:00" {
			t.Fatalf("found record older than cutoff after prune: %+v", r)
		}
	}

	// 3. Re-running same prune should remove 0 without error
	res2, err := svc.PruneAttendanceArchive(models.PruneArchiveRequest{BeforeDate: "2026-11-01"})
	if err != nil || res2.Removed != 0 || res2.RemainCount != 2 {
		t.Fatalf("expected 0 removed on second prune, got %+v err=%v", res2, err)
	}
}
