package exporter

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"

	"zk_mac/internal/models"
)

func TestExportFormats(t *testing.T) {
	dir := t.TempDir()
	records := []models.AttendanceRecord{
		{
			DeviceID:    "230",
			DeviceName:  "考勤机 230",
			SensorID:    "30",
			PIN:         "103",
			Name:        "张三",
			Department:  "研发部",
			Timestamp:   "2026-11-22 08:51:13",
			VerifyMode:  15,
			VerifyLabel: "人脸",
			InOutMode:   0,
			InOutLabel:  "上班签到",
		},
	}

	xlsxPath := filepath.Join(dir, "out.xlsx")
	if err := ExportXLSX(xlsxPath, records); err != nil {
		t.Fatalf("ExportXLSX failed: %v", err)
	}
	f, err := excelize.OpenFile(xlsxPath)
	if err != nil {
		t.Fatalf("open xlsx failed: %v", err)
	}
	defer f.Close()
	val, err := f.GetCellValue("原始打卡流水", "C2")
	if err != nil || val != "研发部" {
		t.Fatalf("unexpected raw sheet dept cell C2=%q err=%v", val, err)
	}
	sumVal, err := f.GetCellValue("每日首末打卡", "D2")
	if err != nil || sumVal != "研发部" {
		t.Fatalf("unexpected summary sheet dept cell D2=%q err=%v", sumVal, err)
	}

	csvPath := filepath.Join(dir, "out.csv")
	if err := ExportCSV(csvPath, records); err != nil {
		t.Fatalf("ExportCSV failed: %v", err)
	}

	txtPath := filepath.Join(dir, "out.txt")
	if err := ExportZKTimeTXT(txtPath, records); err != nil {
		t.Fatalf("ExportZKTimeTXT failed: %v", err)
	}
	raw, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err != nil {
		t.Fatalf("gbk decode failed: %v", err)
	}
	if len(decoded) == 0 {
		t.Fatal("empty txt output")
	}
}

func TestNormalizeFaceJPEG(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "large.png")

	img := image.NewRGBA(image.Rect(0, 0, 1200, 900))
	for y := 0; y < 900; y++ {
		for x := 0; x < 1200; x++ {
			img.Set(x, y, color.RGBA{R: 120, G: 160, B: 200, A: 255})
		}
	}
	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	out, err := NormalizeFaceJPEG(srcPath)
	if err != nil {
		t.Fatalf("NormalizeFaceJPEG failed: %v", err)
	}
	if len(out) < 4 || out[0] != 0xFF || out[1] != 0xD8 {
		t.Fatalf("output is not valid JPEG (len=%d)", len(out))
	}
}

func TestExportMasterUsersXLSX(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "master.xlsx")
	users := []models.MasterUser{
		{
			PIN:        "908",
			Name:       "李四",
			Department: "技术部",
			WorkStatus: "active",
			Privilege:  14,
			Enabled:    true,
			Card:       "123456",
			Password:   "pwd",
			Fingerprints: []models.FingerprintTemplate{
				{FingerID: 0, Flag: 1, Template: "T0"},
				{FingerID: 2, Flag: 1, Template: "T2"},
			},
			UpdatedAt: "2026-11-22 18:00:00",
		},
	}
	if err := ExportMasterUsersXLSX(outPath, users, map[string]bool{"908": true}); err != nil {
		t.Fatalf("ExportMasterUsersXLSX failed: %v", err)
	}
	f, err := excelize.OpenFile(outPath)
	if err != nil {
		t.Fatalf("open master.xlsx failed: %v", err)
	}
	defer f.Close()
	if v, _ := f.GetCellValue("本地人员主库", "B2"); v != "李四" {
		t.Fatalf("expected B2=李四, got %q", v)
	}
	if v, _ := f.GetCellValue("本地人员主库", "K2"); v != "0,2" {
		t.Fatalf("expected K2=0,2 (fp slots), got %q", v)
	}
}
