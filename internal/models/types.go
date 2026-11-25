package models

// DeviceConfig represents a configured ZKTeco attendance terminal.
type DeviceConfig struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	IP            string `json:"ip"`
	Port          int    `json:"port"`
	Password      int    `json:"password"`
	MachineNumber int    `json:"machineNumber"`
	SensorID      string `json:"sensorId"`
	Note          string `json:"note"`
	Enabled       bool   `json:"enabled"`
}

// DeviceStatus holds live probe & connection state for a device.
type DeviceStatus struct {
	DeviceID     string `json:"deviceId"`
	Online       bool   `json:"online"`
	Connected    bool   `json:"connected"`
	Error        string `json:"error,omitempty"`
	SN           string `json:"sn"`
	Firmware     string `json:"firmware"`
	Platform     string `json:"platform"`
	DeviceTime   string `json:"deviceTime"`
	TimeDriftSec int    `json:"timeDriftSec"` // deviceTime - localTime in seconds
	HasTimeDrift bool   `json:"hasTimeDrift"`
	FaceVersion  string `json:"faceVersion"`
	BioVersion   string `json:"bioVersion"`
	UserCount    int    `json:"userCount"`
	FPCount      int    `json:"fpCount"`
	FaceCount    int    `json:"faceCount"`
	LogCount     int    `json:"logCount"`
	LogMode      string `json:"logMode"`
	CheckedAt    string `json:"checkedAt"`
}

// DeviceUser represents one user record on a specific device.
type DeviceUser struct {
	PIN       string `json:"pin"`
	Name      string `json:"name"`
	Password  string `json:"password"`
	Privilege int    `json:"privilege"`
	Enabled   bool   `json:"enabled"`
	Card      string `json:"card"`
	HasFace   bool   `json:"hasFace"`
	FPIndices []int  `json:"fpIndices"`
	FPCount   int    `json:"fpCount"`
}

// DeviceUserSnapshot stores cached users for one device.
type DeviceUserSnapshot struct {
	DeviceID      string       `json:"deviceId"`
	UpdatedAt     string       `json:"updatedAt"`
	Users         []DeviceUser `json:"users"`
	FacePhotoPINs []string     `json:"facePhotoPins"`
}

// FingerprintTemplate stores one ZKTeco 10.0 fingerprint template in the local master DB.
type FingerprintTemplate struct {
	FingerID int    `json:"fingerId"`
	Flag     int    `json:"flag"`
	Template string `json:"template"`
}

// MasterUser is the authoritative local staff record stored in master_users.json.
type MasterUser struct {
	PIN          string                `json:"pin"`
	Name         string                `json:"name"`
	Department   string                `json:"department"`
	WorkStatus   string                `json:"workStatus"` // "active" | "resigned"
	Privilege    int                   `json:"privilege"`
	Enabled      bool                  `json:"enabled"`
	Card         string                `json:"card"`
	Password     string                `json:"password"`
	Note         string                `json:"note"`
	Fingerprints []FingerprintTemplate `json:"fingerprints,omitempty"`
	UpdatedAt    string                `json:"updatedAt"`
}

// MatrixCell is a user's state on one specific device compared against the local master DB.
type MatrixCell struct {
	Present     bool     `json:"present"`
	Name        string   `json:"name"`
	Privilege   int      `json:"privilege"`
	Enabled     bool     `json:"enabled"`
	Card        string   `json:"card"`
	HasFace     bool     `json:"hasFace"`
	FPCount     int      `json:"fpCount"`
	SyncStatus  string   `json:"syncStatus"` // "synced" | "not_assigned" | "need_push" | "need_pull" | "both" | "resigned_present" | "no_snapshot"
	DiffDetails []string `json:"diffDetails,omitempty"`
}

