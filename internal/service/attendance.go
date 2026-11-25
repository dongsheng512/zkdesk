package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"zk_mac/internal/exporter"
	"zk_mac/internal/models"
	"zk_mac/internal/store"
)

// ==================== Attendance Log Pull & Export ====================

func verifyLabel(code int) string {
	switch code {
	case 0:
		return "密码"
	case 1:
		return "指纹"
	case 2:
		return "刷卡"
	case 15:
		return "人脸"
	case 25:
		return "掌纹"
	default:
		return fmt.Sprintf("方式(%d)", code)
	}
}

func inOutLabel(code int) string {
	switch code {
	case 0:
		return "上班签到"
	case 1:
		return "下班签退"
	case 2:
		return "外出"
	case 3:
		return "外出返回"
	case 4:
		return "加班签到"
	case 5:
		return "加班签退"
	case 255:
		return "打卡"
	default:
		return fmt.Sprintf("状态(%d)", code)
	}
}

func (s *ZKService) enrichDepartments(records []models.AttendanceRecord) []models.AttendanceRecord {
	masterUsers, _ := s.store.LoadMasterUsers()
	profiles := s.store.LoadProfiles()
	for i := range records {
		records[i].Name = store.RepairZKName(records[i].Name)
		if mu, ok := masterUsers[records[i].PIN]; ok {
			if mu.Department != "" {
				records[i].Department = mu.Department
			}
			if records[i].Name == "" && mu.Name != "" {
				records[i].Name = mu.Name
			}
		}
		if records[i].Department == "" {
			if p, ok := profiles[records[i].PIN]; ok && p.Department != "" {
				records[i].Department = p.Department
			}
		}
	}
	return records
}

func (s *ZKService) GetLastLogs() models.PullLogsResponse {
	resp := s.store.LoadLastLogs()
	_, archiveTotal, _ := s.store.QueryAttendanceArchive("", "", nil)
	resp.ArchiveTotal = archiveTotal
	resp.Records = s.enrichDepartments(resp.Records)
	resp.DailySummary = exporter.BuildDailySummary(resp.Records)
	if resp.DailySummary == nil {
		resp.DailySummary = []models.DailySummaryRow{}
	}
	return resp
}

// QueryArchivedLogs queries the persistent deduplicated local attendance archive without hitting hardware.
func (s *ZKService) QueryArchivedLogs(startDate, endDate string, deviceIDs []string) models.PullLogsResponse {
	records, archiveTotal, updatedAt := s.store.QueryAttendanceArchive(startDate, endDate, deviceIDs)
	records = s.enrichDepartments(records)

	allDevs, _ := s.store.LoadDevices()
	devOrder := make(map[string]int, len(allDevs))
	devNameByID := make(map[string]string, len(allDevs))
	for i, d := range allDevs {
		devOrder[d.ID] = i
		devNameByID[d.ID] = d.Name
	}

	byDev := map[string]*models.PullLogsDeviceSummary{}
	for _, r := range records {
		sm, ok := byDev[r.DeviceID]
		if !ok {
			name := devNameByID[r.DeviceID]
			if name == "" {
				name = r.DeviceName
			}
			if name == "" {
				name = r.DeviceID
			}
			sm = &models.PullLogsDeviceSummary{
				DeviceID:   r.DeviceID,
				DeviceName: name,
				ModeUsed:   "本地累计库",
			}
			byDev[r.DeviceID] = sm
		}
		sm.Count++
	}
	summaries := make([]models.PullLogsDeviceSummary, 0, len(byDev))
	for _, sm := range byDev {
		summaries = append(summaries, *sm)
	}
	sort.Slice(summaries, func(i, j int) bool {
		oi, oki := devOrder[summaries[i].DeviceID]
		oj, okj := devOrder[summaries[j].DeviceID]
		if oki && okj {
			return oi < oj
		}
		if oki != okj {
			return oki
		}
		return summaries[i].DeviceID < summaries[j].DeviceID
	})

	return models.PullLogsResponse{
		Records:      records,
		DailySummary: exporter.BuildDailySummary(records),
		Total:        len(records),
		ArchiveTotal: archiveTotal,
		Summaries:    summaries,
		PulledAt:     updatedAt,
	}
}

