package exporter

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"zk_mac/internal/models"
)

func TestLH5RoundTripEdgeCases(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	randBuf := make([]byte, 80000)
	rng.Read(randBuf)

	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"single_byte", []byte{'A'}},
		{"all_identical_10k", bytes.Repeat([]byte{'Z'}, 10000)},
		{"short_text", []byte("908\t2026-01-01 16:01:50\t32\t15\t\xd5\xc5\xc8\xfd\tI\t0\t0\r\n")},
		{"repetitive_100k", bytes.Repeat([]byte("3226\t2026-01-01 07:56:26\t32\t1\t\xc0\xee\xcb\xc4\tI\t0\t0\r\n"), 2500)},
		{"incompressible_random_80k", randBuf},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc := EncodeLH5(tc.data)
			dec, err := DecodeLH5(enc)
			if err != nil {
				t.Fatalf("DecodeLH5(%s) failed: %v", tc.name, err)
			}
			if !bytes.Equal(dec, tc.data) {
				t.Fatalf("DecodeLH5(%s) mismatch: got len %d, want %d", tc.name, len(dec), len(tc.data))
			}
			t.Logf("%s: raw=%d -> lh5=%d (%.1f%%)", tc.name, len(tc.data), len(enc),
				func() float64 {
					if len(tc.data) == 0 {
						return 0
					}
					return 100 * float64(len(enc)) / float64(len(tc.data))
				}())
		})
	}
}

func TestABTExportAndParseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	records := []models.AttendanceRecord{
		{
			DeviceID:   "dev_b",
			DeviceName: "东门考勤机",
			SensorID:   "32",
			PIN:        "101",
			Name:       "张三",
			Timestamp:  "2026-01-01 16:01:50",
			VerifyMode: 15,
			InOutMode:  0,
			WorkCode:   0,
		},
		{
			DeviceID:   "dev_a",
			DeviceName: "西门考勤机",
			SensorID:   "31",
			PIN:        "102",
			Name:       "李四",
			Timestamp:  "2026-01-02 18:10:05",
			VerifyMode: 1,
			InOutMode:  1,
			WorkCode:   0,
		},
		{
			DeviceID:   "sensor_21",
			DeviceName: "外部考勤机 (21)",
			SensorID:   "21",
			PIN:        "103",
			Name:       "王五",
			Timestamp:  "2026-01-01 08:46:37",
			VerifyMode: 1,
			InOutMode:  0,
			WorkCode:   0,
		},
	}

	abtPath := filepath.Join(dir, "20260201_test.abt")
	if err := ExportABT(abtPath, records); err != nil {
		t.Fatalf("ExportABT failed: %v", err)
	}

	devBySensor := map[string]models.DeviceConfig{
		"31": {ID: "dev_a", Name: "西门考勤机", SensorID: "31"},
		"32": {ID: "dev_b", Name: "东门考勤机", SensorID: "32"},
	}
	parsed, err := ParseABTFile(abtPath, devBySensor)
	if err != nil {
		t.Fatalf("ParseABTFile failed: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("expected 3 records, got %d", len(parsed))
	}
	// Sorted ascending by Timestamp: 08:46:37 (103), 16:01:50 (101), 18:10:05 (102)
	if parsed[0].PIN != "103" || parsed[0].SensorID != "21" || parsed[0].DeviceID != "sensor_21" {
		t.Fatalf("unexpected parsed[0]: %+v", parsed[0])
	}
	if parsed[1].PIN != "101" || parsed[1].Name != "张三" || parsed[1].DeviceID != "dev_b" || parsed[1].VerifyMode != 15 || parsed[1].InOutMode != 0 {
		t.Fatalf("unexpected parsed[1]: %+v", parsed[1])
	}
	if parsed[2].PIN != "102" || parsed[2].Name != "李四" || parsed[2].DeviceID != "dev_a" || parsed[2].InOutMode != 1 {
		t.Fatalf("unexpected parsed[2]: %+v", parsed[2])
	}
}

func TestRealSampleABTCompatibility(t *testing.T) {
	samplePath := os.Getenv("ZK_SAMPLE_ABT")
	if samplePath == "" {
		t.Skip("set ZK_SAMPLE_ABT=/path/to/sample.abt to verify against a real .abt file")
	}
	rawABT, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatalf("read sample .abt failed: %v", err)
	}

	plain, err := DecodeLH5(rawABT)
	if err != nil {
		t.Fatalf("DecodeLH5(real sample) failed: %v", err)
	}
	reencoded := EncodeLH5(plain)
	redecoded, err := DecodeLH5(reencoded)
	if err != nil {
		t.Fatalf("DecodeLH5(reencoded) failed: %v", err)
	}
	if !bytes.Equal(redecoded, plain) {
		t.Fatalf("reencoded round-trip byte mismatch")
	}
	recs, err := ParseABTFile(samplePath, nil)
	if err != nil || len(recs) == 0 {
		t.Fatalf("ParseABTFile(real sample) failed: count=%d err=%v", len(recs), err)
	}
	t.Logf("Real sample: origABT=%d bytes, ourABT=%d bytes, plain=%d bytes, records=%d", len(rawABT), len(reencoded), len(plain), len(recs))
}