// MergedUserRow aggregates one employee's local master record alongside per-device states.
type MergedUserRow struct {
	PIN               string                `json:"pin"`
	InMaster          bool                  `json:"inMaster"`
	PrimaryName       string                `json:"primaryName"`
	Department        string                `json:"department"`
	WorkStatus        string                `json:"workStatus"` // "" or "active" | "resigned"
	ProfileNote       string                `json:"profileNote"`
	Privilege         int                   `json:"privilege"`
	Enabled           bool                  `json:"enabled"`
	Card              string                `json:"card"`
	MasterPasswordSet bool                  `json:"masterPasswordSet"`
	MasterFPCount     int                   `json:"masterFpCount"`
	MasterFPIndices   []int                 `json:"masterFpIndices,omitempty"`
	MasterUpdatedAt   string                `json:"masterUpdatedAt"`
	PresentCount      int                   `json:"presentCount"`
	TotalDevices      int                   `json:"totalDevices"`
	HasDiff           bool                  `json:"hasDiff"`
	HasPendingPush    bool                  `json:"hasPendingPush"`
	HasPendingPull    bool                  `json:"hasPendingPull"`
	HasResignedInDev  bool                  `json:"hasResignedInDev"`
	DiffReasons       []string              `json:"diffReasons"`
	HasLocalFace      bool                  `json:"hasLocalFace"`
	LocalFaceDevID    string                `json:"localFaceDeviceId"`
	Devices           map[string]MatrixCell `json:"devices"`
}

// UserProfile stores local HR metadata not stored on ZKTeco terminals (legacy & CSV import struct).
type UserProfile struct {
	PIN        string `json:"pin"`
	Department string `json:"department"`
	WorkStatus string `json:"workStatus"` // "active" | "resigned"
	Note       string `json:"note"`
}

// UserMatrixResponse is returned to the UI for the unified staff table.
type UserMatrixResponse struct {
	Devices          []DeviceConfig  `json:"devices"`
	Rows             []MergedUserRow `json:"rows"`
	Departments      []string        `json:"departments"`
	TotalUsers       int             `json:"totalUsers"`
	MasterCount      int             `json:"masterCount"`
	DiffUsers        int             `json:"diffUsers"`
	PendingPushCount int             `json:"pendingPushCount"`
	PendingPullCount int             `json:"pendingPullCount"`
	ResignedInDev    int             `json:"resignedInDev"`
	LastUpdated      string          `json:"lastUpdated"`
	MasterUpdatedAt  string          `json:"masterUpdatedAt"`
	MasterFilePath   string          `json:"masterFilePath"`
}

// SaveMasterUserRequest creates or updates a user in the local master DB without touching hardware.
type SaveMasterUserRequest struct {
	PIN                 string `json:"pin"`
	Name                string `json:"name"`
	Password            string `json:"password"`
	Privilege           int    `json:"privilege"`
	Enabled             bool   `json:"enabled"`
	Card                string `json:"card"`
	Department          string `json:"department"`
	WorkStatus          string `json:"workStatus"`
	Note                string `json:"note"`
	PreserveCardIfEmpty bool   `json:"preserveCardIfEmpty"`
	PreservePwdIfEmpty  bool   `json:"preservePwdIfEmpty"`
	LocalFaceFilePath   string `json:"localFaceFilePath"`
}

// PushMasterToDevicesRequest pushes selected local master users to selected devices.
type PushMasterToDevicesRequest struct {
	PINs                []string `json:"pins"`
	DeviceIDs           []string `json:"deviceIds"`
	SyncInfo            bool     `json:"syncInfo"`
	SyncFP              bool     `json:"syncFp"`
	SyncFace            bool     `json:"syncFace"`
	PreserveCardIfEmpty bool     `json:"preserveCardIfEmpty"`
	PreservePwdIfEmpty  bool     `json:"preservePwdIfEmpty"`
}

// PullDevicesToMasterRequest absorbs users/fingerprints/faces from devices into local master DB (read-only on hardware).
type PullDevicesToMasterRequest struct {
	DeviceIDs             []string `json:"deviceIds"`
	PINs                  []string `json:"pins"`
	IncludeInfo           bool     `json:"includeInfo"`
	IncludeFP             bool     `json:"includeFp"`
	IncludeFace           bool     `json:"includeFace"`
	OverwriteExistingInfo bool     `json:"overwriteExistingInfo"`
}

// PullToMasterResponse summarizes a device -> local master absorption run.
type PullToMasterResponse struct {
	DeviceIDs      []string `json:"deviceIds"`
	TotalProcessed int      `json:"totalProcessed"`
	Created        int      `json:"created"`
	Updated        int      `json:"updated"`
	FPsAbsorbed    int      `json:"fpsAbsorbed"`
	FacesAbsorbed  int      `json:"facesAbsorbed"`
	BackupPath     string   `json:"backupPath"`
	Errors         []string `json:"errors,omitempty"`
}

