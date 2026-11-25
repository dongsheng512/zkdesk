package exporter

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/image/draw"
	"golang.org/x/text/encoding/simplifiedchinese"

	"zk_mac/internal/models"
)

// BuildDailySummary groups attendance records by (date, pin) and computes first/last punches,
// duration span, and single-punch abnormal warnings.
func BuildDailySummary(records []models.AttendanceRecord) []models.DailySummaryRow {
	type bucket struct {
		date  string
		pin   string
		name  string
		dept  string
		items []models.AttendanceRecord
	}
	byKey := map[string]*bucket{}
	for _, r := range records {
		if len(r.Timestamp) < 10 {
			continue
		}
		day := r.Timestamp[:10]
		key := day + "|" + r.PIN
		b, ok := byKey[key]
		if !ok {
			b = &bucket{date: day, pin: r.PIN, name: r.Name, dept: r.Department}
			byKey[key] = b
		}
		if b.name == "" && r.Name != "" {
			b.name = r.Name
		}
		if b.dept == "" && r.Department != "" {
			b.dept = r.Department
		}
		b.items = append(b.items, r)
	}

	out := make([]models.DailySummaryRow, 0, len(byKey))
	for _, b := range byKey {
		sort.Slice(b.items, func(i, j int) bool {
			return b.items[i].Timestamp < b.items[j].Timestamp
		})
		first := b.items[0]
		last := b.items[len(b.items)-1]

		extractClock := func(ts string) string {
			if len(ts) >= 19 {
				return ts[11:19]
			}
			return ts
		}
		allTimes := make([]string, 0, len(b.items))
		for _, it := range b.items {
			allTimes = append(allTimes, extractClock(it.Timestamp))
		}

		duration := "-"
		abnormal := false
		abnormalNote := ""
		if len(b.items) == 1 {
			abnormal = true
			abnormalNote = "仅 1 次打卡（可能漏签）"
		} else {
			t1, e1 := time.ParseInLocation("2006-01-02 15:04:05", first.Timestamp, time.Local)
			t2, e2 := time.ParseInLocation("2006-01-02 15:04:05", last.Timestamp, time.Local)
			if e1 == nil && e2 == nil && !t2.Before(t1) {
				mins := int(t2.Sub(t1).Minutes())
				if mins < 5 {
					abnormal = true
					abnormalNote = "重复打卡（间隔<5分钟）"
				}
				duration = fmt.Sprintf("%dh %02dm", mins/60, mins%60)
			}
		}

		lastClock := ""
		lastDev := ""
		if len(b.items) > 1 {
			lastClock = extractClock(last.Timestamp)
			lastDev = last.DeviceName
		}

		out = append(out, models.DailySummaryRow{
			Date:         b.date,
			PIN:          b.pin,
			Name:         b.name,
			Department:   b.dept,
			FirstTime:    extractClock(first.Timestamp),
			LastTime:     lastClock,
			FirstDevice:  first.DeviceName,
			LastDevice:   lastDev,
			PunchCount:   len(b.items),
			WorkDuration: duration,
			Abnormal:     abnormal,
			AbnormalNote: abnormalNote,
			AllTimes:     allTimes,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		ni, ei := strconv.Atoi(out[i].PIN)
		nj, ej := strconv.Atoi(out[j].PIN)
		if ei == nil && ej == nil {
			return ni < nj
		}
		return out[i].PIN < out[j].PIN
	})
	return out
}

// ExportXLSX writes a dual-sheet Excel workbook:
// Sheet 1: 每日首末打卡汇总 (highlights abnormal rows)
// Sheet 2: 原始打卡流水
func ExportXLSX(path string, records []models.AttendanceRecord) error {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#FFFFFF", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#1E40AF"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	cellStyle, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	warnStyle, _ := f.NewStyle(&excelize.Style{
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FEF3C7"}, Pattern: 1},
		Font:      &excelize.Font{Color: "#92400E"},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})

	// Sheet 1: Daily summary
	const summarySheet = "每日首末打卡"
	if idx, err := f.GetSheetIndex("Sheet1"); err == nil && idx >= 0 {
		_ = f.SetSheetName("Sheet1", summarySheet)
	}
	summaryHeaders := []string{"日期", "工号", "姓名", "部门", "首次打卡", "末次打卡", "打卡次数", "跨度时长", "状态提醒", "首次设备", "末次设备", "当日全部打卡"}
	for col, h := range summaryHeaders {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		_ = f.SetCellValue(summarySheet, cell, h)
		_ = f.SetCellStyle(summarySheet, cell, cell, headerStyle)
	}
	_ = f.SetRowHeight(summarySheet, 1, 24)

	daily := BuildDailySummary(records)
	for i, d := range daily {
		row := i + 2
		note := "正常"
		if d.Abnormal {
			note = d.AbnormalNote
		}
		vals := []any{
			d.Date, d.PIN, d.Name, d.Department, d.FirstTime, d.LastTime,
			d.PunchCount, d.WorkDuration, note,
			d.FirstDevice, d.LastDevice, strings.Join(d.AllTimes, " / "),
		}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = f.SetCellValue(summarySheet, cell, v)
		}
		startCell, _ := excelize.CoordinatesToCellName(1, row)
		endCell, _ := excelize.CoordinatesToCellName(len(summaryHeaders), row)
		if d.Abnormal {
			_ = f.SetCellStyle(summarySheet, startCell, endCell, warnStyle)
		} else {
			_ = f.SetCellStyle(summarySheet, startCell, endCell, cellStyle)
		}
	}
	for col, w := range map[string]float64{
		"A": 13, "B": 10, "C": 13, "D": 15, "E": 12, "F": 12,
		"G": 10, "H": 12, "I": 22, "J": 22, "K": 22, "L": 30,
	} {
		_ = f.SetColWidth(summarySheet, col, col, w)
	}
	_ = f.SetPanes(summarySheet, &excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	})

	// Sheet 2: Raw attendance logs
	const rawSheet = "原始打卡流水"
	_, _ = f.NewSheet(rawSheet)
	headers := []string{"工号", "姓名", "部门", "打卡时间", "设备名称", "设备编号", "验证方式", "考勤状态"}
	for col, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		_ = f.SetCellValue(rawSheet, cell, h)
		_ = f.SetCellStyle(rawSheet, cell, cell, headerStyle)
	}
	_ = f.SetRowHeight(rawSheet, 1, 24)

	for i, r := range records {
		row := i + 2
		vals := []any{r.PIN, r.Name, r.Department, r.Timestamp, r.DeviceName, r.SensorID, r.VerifyLabel, r.InOutLabel}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = f.SetCellValue(rawSheet, cell, v)
		}
		startCell, _ := excelize.CoordinatesToCellName(1, row)
		endCell, _ := excelize.CoordinatesToCellName(len(headers), row)
		_ = f.SetCellStyle(rawSheet, startCell, endCell, cellStyle)
	}
	for col, w := range map[string]float64{
		"A": 12, "B": 14, "C": 15, "D": 22, "E": 24, "F": 10, "G": 12, "H": 12,
	} {
		_ = f.SetColWidth(rawSheet, col, col, w)
	}
	_ = f.SetPanes(rawSheet, &excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	})

	return f.SaveAs(path)
}