// GetAttendanceArchiveOverview returns per-device record counts and date coverage in the local cumulative archive.
func (s *ZKService) GetAttendanceArchiveOverview() models.ArchiveOverviewResponse {
	raw := s.store.GetAttendanceArchiveOverview()
	allDevs, _ := s.store.LoadDevices()

	byID := make(map[string]models.ArchiveDeviceStat, len(raw.Devices))
	for _, st := range raw.Devices {
		byID[st.DeviceID] = st
	}

	seen := make(map[string]bool, len(allDevs))
	ordered := make([]models.ArchiveDeviceStat, 0, len(allDevs)+len(raw.Devices))
	for _, d := range allDevs {
		seen[d.ID] = true
		st, ok := byID[d.ID]
		if !ok {
			st = models.ArchiveDeviceStat{
				DeviceID: d.ID,
			}
		}
		st.DeviceName = d.Name
		if d.SensorID != "" {
			st.SensorID = d.SensorID
		}
		st.Configured = true
		ordered = append(ordered, st)
	}
	for _, st := range raw.Devices {
		if seen[st.DeviceID] {
			continue
		}
		if st.DeviceName == "" {
			st.DeviceName = st.DeviceID
		}
		st.Configured = false
		ordered = append(ordered, st)
	}
	raw.Devices = ordered
	return raw
}

// PreviewPruneAttendanceArchive previews how many local archived records would be deleted strictly before BeforeDate.
// Pure local read-only operation; never touches attendance hardware.
func (s *ZKService) PreviewPruneAttendanceArchive(req models.PruneArchiveRequest) (models.PruneArchivePreview, error) {
	return s.store.PreviewPruneAttendanceArchive(strings.TrimSpace(req.BeforeDate), strings.TrimSpace(req.DeviceID))
}

// PruneAttendanceArchive removes records strictly before BeforeDate from the local cumulative archive ONLY.
// Never modifies any attendance terminal; automatically saves removed records to backups/ and logs audit entry.
func (s *ZKService) PruneAttendanceArchive(req models.PruneArchiveRequest) (models.PruneArchiveResponse, error) {
	beforeDate := strings.TrimSpace(req.BeforeDate)
	deviceID := strings.TrimSpace(req.DeviceID)
	resp, err := s.store.PruneAttendanceArchive(beforeDate, deviceID)
	if err != nil {
		return resp, err
	}
	if resp.Removed > 0 {
		scope := "全部设备"
		if deviceID != "" {
			if d, ferr := s.findDevice(deviceID); ferr == nil {
				scope = d.Name
			} else {
				scope = deviceID
			}
		}
		s.recordAudit(
			"prune_archive",
			"清理本地考勤库",
			fmt.Sprintf("%s · %s 之前", scope, beforeDate),
			fmt.Sprintf("已清理 %d 条本地历史记录，剩余 %d 条（不动考勤机数据）", resp.Removed, resp.RemainCount),
			true,
			"",
			resp.BackupPath,
		)
	}
	return resp, nil
}

