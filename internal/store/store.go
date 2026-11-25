package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"zk_mac/internal/models"
)

// ValidateSafeSegment rejects empty values, path separators, traversal sequences (".."),
// and control characters so IDs/PINs can never escape their designated storage directory.
func ValidateSafeSegment(label, val string) error {
	v := strings.TrimSpace(val)
	if v == "" {
		return fmt.Errorf("%s不能为空", label)
	}
	if len(v) > 64 {
		return fmt.Errorf("%s长度不能超过 64 个字符", label)
	}
	if v == "." || v == ".." || strings.Contains(v, "..") {
		return fmt.Errorf("%s包含非法路径字符", label)
	}
	for _, r := range v {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s包含非法字符", label)
		}
	}
	return nil
}

func ValidateDeviceID(id string) error { return ValidateSafeSegment("设备 ID", id) }
func ValidatePIN(pin string) error     { return ValidateSafeSegment("工号", pin) }

func sanitizeBackupLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "snapshot"
	}
	var b strings.Builder
	for _, r := range label {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "snapshot"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// Store manages local JSON persistence, face photo cache, and pre-write backups.
type Store struct {
	mu      sync.RWMutex
	dataDir string
}

func New(dataDir string) (*Store, error) {
	dirs := []string{
		dataDir,
		filepath.Join(dataDir, "faces"),
		filepath.Join(dataDir, "backups"),
		filepath.Join(dataDir, "cache"),
		filepath.Join(dataDir, "logs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		_ = os.Chmod(d, 0o700)
	}
	s := &Store{dataDir: dataDir}
	if _, err := os.Stat(s.devicesPath()); os.IsNotExist(err) {
		if err := s.SaveDevices(defaultDevices()); err != nil {
			return nil, err
		}
	}
	// Tighten permissions on pre-existing sensitive files from earlier versions.
	for _, p := range []string{
		s.devicesPath(),
		s.MasterUsersPath(),
		s.MasterUsersPath() + ".bak",
		s.snapshotsPath(),
		s.statusesPath(),
		s.archiveLogsPath(),
		s.opLogsPath(),
		s.profilesPath(),
	} {
		_ = os.Chmod(p, 0o600)
	}
	s.RepairMojibakeCache()
	return s, nil
}

func (s *Store) DataDir() string { return s.dataDir }
func (s *Store) FacesDir() string { return filepath.Join(s.dataDir, "faces") }
func (s *Store) BackupsDir() string { return filepath.Join(s.dataDir, "backups") }
func (s *Store) devicesPath() string { return filepath.Join(s.dataDir, "devices.json") }
func (s *Store) MasterUsersPath() string { return filepath.Join(s.dataDir, "master_users.json") }
func (s *Store) statusesPath() string { return filepath.Join(s.dataDir, "cache", "statuses.json") }
func (s *Store) snapshotsPath() string { return filepath.Join(s.dataDir, "cache", "user_snapshots.json") }
func (s *Store) logsPath() string { return filepath.Join(s.dataDir, "cache", "last_logs.json") }
func (s *Store) archiveLogsPath() string { return filepath.Join(s.dataDir, "cache", "attendance_archive.json") }
func (s *Store) opLogsPath() string { return filepath.Join(s.dataDir, "cache", "operation_logs.json") }
func (s *Store) profilesPath() string { return filepath.Join(s.dataDir, "cache", "user_profiles.json") }

func defaultDevices() []models.DeviceConfig {
	return []models.DeviceConfig{}
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_ = f.Chmod(0o600)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// writeJSONDurable keeps a rolling .bak copy of the previous valid file before fsync + atomic rename.
func writeJSONDurable(path string, v any) error {
	if prev, err := os.ReadFile(path); err == nil && len(prev) > 2 && json.Valid(prev) {
		_ = os.WriteFile(path+".bak", prev, 0o600)
	}
	return writeJSONAtomic(path, v)
}

func readJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func (s *Store) LoadDevices() ([]models.DeviceConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var list []models.DeviceConfig
	if err := readJSON(s.devicesPath(), &list); err != nil {
		if os.IsNotExist(err) {
			return defaultDevices(), nil
		}
		return nil, err
	}
	return list, nil
}

func (s *Store) SaveDevices(list []models.DeviceConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.devicesPath(), list)
}

func (s *Store) LoadStatuses() map[string]models.DeviceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := map[string]models.DeviceStatus{}
	_ = readJSON(s.statusesPath(), &m)
	return m
}

func (s *Store) SaveStatus(st models.DeviceStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]models.DeviceStatus{}
	_ = readJSON(s.statusesPath(), &m)
	m[st.DeviceID] = st
	return writeJSONAtomic(s.statusesPath(), m)
}