// MasterBackupResponse reports export/import of master_users.json.
type MasterBackupResponse struct {
	FilePath   string `json:"filePath"`
	UserCount  int    `json:"userCount"`
	BackupPath string `json:"backupPath,omitempty"`
}

// UpsertUserRequest modifies or creates a user on one or more devices.
type UpsertUserRequest struct {
	PIN                    string   `json:"pin"`
	Name                   string   `json:"name"`
	Password               string   `json:"password"`
	Privilege              int      `json:"privilege"`
	Enabled                bool     `json:"enabled"`
	Card                   string   `json:"card"`
	Department             string   `json:"department"`
	WorkStatus             string   `json:"workStatus"`
	ProfileNote            string   `json:"profileNote"`
	PreserveCardIfEmpty    bool     `json:"preserveCardIfEmpty"`
	PreservePwdIfEmpty     bool     `json:"preservePwdIfEmpty"`
	DeviceIDs              []string `json:"deviceIds"`
}

// ClearBiometricRequest clears only fingerprints, password, or face while keeping the user.
type ClearBiometricRequest struct {
	PIN       string   `json:"pin"`
	Target    string   `json:"target"` // "fingerprints" | "password" | "face"
	DeviceIDs []string `json:"deviceIds"`
}

// BatchFaceCandidate represents one photo file found during folder scan.
type BatchFaceCandidate struct {
	PIN          string `json:"pin"`
	Name         string `json:"name"`
	FileName     string `json:"fileName"`
	FilePath     string `json:"filePath"`
	SizeBytes    int64  `json:"sizeBytes"`
	ExistsInDevs bool   `json:"existsInDevs"`
}

// BatchFaceUploadItem reports per-photo upload result.
type BatchFaceUploadItem struct {
	PIN        string   `json:"pin"`
	FileName   string   `json:"fileName"`
	SuccessCnt int      `json:"successCnt"`
	FailCnt    int      `json:"failCnt"`
	Errors     []string `json:"errors,omitempty"`
}

// BatchFaceUploadResponse summarizes folder batch face upload.
type BatchFaceUploadResponse struct {
	TotalFiles int                   `json:"totalFiles"`
	SuccessAll int                   `json:"successAll"`
	Items      []BatchFaceUploadItem `json:"items"`
}

// ImportProfilesResponse reports HR department/status import stats.
type ImportProfilesResponse struct {
	FilePath string `json:"filePath"`
	Imported int    `json:"imported"`
	Skipped  int    `json:"skipped"`
}

// SyncUsersRequest copies selected users (info + fingerprints + face) from source to targets.
type SyncUsersRequest struct {
	SourceDeviceID  string   `json:"sourceDeviceId"`
	TargetDeviceIDs []string `json:"targetDeviceIds"`
	PINs            []string `json:"pins"`
	SyncInfo        bool     `json:"syncInfo"`
	SyncFP          bool     `json:"syncFp"`
	SyncFace        bool     `json:"syncFace"`
}

// SyncUserItemResult reports per-user sync outcome on a single target device.
type SyncUserItemResult struct {
	PIN      string   `json:"pin"`
	InfoOK   bool     `json:"infoOk"`
	FPSynced int      `json:"fpSynced"`
	FaceOK   bool     `json:"faceOk"`
	Errors   []string `json:"errors,omitempty"`
}

// SyncDeviceResult groups sync results for one target device.
type SyncDeviceResult struct {
	DeviceID   string               `json:"deviceId"`
	DeviceName string               `json:"deviceName"`
	Success    bool                 `json:"success"`
	Error      string               `json:"error,omitempty"`
	Items      []SyncUserItemResult `json:"items"`
}

// SyncUsersResponse is the full outcome of a cross-device sync job.
type SyncUsersResponse struct {
	SourceDeviceID string             `json:"sourceDeviceId"`
	RequestedPINs  int                `json:"requestedPins"`
	ReadBundles    int                `json:"readBundles"`
	BackupPath     string             `json:"backupPath"`
	Results        []SyncDeviceResult `json:"results"`
}

// DeleteUsersRequest deletes users from chosen devices (with automatic backup).
type DeleteUsersRequest struct {
	PINs      []string `json:"pins"`
	DeviceIDs []string `json:"deviceIds"`
}