func (s *ZKService) PullAttendanceLogs(req models.PullLogsRequest) (models.PullLogsResponse, error) {
	if req.StartDate == "" || req.EndDate == "" {
		return models.PullLogsResponse{}, errors.New("请选择起止日期")
	}
	startTime := req.StartDate
	if len(startTime) == 10 {
		startTime += " 00:00:00"
	}
	endTime := req.EndDate
	if len(endTime) == 10 {
		endTime += " 23:59:59"
	}

	allDevs, err := s.store.LoadDevices()
	if err != nil {
		return models.PullLogsResponse{}, err
	}
	pick := map[string]bool{}
	for _, id := range req.DeviceIDs {
		pick[id] = true
	}
	targets := make([]models.DeviceConfig, 0, len(allDevs))
	for _, d := range allDevs {
		if !d.Enabled {
			continue
		}
		if len(pick) == 0 || pick[d.ID] {
			targets = append(targets, d)
		}
	}
	if len(targets) == 0 {
		return models.PullLogsResponse{}, errors.New("请至少选择一台考勤机")
	}
	if err := s.requireConnectedDevices(targets...); err != nil {
		return models.PullLogsResponse{}, err
	}

	// Build global fallback name map from cached snapshots
	fallbackNames := map[string]string{}
	for _, snap := range s.store.LoadSnapshots() {
		for _, u := range snap.Users {
			if u.Name != "" {
				fallbackNames[u.PIN] = u.Name
			}
		}
	}

	seen := map[string]bool{}
	merged := make([]models.AttendanceRecord, 0)
	summaries := make([]models.PullLogsDeviceSummary, 0, len(targets))

	for i, d := range targets {
		s.emitProgress("logs", fmt.Sprintf("正在拉取 %s (%s ~ %s)...", d.Name, req.StartDate, req.EndDate), i+1, len(targets), false)

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		params := deviceParams(d)
		params["start_time"] = startTime
		params["end_time"] = endTime
		params["log_mode"] = "auto"

		var resp struct {
			Logs []struct {
				PIN        string `json:"pin"`
				Name       string `json:"name"`
				Timestamp  string `json:"timestamp"`
				VerifyMode int    `json:"verify_mode"`
				InOutMode  int    `json:"in_out_mode"`
				WorkCode   int    `json:"work_code"`
			} `json:"logs"`
			ModeUsed       string `json:"mode_used"`
			InvalidDropped int    `json:"invalid_dropped"`
		}
		err := s.worker.Call(ctx, "pull_logs", params, &resp)
		cancel()
		if err != nil {
			summaries = append(summaries, models.PullLogsDeviceSummary{
				DeviceID:   d.ID,
				DeviceName: d.Name,
				Error:      err.Error(),
			})
			continue
		}

		devCount := 0
		for _, item := range resp.Logs {
			key := d.ID + "|" + item.PIN + "|" + item.Timestamp
			if seen[key] {
				continue
			}
			seen[key] = true
			name := item.Name
			if name == "" {
				name = fallbackNames[item.PIN]
			}
			merged = append(merged, models.AttendanceRecord{
				DeviceID:    d.ID,
				DeviceName:  d.Name,
				SensorID:    d.SensorID,
				PIN:         item.PIN,
				Name:        name,
				Timestamp:   item.Timestamp,
				VerifyMode:  item.VerifyMode,
				VerifyLabel: verifyLabel(item.VerifyMode),
				InOutMode:   item.InOutMode,
				InOutLabel:  inOutLabel(item.InOutMode),
				WorkCode:    item.WorkCode,
			})
			devCount++
		}
		summaries = append(summaries, models.PullLogsDeviceSummary{
			DeviceID:       d.ID,
			DeviceName:     d.Name,
			Count:          devCount,
			ModeUsed:       resp.ModeUsed,
			InvalidDropped: resp.InvalidDropped,
		})
	}

	var pullErrs []string
	for _, sm := range summaries {
		if sm.Error != "" {
			pullErrs = append(pullErrs, sm.DeviceName+": "+sm.Error)
		}
	}
	if len(pullErrs) == len(targets) {
		errMsg := strings.Join(pullErrs, "；")
		s.recordAudit(
			"pull_logs",
			"拉取考勤",
			fmt.Sprintf("%s ~ %s (%d 台)", req.StartDate, req.EndDate, len(targets)),
			"全部所选考勤机拉取失败",
			false,
			errMsg,
			"",
		)
		s.emitProgress("logs", "考勤拉取失败", len(targets), len(targets), true)
		return models.PullLogsResponse{}, fmt.Errorf("全部所选考勤机拉取失败：%s", errMsg)
	}

	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Timestamp != merged[j].Timestamp {
			return merged[i].Timestamp > merged[j].Timestamp
		}
		return merged[i].PIN < merged[j].PIN
	})

	merged = s.enrichDepartments(merged)
	addedNew, archiveTotal, err := s.store.MergeAttendanceArchive(merged)
	if err != nil {
		s.recordAudit(
			"pull_logs",
			"拉取考勤",
			fmt.Sprintf("%s ~ %s (%d 台)", req.StartDate, req.EndDate, len(targets)),
			fmt.Sprintf("拉取到 %d 条，但写入本地考勤累计库失败", len(merged)),
			false,
			err.Error(),
			"",
		)
		s.emitProgress("logs", "写入本地考勤累计库失败", len(targets), len(targets), true)
		return models.PullLogsResponse{}, fmt.Errorf("写入本地考勤累计库失败: %w", err)
	}

	// Return full archived view for the requested range & devices so newly pulled + earlier cached records stay consistent
	rangeRecords, _, _ := s.store.QueryAttendanceArchive(req.StartDate, req.EndDate, req.DeviceIDs)
	if len(rangeRecords) < len(merged) {
		rangeRecords = merged
	}
	rangeRecords = s.enrichDepartments(rangeRecords)

	out := models.PullLogsResponse{
		Records:      rangeRecords,
		DailySummary: exporter.BuildDailySummary(rangeRecords),
		Total:        len(rangeRecords),
		AddedNew:     addedNew,
		ArchiveTotal: archiveTotal,
		Summaries:    summaries,
		PulledAt:     time.Now().Format("2006-01-02 15:04:05"),
	}
	_ = s.store.SaveLastLogs(out)

	s.recordAudit(
		"pull_logs",
		"拉取考勤",
		fmt.Sprintf("%s ~ %s (%d 台)", req.StartDate, req.EndDate, len(targets)),
		fmt.Sprintf("本次拉取 %d 条，新增入库 %d 条，累计库 %d 条", len(merged), addedNew, archiveTotal),
		len(pullErrs) == 0,
		strings.Join(pullErrs, "；"),
		"",
	)
	s.emitProgress("logs", fmt.Sprintf("考勤拉取完成，本次 %d 条（新增入库 %d 条）", len(merged), addedNew), len(targets), len(targets), true)
	return out, nil
}