// ResetAllConnectedStatuses marks every cached device status as disconnected in a single atomic write.
func (s *Store) ResetAllConnectedStatuses() (map[string]models.DeviceStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]models.DeviceStatus{}
	_ = readJSON(s.statusesPath(), &m)
	changed := false
	for id, st := range m {
		if st.Connected || st.Error != "" {
			st.Connected = false
			st.Error = ""
			m[id] = st
			changed = true
		}
	}
	if changed {
		if err := writeJSONAtomic(s.statusesPath(), m); err != nil {
			return m, err
		}
	}
	return m, nil
}

func (s *Store) LoadSnapshots() map[string]models.DeviceUserSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := map[string]models.DeviceUserSnapshot{}
	_ = readJSON(s.snapshotsPath(), &m)
	return m
}

func (s *Store) SaveSnapshot(snap models.DeviceUserSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]models.DeviceUserSnapshot{}
	_ = readJSON(s.snapshotsPath(), &m)
	m[snap.DeviceID] = snap
	return writeJSONAtomic(s.snapshotsPath(), m)
}

// DeleteDeviceCache removes orphaned status and snapshot entries for a deleted device.
func (s *Store) DeleteDeviceCache(deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	statuses := map[string]models.DeviceStatus{}
	if err := readJSON(s.statusesPath(), &statuses); err == nil {
		if _, exists := statuses[deviceID]; exists {
			delete(statuses, deviceID)
			if err := writeJSONAtomic(s.statusesPath(), statuses); err != nil {
				return err
			}
		}
	}

	snaps := map[string]models.DeviceUserSnapshot{}
	if err := readJSON(s.snapshotsPath(), &snaps); err == nil {
		if _, exists := snaps[deviceID]; exists {
			delete(snaps, deviceID)
			if err := writeJSONAtomic(s.snapshotsPath(), snaps); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) SaveLastLogs(resp models.PullLogsResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.logsPath(), resp)
}

func (s *Store) LoadLastLogs() models.PullLogsResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var resp models.PullLogsResponse
	_ = readJSON(s.logsPath(), &resp)
	if resp.Records == nil {
		resp.Records = []models.AttendanceRecord{}
	}
	if resp.Summaries == nil {
		resp.Summaries = []models.PullLogsDeviceSummary{}
	}
	return resp
}

