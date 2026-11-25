package store

import (
	"testing"

	"zk_mac/internal/models"
)

func TestRepairZKName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Ê¾ÀýÁ\u02c6", "示例翀"}, // GBK 0xC1 0x88 -> CP1252 'Á' + U+02C6
		{"Ê¾Àý\u2020´", "示例喆"}, // GBK 0x86 0xB4 -> CP1252 U+2020 + '´'
		{"张三", "张三"},
		{"李四", "李四"},
		{"Roy", "Roy"},
		{"René", "René"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := RepairZKName(tc.in); got != tc.want {
			t.Errorf("RepairZKName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRepairMojibakeCacheOnNew(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMasterUser(models.MasterUser{PIN: "178", Name: "Ê¾ÀýÁ\u02c6", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMasterUser(models.MasterUser{PIN: "563", Name: "Ê¾Àý\u2020´", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSnapshot(models.DeviceUserSnapshot{
		DeviceID: "dev1",
		Users: []models.DeviceUser{
			{PIN: "178", Name: "Ê¾ÀýÁ\u02c6"},
			{PIN: "563", Name: "Ê¾Àý\u2020´"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.MergeAttendanceArchive([]models.AttendanceRecord{
		{DeviceID: "dev1", PIN: "178", Name: "Ê¾ÀýÁ\u02c6", Timestamp: "2026-11-24 08:30:00"},
	}); err != nil {
		t.Fatal(err)
	}

	// Re-open store (simulates app startup) -> should auto-repair all three files
	st2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	mu, _ := st2.LoadMasterUsers()
	if mu["178"].Name != "示例翀" || mu["563"].Name != "示例喆" {
		t.Fatalf("master_users not repaired: 178=%q 563=%q", mu["178"].Name, mu["563"].Name)
	}
	snaps := st2.LoadSnapshots()
	if snaps["dev1"].Users[0].Name != "示例翀" || snaps["dev1"].Users[1].Name != "示例喆" {
		t.Fatalf("snapshots not repaired: %+v", snaps["dev1"].Users)
	}
	recs, _, _ := st2.QueryAttendanceArchive("2026-11-01", "2026-11-30", nil)
	if len(recs) != 1 || recs[0].Name != "示例翀" {
		t.Fatalf("attendance_archive not repaired: %+v", recs)
	}
}