// ExportAttendanceLogs opens a native macOS Save dialog and exports records as xlsx/csv/txt/abt.
func (s *ZKService) ExportAttendanceLogs(format string, records []models.AttendanceRecord) (string, error) {
	if s.app == nil {
		return "", errors.New("应用上下文未就绪")
	}
	if len(records) == 0 {
		return "", errors.New("当前没有可导出的考勤记录")
	}

	stamp := time.Now().Format("20060102_1504")
	var defaultName, filterLabel, filterPattern string
	switch format {
	case "abt":
		defaultName = fmt.Sprintf("%s_考勤记录.abt", time.Now().Format("20060102"))
		filterLabel, filterPattern = "ZKTime 考勤数据 (*.abt)", "*.abt"
	case "csv":
		defaultName = fmt.Sprintf("考勤记录_%s.csv", stamp)
		filterLabel, filterPattern = "CSV 文件", "*.csv"
	case "txt":
		defaultName = fmt.Sprintf("考勤记录_ZKTime_%s.txt", stamp)
		filterLabel, filterPattern = "ZKTime TXT 文件", "*.txt"
	default:
		format = "xlsx"
		defaultName = fmt.Sprintf("考勤记录_%s.xlsx", stamp)
		filterLabel, filterPattern = "Excel 工作簿", "*.xlsx"
	}

	dest, err := s.app.Dialog.SaveFile().
		SetMessage("导出考勤记录").
		SetFilename(defaultName).
		AddFilter(filterLabel, filterPattern).
		PromptForSingleSelection()
	if err != nil || dest == "" {
		return "", err
	}

	switch format {
	case "abt":
		err = exporter.ExportABT(dest, records)
	case "csv":
		err = exporter.ExportCSV(dest, records)
	case "txt":
		err = exporter.ExportZKTimeTXT(dest, records)
	default:
		err = exporter.ExportXLSX(dest, records)
	}
	if err != nil {
		return "", err
	}
	return dest, nil
}

func sanitizeExportPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", "*", "", "?", "", "\"", "", "<", "", ">", "", "|", "-", " ", "_",
	)
	clean := replacer.Replace(prefix)
	runes := []rune(clean)
	if len(runes) > 48 {
		clean = string(runes[:48])
	}
	return strings.Trim(clean, "_-")
}

// ExportQueriedLogs exports the exact currently queried/filtered attendance records or daily summary
// to xlsx, abt, csv (raw), summary_csv (daily summary), or ZKTime txt with descriptive filenames.
func (s *ZKService) ExportQueriedLogs(req models.ExportLogsRequest) (string, error) {
	if s.app == nil {
		return "", errors.New("应用上下文未就绪")
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "xlsx"
	}

	summaryRows := req.DailySummary
	if format == "summary_csv" {
		if len(summaryRows) == 0 && len(req.Records) > 0 {
			summaryRows = exporter.BuildDailySummary(req.Records)
		}
		if len(summaryRows) == 0 {
			return "", errors.New("当前查询没有可导出的每日汇总数据")
		}
	} else if len(req.Records) == 0 {
		return "", errors.New("当前查询结果为空，无法导出")
	}

	stamp := time.Now().Format("20060102_1504")
	prefix := sanitizeExportPrefix(req.FilePrefix)
	if prefix == "" {
		prefix = "考勤查询_" + stamp
	}

	var defaultName, filterLabel, filterPattern, formatLabel string
	switch format {
	case "abt":
		defaultName = prefix + ".abt"
		filterLabel, filterPattern, formatLabel = "ZKTime 考勤数据 (*.abt)", "*.abt", "ZKTime .ABT"
	case "csv":
		defaultName = prefix + "_原始流水.csv"
		filterLabel, filterPattern, formatLabel = "CSV 文件", "*.csv", "原始流水 CSV"
	case "summary_csv":
		defaultName = prefix + "_每日汇总.csv"
		filterLabel, filterPattern, formatLabel = "CSV 文件", "*.csv", "每日汇总 CSV"
	case "txt":
		defaultName = prefix + "_ZKTime.txt"
		filterLabel, filterPattern, formatLabel = "ZKTime TXT 文件", "*.txt", "ZKTime TXT"
	default:
		format = "xlsx"
		defaultName = prefix + ".xlsx"
		filterLabel, filterPattern, formatLabel = "Excel 工作簿", "*.xlsx", "双表 Excel"
	}

	dest, err := s.app.Dialog.SaveFile().
		SetMessage("导出查询的考勤数据").
		SetFilename(defaultName).
		AddFilter(filterLabel, filterPattern).
		PromptForSingleSelection()
	if err != nil || dest == "" {
		return "", err
	}

	switch format {
	case "abt":
		err = exporter.ExportABT(dest, req.Records)
	case "csv":
		err = exporter.ExportCSV(dest, req.Records)
	case "summary_csv":
		err = exporter.ExportDailySummaryCSV(dest, summaryRows)
	case "txt":
		err = exporter.ExportZKTimeTXT(dest, req.Records)
	default:
		err = exporter.ExportXLSX(dest, req.Records)
	}
	if err != nil {
		return "", err
	}

	countInfo := fmt.Sprintf("%d 条打卡流水", len(req.Records))
	if format == "summary_csv" {
		countInfo = fmt.Sprintf("%d 条每日汇总", len(summaryRows))
	}
	if strings.TrimSpace(req.ScopeNote) != "" {
		countInfo = req.ScopeNote + " · " + countInfo
	}
	s.recordAudit("export_logs", "导出查询考勤", filepath.Base(dest), fmt.Sprintf("%s (%s)", formatLabel, countInfo), true, "", dest)
	return dest, nil
}