// DeviceFaceDir returns the cached face directory for a device (or "custom"/"master" for local uploads).
func (s *Store) DeviceFaceDir(deviceID string) string {
	if ValidateDeviceID(deviceID) != nil {
		deviceID = "custom"
	}
	dir := filepath.Join(s.FacesDir(), deviceID)
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func (s *Store) SaveFaceJPEG(deviceID, pin string, jpegBytes []byte) (string, error) {
	if err := ValidateDeviceID(deviceID); err != nil {
		return "", err
	}
	if err := ValidatePIN(pin); err != nil {
		return "", err
	}
	dir := s.DeviceFaceDir(deviceID)
	dest := filepath.Join(dir, fmt.Sprintf("%s.jpg", strings.TrimSpace(pin)))
	if err := os.WriteFile(dest, jpegBytes, 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

// FindLocalFace looks up a cached face JPEG for a PIN, preferring preferredDevID if specified.
func (s *Store) FindLocalFace(pin string, preferredDevIDs ...string) (path string, sourceDev string, size int64) {
	if ValidatePIN(pin) != nil {
		return "", "", 0
	}
	pin = strings.TrimSpace(pin)
	checked := map[string]bool{}
	tryDev := func(devID string) bool {
		if devID == "" || checked[devID] || ValidateDeviceID(devID) != nil {
			return false
		}
		checked[devID] = true
		p := filepath.Join(s.FacesDir(), devID, fmt.Sprintf("%s.jpg", pin))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			path = p
			sourceDev = devID
			size = fi.Size()
			return true
		}
		return false
	}

	if tryDev("master") {
		return
	}
	for _, id := range preferredDevIDs {
		if tryDev(id) {
			return
		}
	}
	if tryDev("custom") {
		return
	}
	entries, err := os.ReadDir(s.FacesDir())
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if tryDev(name) {
			return
		}
	}
	return
}

func ReadJPEGDataURL(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// WriteBackup writes a timestamped JSON snapshot before destructive/sync operations.
func (s *Store) WriteBackup(label string, payload any) (string, error) {
	stamp := time.Now().Format("20060102_150405")
	name := fmt.Sprintf("%s_%s.json", stamp, sanitizeBackupLabel(label))
	dest := filepath.Join(s.BackupsDir(), name)
	if err := writeJSONAtomic(dest, payload); err != nil {
		return "", err
	}
	return dest, nil
}

// ==================== Persistent Attendance Archive ====================

type attendanceArchiveFile struct {
	UpdatedAt string                    `json:"updatedAt"`
	Records   []models.AttendanceRecord `json:"records"`
}

func attendanceKey(r models.AttendanceRecord) string {
	return r.DeviceID + "|" + r.PIN + "|" + r.Timestamp
}

// MergeAttendanceArchive deduplicates and appends new records into the persistent archive.
// Returns (addedNewCount, totalArchiveCount, error).
func (s *Store) MergeAttendanceArchive(incoming []models.AttendanceRecord) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var cur attendanceArchiveFile
	_ = readJSON(s.archiveLogsPath(), &cur)

	byKey := make(map[string]models.AttendanceRecord, len(cur.Records)+len(incoming))
	for _, r := range cur.Records {
		byKey[attendanceKey(r)] = r
	}
	added := 0
	for _, r := range incoming {
		r.Name = RepairZKName(r.Name)
		k := attendanceKey(r)
		if old, exists := byKey[k]; !exists {
			byKey[k] = r
			added++
		} else if (old.Name == "" || RepairZKName(old.Name) != old.Name) && r.Name != "" {
			byKey[k] = r
		}
	}

	merged := make([]models.AttendanceRecord, 0, len(byKey))
	for _, r := range byKey {
		merged = append(merged, r)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Timestamp != merged[j].Timestamp {
			return merged[i].Timestamp > merged[j].Timestamp
		}
		if merged[i].PIN != merged[j].PIN {
			return merged[i].PIN < merged[j].PIN
		}
		return merged[i].DeviceID < merged[j].DeviceID
	})

	cur.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	cur.Records = merged
	if err := writeJSONAtomic(s.archiveLogsPath(), cur); err != nil {
		return 0, 0, err
	}
	return added, len(merged), nil
}

// QueryAttendanceArchive filters archived records by optional date range and device IDs.
func (s *Store) QueryAttendanceArchive(startDate, endDate string, deviceIDs []string) ([]models.AttendanceRecord, int, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var cur attendanceArchiveFile
	_ = readJSON(s.archiveLogsPath(), &cur)

	startTS := startDate
	if len(startTS) == 10 {
		startTS += " 00:00:00"
	}
	endTS := endDate
	if len(endTS) == 10 {
		endTS += " 23:59:59"
	}
	devFilter := map[string]bool{}
	for _, id := range deviceIDs {
		if id != "" {
			devFilter[id] = true
		}
	}

	out := make([]models.AttendanceRecord, 0)
	for _, r := range cur.Records {
		if len(devFilter) > 0 && !devFilter[r.DeviceID] {
			continue
		}
		if startTS != "" && r.Timestamp < startTS {
			continue
		}
		if endTS != "" && r.Timestamp > endTS {
			continue
		}
		out = append(out, r)
	}
	return out, len(cur.Records), cur.UpdatedAt
}

// GetAttendanceArchiveOverview aggregates per-device record counts and earliest/latest date coverage.
func (s *Store) GetAttendanceArchiveOverview() models.ArchiveOverviewResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var cur attendanceArchiveFile
	_ = readJSON(s.archiveLogsPath(), &cur)

	resp := models.ArchiveOverviewResponse{
		TotalRecords: len(cur.Records),
		UpdatedAt:    cur.UpdatedAt,
		Devices:      []models.ArchiveDeviceStat{},
	}
	if len(cur.Records) == 0 {
		return resp
	}

	byDev := map[string]*models.ArchiveDeviceStat{}
	for _, r := range cur.Records {
		day := ""
		if len(r.Timestamp) >= 10 {
			day = r.Timestamp[:10]
		}
		if day != "" {
			if resp.EarliestDate == "" || day < resp.EarliestDate {
				resp.EarliestDate = day
			}
			if resp.LatestDate == "" || day > resp.LatestDate {
				resp.LatestDate = day
			}
		}
		st, ok := byDev[r.DeviceID]
		if !ok {
			st = &models.ArchiveDeviceStat{
				DeviceID:   r.DeviceID,
				DeviceName: r.DeviceName,
				SensorID:   r.SensorID,
			}
			byDev[r.DeviceID] = st
		}
		if st.DeviceName == "" && r.DeviceName != "" {
			st.DeviceName = r.DeviceName
		}
		if st.SensorID == "" && r.SensorID != "" {
			st.SensorID = r.SensorID
		}
		st.Count++
		if day != "" {
			if st.EarliestDate == "" || day < st.EarliestDate {
				st.EarliestDate = day
			}
			if st.LatestDate == "" || day > st.LatestDate {
				st.LatestDate = day
			}
		}
	}

	out := make([]models.ArchiveDeviceStat, 0, len(byDev))
	for _, st := range byDev {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DeviceID < out[j].DeviceID
	})
	resp.Devices = out
	return resp
}