// ActionDeviceResult is a generic per-device result for write actions.
type ActionDeviceResult struct {
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	OK         bool   `json:"ok"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

// FacePhotoItem represents a face photo entry in the gallery.
type FacePhotoItem struct {
	PIN        string   `json:"pin"`
	Name       string   `json:"name"`
	Devices    []string `json:"devices"`
	LocalPath  string   `json:"localPath"`
	DataURL    string   `json:"dataUrl"`
	SizeBytes  int64    `json:"sizeBytes"`
	CachedFrom string   `json:"cachedFrom"`
}

// DownloadFacesResponse summarizes a batch face download from one device.
type DownloadFacesResponse struct {
	DeviceID   string   `json:"deviceId"`
	Downloaded int      `json:"downloaded"`
	Failed     int      `json:"failed"`
	FailedPINs []string `json:"failedPins,omitempty"`
	SaveDir    string   `json:"saveDir"`
}

// AttendanceRecord represents one normalized punch record.
type AttendanceRecord struct {
	DeviceID    string `json:"deviceId"`
	DeviceName  string `json:"deviceName"`
	SensorID    string `json:"sensorId"`
	PIN         string `json:"pin"`
	Name        string `json:"name"`
	Department  string `json:"department"`
	Timestamp   string `json:"timestamp"`
	VerifyMode  int    `json:"verifyMode"`
	VerifyLabel string `json:"verifyLabel"`
	InOutMode   int    `json:"inOutMode"`
	InOutLabel  string `json:"inOutLabel"`
	WorkCode    int    `json:"workCode"`
}

// PullLogsRequest specifies which devices and date range to pull.
type PullLogsRequest struct {
	DeviceIDs []string `json:"deviceIds"`
	StartDate string   `json:"startDate"` // YYYY-MM-DD
	EndDate   string   `json:"endDate"`   // YYYY-MM-DD
}

// PullLogsDeviceSummary reports per-device pull stats.
type PullLogsDeviceSummary struct {
	DeviceID       string `json:"deviceId"`
	DeviceName     string `json:"deviceName"`
	Count          int    `json:"count"`
	ModeUsed       string `json:"modeUsed"`
	InvalidDropped int    `json:"invalidDropped"`
	Error          string `json:"error,omitempty"`
}

// PullLogsResponse returns merged deduplicated logs and per-device stats.
type PullLogsResponse struct {
	Records       []AttendanceRecord      `json:"records"`
	DailySummary  []DailySummaryRow       `json:"dailySummary"`
	Total         int                     `json:"total"`
	AddedNew      int                     `json:"addedNew"`
	ArchiveTotal  int                     `json:"archiveTotal"`
	Summaries     []PullLogsDeviceSummary `json:"summaries"`
	PulledAt      string                  `json:"pulledAt"`
}

// DailySummaryRow aggregates one employee's punches on a single calendar day.
type DailySummaryRow struct {
	Date         string   `json:"date"`
	PIN          string   `json:"pin"`
	Name         string   `json:"name"`
	Department   string   `json:"department"`
	FirstTime    string   `json:"firstTime"`
	LastTime     string   `json:"lastTime"`
	FirstDevice  string   `json:"firstDevice"`
	LastDevice   string   `json:"lastDevice"`
	PunchCount   int      `json:"punchCount"`
	WorkDuration string   `json:"workDuration"`
	Abnormal     bool     `json:"abnormal"`
	AbnormalNote string   `json:"abnormalNote"`
	AllTimes     []string `json:"allTimes"`
}

// ArchiveDeviceStat summarizes local archived attendance coverage for one device or imported sensor.
type ArchiveDeviceStat struct {
	DeviceID     string `json:"deviceId"`
	DeviceName   string `json:"deviceName"`
	SensorID     string `json:"sensorId"`
	Count        int    `json:"count"`
	EarliestDate string `json:"earliestDate"`
	LatestDate   string `json:"latestDate"`
	Configured   bool   `json:"configured"`
}

// ArchiveOverviewResponse summarizes the entire local cumulative attendance archive.
type ArchiveOverviewResponse struct {
	TotalRecords int                 `json:"totalRecords"`
	UpdatedAt    string              `json:"updatedAt"`
	EarliestDate string              `json:"earliestDate"`
	LatestDate   string              `json:"latestDate"`
	Devices      []ArchiveDeviceStat `json:"devices"`
}

// ExportLogsRequest specifies format, records/summary payload, and filename hint for exporting queried attendance data.
type ExportLogsRequest struct {
	Format       string             `json:"format"` // "xlsx" | "abt" | "csv" | "summary_csv" | "txt"
	Records      []AttendanceRecord `json:"records"`
	DailySummary []DailySummaryRow  `json:"dailySummary,omitempty"`
	FilePrefix   string             `json:"filePrefix,omitempty"`
	ScopeNote    string             `json:"scopeNote,omitempty"`
}

// PruneArchiveRequest specifies criteria for cleaning old records strictly from the local cumulative archive.
type PruneArchiveRequest struct {
	BeforeDate string `json:"beforeDate"`         // YYYY-MM-DD, deletes records strictly before YYYY-MM-DD 00:00:00
	DeviceID   string `json:"deviceId,omitempty"` // optional: empty means all devices & imported sensors
}

// PruneArchivePreview reports how many local records would be removed vs kept before executing cleanup.
type PruneArchivePreview struct {
	BeforeDate   string `json:"beforeDate"`
	DeviceID     string `json:"deviceId"`
	TotalBefore  int    `json:"totalBefore"`
	MatchCount   int    `json:"matchCount"`
	RemainCount  int    `json:"remainCount"`
	EarliestDate string `json:"earliestDate"`
	LatestDate   string `json:"latestDate"`
}

// PruneArchiveResponse reports the outcome of a local-only attendance archive cleanup.
type PruneArchiveResponse struct {
	BeforeDate  string `json:"beforeDate"`
	DeviceID    string `json:"deviceId"`
	Removed     int    `json:"removed"`
	RemainCount int    `json:"remainCount"`
	BackupPath  string `json:"backupPath"`
}

// SmartSyncPlanItem describes one employee's planned cross-device reconciliation.
type SmartSyncPlanItem struct {
	PIN               string   `json:"pin"`
	Name              string   `json:"name"`
	SourceDeviceID    string   `json:"sourceDeviceId"`
	SourceDeviceName  string   `json:"sourceDeviceName"`
	TargetDeviceIDs   []string `json:"targetDeviceIds"`
	TargetDeviceNames []string `json:"targetDeviceNames"`
	Reasons           []string `json:"reasons"`
	SourceFPCount     int      `json:"sourceFpCount"`
	SourceHasFace     bool     `json:"sourceHasFace"`
}

// SmartSyncPlan is the read-only preview before executing smart diff reconciliation.
type SmartSyncPlan struct {
	Mode           string              `json:"mode"` // "smart" | "master"
	MasterDeviceID string              `json:"masterDeviceId"`
	SyncInfo       bool                `json:"syncInfo"`
	SyncFP         bool                `json:"syncFp"`
	SyncFace       bool                `json:"syncFace"`
	TotalAffected  int                 `json:"totalAffected"`
	Skipped        int                 `json:"skipped"`
	Items          []SmartSyncPlanItem `json:"items"`
}

// SmartSyncExecuteResponse summarizes execution across grouped source devices.
type SmartSyncExecuteResponse struct {
	TotalPlanned int                 `json:"totalPlanned"`
	GroupCount   int                 `json:"groupCount"`
	AllSuccess   bool                `json:"allSuccess"`
	BackupPaths  []string            `json:"backupPaths"`
	Results      []SyncUsersResponse `json:"results"`
}

// OperationLog stores one persistent audit trail entry.
type OperationLog struct {
	ID          string `json:"id"`
	Timestamp   string `json:"timestamp"`
	Action      string `json:"action"`
	ActionLabel string `json:"actionLabel"`
	Target      string `json:"target"`
	Summary     string `json:"summary"`
	Success     bool   `json:"success"`
	Detail      string `json:"detail,omitempty"`
	BackupPath  string `json:"backupPath,omitempty"`
}

// ProgressEvent is emitted to the frontend during long operations.
type ProgressEvent struct {
	Task    string `json:"task"`
	Message string `json:"message"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Done    bool   `json:"done"`
}

// RuntimeHealth reports the status of Wine + SDK worker readiness.
type RuntimeHealth struct {
	Ready       bool   `json:"ready"`
	WinePath    string `json:"winePath"`
	WineVersion string `json:"wineVersion"`
	DataDir     string `json:"dataDir"`
	Error       string `json:"error,omitempty"`
}