// ImportABTFile parses a ZKTime 5.0 .ABT (or .TXT) file, merges it into the persistent archive,
// and returns the imported view for display.
func (s *ZKService) ImportABTFile(path string) (models.PullLogsResponse, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return models.PullLogsResponse{}, errors.New("文件路径为空")
	}
	allDevs, _ := s.store.LoadDevices()
	devBySensor := make(map[string]models.DeviceConfig, len(allDevs))
	for _, d := range allDevs {
		if d.SensorID != "" {
			devBySensor[d.SensorID] = d
		}
	}

	records, err := exporter.ParseABTFile(path, devBySensor)
	if err != nil {
		return models.PullLogsResponse{}, err
	}
	if len(records) == 0 {
		return models.PullLogsResponse{}, errors.New("文件中未解析出有效考勤记录")
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].Timestamp != records[j].Timestamp {
			return records[i].Timestamp > records[j].Timestamp
		}
		return records[i].PIN < records[j].PIN
	})

	records = s.enrichDepartments(records)
	addedNew, archiveTotal, err := s.store.MergeAttendanceArchive(records)
	if err != nil {
		return models.PullLogsResponse{}, err
	}

	// Per-device / sensor breakdown
	countByDev := map[string]*models.PullLogsDeviceSummary{}
	order := make([]string, 0)
	for _, r := range records {
		sm, ok := countByDev[r.DeviceID]
		if !ok {
			sm = &models.PullLogsDeviceSummary{
				DeviceID:   r.DeviceID,
				DeviceName: r.DeviceName,
				ModeUsed:   "ABT导入",
			}
			countByDev[r.DeviceID] = sm
			order = append(order, r.DeviceID)
		}
		sm.Count++
	}
	sort.Strings(order)
	summaries := make([]models.PullLogsDeviceSummary, 0, len(order))
	for _, id := range order {
		summaries = append(summaries, *countByDev[id])
	}

	out := models.PullLogsResponse{
		Records:      records,
		DailySummary: exporter.BuildDailySummary(records),
		Total:        len(records),
		AddedNew:     addedNew,
		ArchiveTotal: archiveTotal,
		Summaries:    summaries,
		PulledAt:     time.Now().Format("2006-01-02 15:04:05"),
	}
	_ = s.store.SaveLastLogs(out)

	s.recordAudit(
		"import_abt",
		"导入 ABT/TXT 考勤",
		filepath.Base(path),
		fmt.Sprintf("解析 %d 条，新增入库 %d 条，累计库 %d 条", len(records), addedNew, archiveTotal),
		true,
		"",
		"",
	)
	return out, nil
}

// ImportABTLogsFromDialog opens a native file picker for .abt / .txt files and merges them into the archive.
func (s *ZKService) ImportABTLogsFromDialog() (models.PullLogsResponse, error) {
	if s.app == nil {
		return models.PullLogsResponse{}, errors.New("应用上下文未就绪")
	}
	path, err := s.app.Dialog.OpenFile().
		SetTitle("选择要导入的 ZKTime .ABT / .TXT 考勤文件").
		AddFilter("ZKTime 考勤数据 (*.abt;*.txt)", "*.abt;*.txt").
		PromptForSingleSelection()
	if err != nil || path == "" {
		return models.PullLogsResponse{}, err
	}
	return s.ImportABTFile(path)
}

func (s *ZKService) GetOperationLogs() []models.OperationLog {
	return s.store.LoadOperationLogs()
}

func (s *ZKService) ClearOperationLogs() error {
	return s.store.ClearOperationLogs()
}

func fileDirURL(dir string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(dir)}).String()
}

func (s *ZKService) OpenBackupsDirectory() error {
	if s.app == nil {
		return errors.New("应用上下文未就绪")
	}
	return s.app.Browser.OpenURL(fileDirURL(s.store.BackupsDir()))
}

func (s *ZKService) GetDataDirectory() string {
	return s.store.DataDir()
}

func (s *ZKService) OpenDataDirectory() error {
	if s.app == nil {
		return errors.New("应用上下文未就绪")
	}
	return s.app.Browser.OpenURL(fileDirURL(s.store.DataDir()))
}