func matchPruneRecord(r models.AttendanceRecord, cutoffTS, deviceID string) bool {
	if deviceID != "" && r.DeviceID != deviceID {
		return false
	}
	return r.Timestamp != "" && r.Timestamp < cutoffTS
}

// PreviewPruneAttendanceArchive computes how many local archived records fall strictly before beforeDate (YYYY-MM-DD).
func (s *Store) PreviewPruneAttendanceArchive(beforeDate, deviceID string) (models.PruneArchivePreview, error) {
	if len(beforeDate) != 10 {
		return models.PruneArchivePreview{}, fmt.Errorf("请选择有效的截止日期 (YYYY-MM-DD)")
	}
	cutoffTS := beforeDate + " 00:00:00"

	s.mu.RLock()
	defer s.mu.RUnlock()

	var cur attendanceArchiveFile
	_ = readJSON(s.archiveLogsPath(), &cur)

	prev := models.PruneArchivePreview{
		BeforeDate:  beforeDate,
		DeviceID:    deviceID,
		TotalBefore: len(cur.Records),
	}
	for _, r := range cur.Records {
		if matchPruneRecord(r, cutoffTS, deviceID) {
			prev.MatchCount++
			if len(r.Timestamp) >= 10 {
				day := r.Timestamp[:10]
				if prev.EarliestDate == "" || day < prev.EarliestDate {
					prev.EarliestDate = day
				}
				if prev.LatestDate == "" || day > prev.LatestDate {
					prev.LatestDate = day
				}
			}
		}
	}
	prev.RemainCount = prev.TotalBefore - prev.MatchCount
	return prev, nil
}

// PruneAttendanceArchive removes records strictly before beforeDate (YYYY-MM-DD) from the local archive ONLY.
// Automatically backs up removed records to backups/ before rewriting local cache files.
func (s *Store) PruneAttendanceArchive(beforeDate, deviceID string) (models.PruneArchiveResponse, error) {
	if len(beforeDate) != 10 {
		return models.PruneArchiveResponse{}, fmt.Errorf("请选择有效的截止日期 (YYYY-MM-DD)")
	}
	cutoffTS := beforeDate + " 00:00:00"

	s.mu.Lock()
	defer s.mu.Unlock()

	var cur attendanceArchiveFile
	_ = readJSON(s.archiveLogsPath(), &cur)

	removed := make([]models.AttendanceRecord, 0)
	kept := make([]models.AttendanceRecord, 0, len(cur.Records))
	for _, r := range cur.Records {
		if matchPruneRecord(r, cutoffTS, deviceID) {
			removed = append(removed, r)
		} else {
			kept = append(kept, r)
		}
	}

	resp := models.PruneArchiveResponse{
		BeforeDate:  beforeDate,
		DeviceID:    deviceID,
		Removed:     len(removed),
		RemainCount: len(kept),
	}
	if len(removed) == 0 {
		return resp, nil
	}

	// Mandatory safety backup of pruned records before modifying archive
	label := fmt.Sprintf("prune_archive_before_%s", beforeDate)
	if deviceID != "" {
		label += "_" + deviceID
	}
	bakDest := filepath.Join(s.BackupsDir(), fmt.Sprintf("%s_%s.json", time.Now().Format("20060102_150405"), label))
	if err := writeJSONAtomic(bakDest, map[string]any{
		"beforeDate": beforeDate,
		"deviceId":   deviceID,
		"removed":    len(removed),
		"records":    removed,
	}); err != nil {
		return resp, fmt.Errorf("清理前自动备份失败，已中止清理: %w", err)
	}
	resp.BackupPath = bakDest

	cur.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	cur.Records = kept
	if err := writeJSONAtomic(s.archiveLogsPath(), cur); err != nil {
		return resp, fmt.Errorf("写入清理后的本地累计库失败: %w", err)
	}

	// Keep cached last_logs.json consistent so pruned records don't reappear on reload
	var last models.PullLogsResponse
	if err := readJSON(s.logsPath(), &last); err == nil && len(last.Records) > 0 {
		filtered := make([]models.AttendanceRecord, 0, len(last.Records))
		for _, r := range last.Records {
			if !matchPruneRecord(r, cutoffTS, deviceID) {
				filtered = append(filtered, r)
			}
		}
		last.Records = filtered
		last.Total = len(filtered)
		last.ArchiveTotal = len(kept)
		_ = writeJSONAtomic(s.logsPath(), last)
	}

	return resp, nil
}