// ExportCSV writes UTF-8 with BOM CSV (Excel-friendly on macOS & Windows).
func ExportCSV(path string, records []models.AttendanceRecord) error {
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"工号", "姓名", "部门", "打卡时间", "设备名称", "设备编号", "验证方式", "考勤状态"})
	for _, r := range records {
		_ = w.Write([]string{r.PIN, r.Name, r.Department, r.Timestamp, r.DeviceName, r.SensorID, r.VerifyLabel, r.InOutLabel})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ExportDailySummaryCSV writes UTF-8 with BOM CSV for the daily first/last punch summary table.
func ExportDailySummaryCSV(path string, rows []models.DailySummaryRow) error {
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"日期", "工号", "姓名", "部门", "首次打卡", "末次打卡", "打卡次数", "跨度时长", "状态提醒", "首次设备", "末次设备", "当日全部打卡"})
	for _, d := range rows {
		note := "正常"
		if d.Abnormal {
			note = d.AbnormalNote
		}
		_ = w.Write([]string{
			d.Date,
			d.PIN,
			d.Name,
			d.Department,
			d.FirstTime,
			d.LastTime,
			strconv.Itoa(d.PunchCount),
			d.WorkDuration,
			note,
			d.FirstDevice,
			d.LastDevice,
			strings.Join(d.AllTimes, " / "),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ExportZKTimeTXT writes GBK-encoded Tab-separated TXT directly importable by legacy ZKTime 5.0 Att.exe.
func ExportZKTimeTXT(path string, records []models.AttendanceRecord) error {
	encoded, err := FormatZKTimePlaintext(records)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}

// NormalizeFaceJPEG decodes a JPEG or PNG photo, resizes it cleanly to fit ZKTeco xFace
// visible-light constraints (max 640px edge), and encodes baseline JPEG <= ~140KB.
func NormalizeFaceJPEG(inputPath string) ([]byte, error) {
	f, err := os.Open(inputPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("无法解析图片（请使用 JPG 或 PNG 格式）: %w", err)
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	const maxDim = 640

	var dst image.Image = src
	if w > maxDim || h > maxDim {
		var nw, nh int
		if w >= h {
			nw = maxDim
			nh = int(float64(h) * float64(maxDim) / float64(w))
		} else {
			nh = maxDim
			nw = int(float64(w) * float64(maxDim) / float64(h))
		}
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		resized := image.NewRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(resized, resized.Bounds(), src, bounds, draw.Over, nil)
		dst = resized
	}

	for _, quality := range []int{88, 78, 68, 58} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		if buf.Len() <= 140*1024 || quality == 58 {
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("图片编码失败")
}

// ExtractPINFromFilename parses leading numeric employee PIN from filenames like
// "103.jpg", "103_张三.jpg", "103-张三.png".
func ExtractPINFromFilename(name string) string {
	base := name
	if dot := strings.LastIndexByte(base, '.'); dot > 0 {
		base = base[:dot]
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	var digits strings.Builder
	for _, r := range base {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		} else {
			break
		}
	}
	return digits.String()
}

// ParseProfilesFile reads an Excel (.xlsx) or CSV (.csv) mapping PIN -> Department / WorkStatus / Note.
func ParseProfilesFile(path string) ([]models.UserProfile, int, error) {
	var rows [][]string
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".xlsx") {
		f, err := excelize.OpenFile(path)
		if err != nil {
			return nil, 0, fmt.Errorf("打开 Excel 失败: %w", err)
		}
		defer func() { _ = f.Close() }()
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return nil, 0, fmt.Errorf("Excel 文件没有工作表")
		}
		r, err := f.GetRows(sheets[0])
		if err != nil {
			return nil, 0, err
		}
		rows = r
	} else {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, err
		}
		raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
		// Auto-detect GBK if not valid UTF-8
		text := string(raw)
		if strings.ContainsRune(text, '\uFFFD') {
			if dec, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw); err == nil {
				text = string(dec)
			}
		}
		cr := csv.NewReader(strings.NewReader(text))
		cr.FieldsPerRecord = -1
		r, err := cr.ReadAll()
		if err != nil {
			return nil, 0, fmt.Errorf("解析 CSV 失败: %w", err)
		}
		rows = r
	}

	if len(rows) < 2 {
		return nil, 0, fmt.Errorf("文件内容为空或缺少数据行")
	}

	pinCol, deptCol, statusCol, noteCol := -1, -1, -1, -1
	for i, h := range rows[0] {
		k := strings.ToLower(strings.TrimSpace(h))
		switch {
		case k == "工号" || k == "考勤号码" || k == "pin" || k == "badgenumber" || k == "userid" || k == "编号":
			pinCol = i
		case k == "部门" || k == "department" || k == "dept" || k == "deptname":
			deptCol = i
		case k == "状态" || k == "在职状态" || k == "status" || k == "workstatus" || k == "离职":
			statusCol = i
		case k == "备注" || k == "note" || k == "remark":
			noteCol = i
		}
	}
	if pinCol < 0 {
		return nil, 0, fmt.Errorf("未识别到「工号 / PIN / BadgeNumber」列，请检查表头")
	}

	cell := func(row []string, idx int) string {
		if idx < 0 || idx >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[idx])
	}

	profiles := make([]models.UserProfile, 0, len(rows)-1)
	skipped := 0
	for _, row := range rows[1:] {
		pin := cell(row, pinCol)
		if pin == "" {
			skipped++
			continue
		}
		dept := cell(row, deptCol)
		stRaw := strings.ToLower(cell(row, statusCol))
		status := ""
		if stRaw != "" {
			if strings.Contains(stRaw, "离职") || stRaw == "resigned" || stRaw == "inactive" || stRaw == "0" || stRaw == "否" {
				status = "resigned"
			} else if strings.Contains(stRaw, "在职") || stRaw == "active" || stRaw == "1" || stRaw == "是" {
				status = "active"
			}
		}
		note := cell(row, noteCol)
		profiles = append(profiles, models.UserProfile{
			PIN:        pin,
			Department: dept,
			WorkStatus: status,
			Note:       note,
		})
	}
	return profiles, skipped, nil
}

