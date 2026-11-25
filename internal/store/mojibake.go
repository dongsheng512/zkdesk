package store

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"zk_mac/internal/models"
)

// Windows-1252 printable mappings for bytes 0x80..0x9F.
var cp1252RuneToByte = map[rune]byte{
	0x20AC: 0x80, 0x201A: 0x82, 0x0192: 0x83, 0x201E: 0x84,
	0x2026: 0x85, 0x2020: 0x86, 0x2021: 0x87, 0x02C6: 0x88,
	0x2030: 0x89, 0x0160: 0x8A, 0x2039: 0x8B, 0x0152: 0x8C,
	0x017D: 0x8E, 0x2018: 0x91, 0x2019: 0x92, 0x201C: 0x93,
	0x201D: 0x94, 0x2022: 0x95, 0x2013: 0x96, 0x2014: 0x97,
	0x02DC: 0x98, 0x2122: 0x99, 0x0161: 0x9A, 0x203A: 0x9B,
	0x0153: 0x9C, 0x017E: 0x9E, 0x0178: 0x9F,
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

// RepairZKName recovers GBK/GB18030 Chinese names that were previously mis-decoded
// through Windows-1252 (e.g. "Ê¾ÀýÁˆ" -> "示例翀", "Ê¾Àý†´" -> "示例喆").
// Valid UTF-8 CJK, pure ASCII, and normal Western accented strings are returned unchanged.
func RepairZKName(s string) string {
	if s == "" {
		return s
	}
	raw := make([]byte, 0, len(s))
	hasHigh := false
	for _, r := range s {
		if r <= 0xFF {
			if r >= 0x80 {
				hasHigh = true
			}
			raw = append(raw, byte(r))
		} else if b, ok := cp1252RuneToByte[r]; ok {
			hasHigh = true
			raw = append(raw, b)
		} else {
			return s
		}
	}
	if !hasHigh {
		return s
	}

	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return s
	}
	decoded := string(out)
	if !utf8.ValidString(decoded) || strings.ContainsRune(decoded, '\uFFFD') {
		return s
	}
	// Only accept the conversion if it actually yields valid CJK characters (avoiding false positives on Latin accents like "René")
	hasCJK := false
	for _, r := range decoded {
		if isCJK(r) {
			hasCJK = true
			break
		}
	}
	if !hasCJK {
		return s
	}
	return decoded
}

// RepairMojibakeCache scans local persistent JSON stores and automatically heals any
// legacy Windows-1252 mojibake names while preserving rolling .bak safety copies.
func (s *Store) RepairMojibakeCache() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	repaired := 0

	// 1. master_users.json
	if mf, ok := s.loadMasterUsersLocked(); ok && len(mf.Users) > 0 {
		changed := false
		for pin, u := range mf.Users {
			if fixed := RepairZKName(u.Name); fixed != u.Name {
				u.Name = fixed
				mf.Users[pin] = u
				changed = true
				repaired++
			}
		}
		if changed {
			_ = writeJSONDurable(s.MasterUsersPath(), mf)
		}
	}

	// 2. cache/user_snapshots.json
	snaps := map[string]models.DeviceUserSnapshot{}
	if err := readJSON(s.snapshotsPath(), &snaps); err == nil && len(snaps) > 0 {
		changed := false
		for devID, snap := range snaps {
			for i := range snap.Users {
				if fixed := RepairZKName(snap.Users[i].Name); fixed != snap.Users[i].Name {
					snap.Users[i].Name = fixed
					changed = true
					repaired++
				}
			}
			snaps[devID] = snap
		}
		if changed {
			_ = writeJSONAtomic(s.snapshotsPath(), snaps)
		}
	}

	// 3. cache/attendance_archive.json
	var arch attendanceArchiveFile
	if err := readJSON(s.archiveLogsPath(), &arch); err == nil && len(arch.Records) > 0 {
		changed := false
		for i := range arch.Records {
			if fixed := RepairZKName(arch.Records[i].Name); fixed != arch.Records[i].Name {
				arch.Records[i].Name = fixed
				changed = true
				repaired++
			}
		}
		if changed {
			_ = writeJSONAtomic(s.archiveLogsPath(), arch)
		}
	}

	// 4. cache/last_logs.json
	var last models.PullLogsResponse
	if err := readJSON(s.logsPath(), &last); err == nil && (len(last.Records) > 0 || len(last.DailySummary) > 0) {
		changed := false
		for i := range last.Records {
			if fixed := RepairZKName(last.Records[i].Name); fixed != last.Records[i].Name {
				last.Records[i].Name = fixed
				changed = true
				repaired++
			}
		}
		for i := range last.DailySummary {
			if fixed := RepairZKName(last.DailySummary[i].Name); fixed != last.DailySummary[i].Name {
				last.DailySummary[i].Name = fixed
				changed = true
				repaired++
			}
		}
		if changed {
			_ = writeJSONAtomic(s.logsPath(), last)
		}
	}

	return repaired
}