// ==================== Persistent Operation Audit Logs ====================

const maxOperationLogs = 500

func (s *Store) AppendOperationLog(entry models.OperationLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var logs []models.OperationLog
	_ = readJSON(s.opLogsPath(), &logs)

	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().Format("2006-01-02 15:04:05")
	}

	// Prepend newest first
	next := make([]models.OperationLog, 0, len(logs)+1)
	next = append(next, entry)
	next = append(next, logs...)
	if len(next) > maxOperationLogs {
		next = next[:maxOperationLogs]
	}
	return writeJSONAtomic(s.opLogsPath(), next)
}

func (s *Store) LoadOperationLogs() []models.OperationLog {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var logs []models.OperationLog
	_ = readJSON(s.opLogsPath(), &logs)
	if logs == nil {
		return []models.OperationLog{}
	}
	return logs
}

func (s *Store) ClearOperationLogs() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.opLogsPath(), []models.OperationLog{})
}

// ==================== Local HR User Profiles (Department & Status) ====================

func (s *Store) LoadProfiles() map[string]models.UserProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()

	m := map[string]models.UserProfile{}
	_ = readJSON(s.profilesPath(), &m)
	return m
}

func (s *Store) SaveProfile(p models.UserProfile) error {
	if p.PIN == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	m := map[string]models.UserProfile{}
	_ = readJSON(s.profilesPath(), &m)
	m[p.PIN] = p
	return writeJSONAtomic(s.profilesPath(), m)
}

func (s *Store) MergeProfiles(items []models.UserProfile) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := map[string]models.UserProfile{}
	_ = readJSON(s.profilesPath(), &m)
	count := 0
	for _, it := range items {
		if it.PIN == "" {
			continue
		}
		cur := m[it.PIN]
		cur.PIN = it.PIN
		if it.Department != "" {
			cur.Department = it.Department
		}
		if it.WorkStatus != "" {
			cur.WorkStatus = it.WorkStatus
		}
		if it.Note != "" {
			cur.Note = it.Note
		}
		m[it.PIN] = cur
		count++
	}
	if err := writeJSONAtomic(s.profilesPath(), m); err != nil {
		return count, err
	}
	// Propagate imported HR metadata into existing master_users.json records
	if mf, ok := s.loadMasterUsersLocked(); ok && len(mf.Users) > 0 {
		changed := false
		now := time.Now().Format("2006-01-02 15:04:05")
		for _, it := range items {
			if mu, exists := mf.Users[it.PIN]; exists {
				if it.Department != "" {
					mu.Department = it.Department
				}
				if it.WorkStatus != "" {
					mu.WorkStatus = it.WorkStatus
				}
				if it.Note != "" {
					mu.Note = it.Note
				}
				mu.UpdatedAt = now
				mf.Users[it.PIN] = mu
				changed = true
			}
		}
		if changed {
			mf.UpdatedAt = now
			_ = writeJSONDurable(s.MasterUsersPath(), mf)
		}
	}
	return count, nil
}

// ==================== Authoritative Local Master Users (Durable Persistence) ====================

type masterUsersFile struct {
	Version   int                          `json:"version"`
	UpdatedAt string                       `json:"updatedAt"`
	Users     map[string]models.MasterUser `json:"users"`
}

func (s *Store) loadMasterUsersLocked() (masterUsersFile, bool) {
	var f masterUsersFile
	if err := readJSON(s.MasterUsersPath(), &f); err == nil && f.Users != nil {
		return f, true
	}
	// Automatic fallback to rolling .bak if primary file is missing or corrupted
	if err := readJSON(s.MasterUsersPath()+".bak", &f); err == nil && f.Users != nil {
		return f, true
	}
	return masterUsersFile{
		Version: 1,
		Users:   map[string]models.MasterUser{},
	}, false
}