// ExportMasterUsersXLSX exports the authoritative local master staff directory to an Excel workbook.
func ExportMasterUsersXLSX(path string, users []models.MasterUser, hasFace map[string]bool) error {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	const sheet = "本地人员主库"
	if idx, err := f.GetSheetIndex("Sheet1"); err == nil && idx >= 0 {
		_ = f.SetSheetName("Sheet1", sheet)
	}

	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#FFFFFF", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#1E40AF"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	cellStyle, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	resignedStyle, _ := f.NewStyle(&excelize.Style{
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FEE2E2"}, Pattern: 1},
		Font:      &excelize.Font{Color: "#991B1B"},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})

	headers := []string{"工号", "姓名", "部门", "在职状态", "设备权限", "允许验证", "卡号", "密码状态", "主库人脸", "主库指纹数", "指纹槽位", "备注", "最后更新时间"}
	for col, h := range headers {
		c, _ := excelize.CoordinatesToCellName(col+1, 1)
		_ = f.SetCellValue(sheet, c, h)
		_ = f.SetCellStyle(sheet, c, c, headerStyle)
	}
	_ = f.SetRowHeight(sheet, 1, 24)

	sorted := append([]models.MasterUser(nil), users...)
	sort.Slice(sorted, func(i, j int) bool {
		ni, ei := strconv.Atoi(sorted[i].PIN)
		nj, ej := strconv.Atoi(sorted[j].PIN)
		if ei == nil && ej == nil {
			return ni < nj
		}
		return sorted[i].PIN < sorted[j].PIN
	})

	for i, u := range sorted {
		row := i + 2
		statusLabel := "在职"
		if u.WorkStatus == "resigned" {
			statusLabel = "离职"
		}
		privLabel := "普通员工"
		switch u.Privilege {
		case 2:
			privLabel = "登记员"
		case 14:
			privLabel = "超级管理员"
		}
		enabledLabel := "是"
		if !u.Enabled {
			enabledLabel = "否"
		}
		pwdLabel := "未设"
		if u.Password != "" {
			pwdLabel = "已设"
		}
		faceLabel := "无"
		if hasFace[u.PIN] {
			faceLabel = "有"
		}
		fpSlots := make([]string, 0, len(u.Fingerprints))
		for _, fp := range u.Fingerprints {
			fpSlots = append(fpSlots, strconv.Itoa(fp.FingerID))
		}
		vals := []any{
			u.PIN, u.Name, u.Department, statusLabel, privLabel, enabledLabel,
			u.Card, pwdLabel, faceLabel, len(u.Fingerprints), strings.Join(fpSlots, ","),
			u.Note, u.UpdatedAt,
		}
		for col, v := range vals {
			c, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = f.SetCellValue(sheet, c, v)
		}
		startCell, _ := excelize.CoordinatesToCellName(1, row)
		endCell, _ := excelize.CoordinatesToCellName(len(headers), row)
		if u.WorkStatus == "resigned" {
			_ = f.SetCellStyle(sheet, startCell, endCell, resignedStyle)
		} else {
			_ = f.SetCellStyle(sheet, startCell, endCell, cellStyle)
		}
	}
	for col, w := range map[string]float64{
		"A": 10, "B": 14, "C": 16, "D": 10, "E": 12, "F": 10,
		"G": 15, "H": 10, "I": 10, "J": 12, "K": 14, "L": 22, "M": 20,
	} {
		_ = f.SetColWidth(sheet, col, col, w)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	})

	return f.SaveAs(path)
}