// LoadMasterUsers returns the authoritative local master user map and its last update timestamp.
func (s *Store) LoadMasterUsers() (map[string]models.MasterUser, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	f, _ := s.loadMasterUsersLocked()
	out := make(map[string]models.MasterUser, len(f.Users))
	for k, v := range f.Users {
		out[k] = v
	}
	return out, f.UpdatedAt
}

// SaveMasterUser atomically upserts a single user in master_users.json (fsync + rolling .bak).
func (s *Store) SaveMasterUser(u models.MasterUser) error {
	if u.PIN == "" {
		return fmt.Errorf("工号不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, _ := s.loadMasterUsersLocked()
	now := time.Now().Format("2006-01-02 15:04:05")
	u.UpdatedAt = now
	if u.WorkStatus == "" {
		u.WorkStatus = "active"
	}
	f.Version = 1
	f.UpdatedAt = now
	f.Users[u.PIN] = u
	if err := writeJSONDurable(s.MasterUsersPath(), f); err != nil {
		return err
	}
	// Keep legacy user_profiles.json synchronized for department/status compatibility
	if u.Department != "" || u.WorkStatus != "" || u.Note != "" {
		pm := map[string]models.UserProfile{}
		_ = readJSON(s.profilesPath(), &pm)
		pm[u.PIN] = models.UserProfile{
			PIN:        u.PIN,
			Department: u.Department,
			WorkStatus: u.WorkStatus,
			Note:       u.Note,
		}
		_ = writeJSONAtomic(s.profilesPath(), pm)
	}
	return nil
}

// SaveMasterUsersMap atomically persists the complete master user map (fsync + rolling .bak).
func (s *Store) SaveMasterUsersMap(users map[string]models.MasterUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Format("2006-01-02 15:04:05")
	clean := make(map[string]models.MasterUser, len(users))
	for pin, u := range users {
		if pin == "" {
			continue
		}
		u.PIN = pin
		if u.WorkStatus == "" {
			u.WorkStatus = "active"
		}
		if u.UpdatedAt == "" {
			u.UpdatedAt = now
		}
		clean[pin] = u
	}
	f := masterUsersFile{
		Version:   1,
		UpdatedAt: now,
		Users:     clean,
	}
	return writeJSONDurable(s.MasterUsersPath(), f)
}

// DeleteMasterUser removes a user from master_users.json after saving a timestamped backup.
func (s *Store) DeleteMasterUser(pin string) (string, error) {
	if pin == "" {
		return "", fmt.Errorf("工号不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, _ := s.loadMasterUsersLocked()
	old, exists := f.Users[pin]
	if !exists {
		return "", nil
	}
	backupPath := filepath.Join(s.BackupsDir(), fmt.Sprintf("%s_delete_master_%s.json", time.Now().Format("20060102_150405"), pin))
	_ = writeJSONAtomic(backupPath, old)

	delete(f.Users, pin)
	f.Version = 1
	f.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	if err := writeJSONDurable(s.MasterUsersPath(), f); err != nil {
		return backupPath, err
	}
	return backupPath, nil
}

// ExportMasterUsersBackup exports master_users.json cleanly to a user-selected path.
func (s *Store) ExportMasterUsersBackup(destPath string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	f, _ := s.loadMasterUsersLocked()
	if err := writeJSONAtomic(destPath, f); err != nil {
		return 0, err
	}
	return len(f.Users), nil
}

// ImportMasterUsersBackup restores/replaces master_users.json from a backup file,
// creating a pre-restore safety snapshot first.
func (s *Store) ImportMasterUsersBackup(srcPath string) (int, string, error) {
	var incoming masterUsersFile
	if err := readJSON(srcPath, &incoming); err != nil {
		return 0, "", fmt.Errorf("无法读取人员备份文件: %w", err)
	}
	if len(incoming.Users) == 0 {
		return 0, "", fmt.Errorf("备份文件中没有人员数据")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cur, _ := s.loadMasterUsersLocked()
	preBackup := ""
	if len(cur.Users) > 0 {
		preBackup = filepath.Join(s.BackupsDir(), fmt.Sprintf("%s_pre_restore_master.json", time.Now().Format("20060102_150405")))
		_ = writeJSONAtomic(preBackup, cur)
	}

	incoming.Version = 1
	incoming.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	if err := writeJSONDurable(s.MasterUsersPath(), incoming); err != nil {
		return 0, preBackup, err
	}
	return len(incoming.Users), preBackup, nil
}
