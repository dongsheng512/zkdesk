package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"zk_mac/internal/models"
	"zk_mac/internal/store"
	"zk_mac/internal/worker"
)

// ZKService exposes all attendance terminal operations to the Wails v3 frontend.
type ZKService struct {
	app       *application.App
	store     *store.Store
	worker    *worker.Manager
	connMu    sync.RWMutex
	connected map[string]bool
}

func NewZKService(st *store.Store, wm *worker.Manager) *ZKService {
	svc := &ZKService{
		store:     st,
		worker:    wm,
		connected: map[string]bool{},
	}
	if wm != nil {
		wm.SetOnReset(svc.handleWorkerReset)
	}
	return svc
}

func (s *ZKService) handleWorkerReset() {
	s.connMu.Lock()
	hadConnected := len(s.connected) > 0
	s.connected = map[string]bool{}
	s.connMu.Unlock()

	statuses, _ := s.store.ResetAllConnectedStatuses()
	if s.app != nil && hadConnected {
		s.app.Event.Emit("zk:connections-reset", map[string]any{
			"reason":   "worker_reset",
			"statuses": statuses,
		})
	}
}

func (s *ZKService) AttachApp(app *application.App) {
	s.app = app
}

func (s *ZKService) emitProgress(task, msg string, current, total int, done bool) {
	if s.app == nil {
		return
	}
	s.app.Event.Emit("zk:progress", models.ProgressEvent{
		Task:    task,
		Message: msg,
		Current: current,
		Total:   total,
		Done:    done,
	})
}

func (s *ZKService) recordAudit(action, label, target, summary string, success bool, detail, backupPath string) {
	_ = s.store.AppendOperationLog(models.OperationLog{
		Action:      action,
		ActionLabel: label,
		Target:      target,
		Summary:     summary,
		Success:     success,
		Detail:      detail,
		BackupPath:  backupPath,
	})
}

func computeTimeDrift(deviceTime string, ref time.Time) (int, bool) {
	if deviceTime == "" {
		return 0, false
	}
	dt, err := time.ParseInLocation("2006-01-02 15:04:05", deviceTime, time.Local)
	if err != nil {
		return 0, false
	}
	return int(dt.Sub(ref).Seconds()), true
}

func (s *ZKService) findDevice(id string) (models.DeviceConfig, error) {
	list, err := s.store.LoadDevices()
	if err != nil {
		return models.DeviceConfig{}, err
	}
	for _, d := range list {
		if d.ID == id {
			return d, nil
		}
	}
	return models.DeviceConfig{}, fmt.Errorf("未找到设备: %s", id)
}

func deviceParams(d models.DeviceConfig) map[string]any {
	port := d.Port
	if port <= 0 {
		port = 4370
	}
	mn := d.MachineNumber
	if mn <= 0 {
		mn = 1
	}
	return map[string]any{
		"ip":             d.IP,
		"port":           port,
		"password":       d.Password,
		"machine_number": mn,
	}
}

// ==================== Runtime & Device Management ====================

func (s *ZKService) CheckRuntime() models.RuntimeHealth {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var resp struct {
		Status string `json:"status"`
	}
	err := s.worker.Call(ctx, "ping", nil, &resp)
	h := models.RuntimeHealth{
		Ready:       err == nil && resp.Status == "ready",
		WinePath:    s.worker.WinePath(),
		WineVersion: s.worker.WineVersion(),
		DataDir:     s.store.DataDir(),
	}
	if err != nil {
		h.Error = err.Error()
	}
	return h
}

func (s *ZKService) GetDevices() ([]models.DeviceConfig, error) {
	return s.store.LoadDevices()
}

func (s *ZKService) SaveDevice(dev models.DeviceConfig) ([]models.DeviceConfig, error) {
	dev.ID = strings.TrimSpace(dev.ID)
	dev.IP = strings.TrimSpace(dev.IP)
	dev.Name = strings.TrimSpace(dev.Name)
	if err := store.ValidateDeviceID(dev.ID); err != nil {
		return nil, err
	}
	if dev.IP == "" {
		return nil, errors.New("设备 ID 和 IP 地址不能为空")
	}
	if dev.Name == "" {
		dev.Name = "考勤机 " + dev.ID
	}
	if dev.Port <= 0 {
		dev.Port = 4370
	}
	if dev.MachineNumber <= 0 {
		dev.MachineNumber = 1
	}
	list, err := s.store.LoadDevices()
	if err != nil {
		return nil, err
	}
	var prevDev *models.DeviceConfig
	updated := false
	for i := range list {
		if list[i].ID == dev.ID {
			cp := list[i]
			prevDev = &cp
			list[i] = dev
			updated = true
			break
		}
	}
	if !updated {
		list = append(list, dev)
	}
	if err := s.store.SaveDevices(list); err != nil {
		return nil, err
	}

	// Disconnect stale session if connection parameters changed or device was disabled.
	if prevDev != nil && s.isDeviceConnected(dev.ID) {
		connChanged := prevDev.IP != dev.IP ||
			prevDev.Port != dev.Port ||
			prevDev.Password != dev.Password ||
			prevDev.MachineNumber != dev.MachineNumber ||
			!dev.Enabled
		if connChanged {
			if s.worker != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = s.worker.Call(ctx, "disconnect_device", deviceParams(*prevDev), nil)
				cancel()
			}
			s.connMu.Lock()
			delete(s.connected, dev.ID)
			s.connMu.Unlock()

			cached := s.store.LoadStatuses()
			if st, ok := cached[dev.ID]; ok {
				st.Connected = false
				st.Error = ""
				_ = s.store.SaveStatus(st)
			}
		}
	}
	return list, nil
}

func (s *ZKService) DeleteDevice(id string) ([]models.DeviceConfig, error) {
	list, err := s.store.LoadDevices()
	if err != nil {
		return nil, err
	}
	var removed *models.DeviceConfig
	next := make([]models.DeviceConfig, 0, len(list))
	for _, d := range list {
		if d.ID == id {
			cp := d
			removed = &cp
			continue
		}
		next = append(next, d)
	}
	if err := s.store.SaveDevices(next); err != nil {
		return nil, err
	}
	if removed != nil && s.isDeviceConnected(id) && s.worker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.worker.Call(ctx, "disconnect_device", deviceParams(*removed), nil)
		cancel()
	}
	s.connMu.Lock()
	delete(s.connected, id)
	s.connMu.Unlock()
	_ = s.store.DeleteDeviceCache(id)
	return next, nil
}

func (s *ZKService) GetCachedStatuses() map[string]models.DeviceStatus {
	m := s.store.LoadStatuses()
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	for id, st := range m {
		st.Connected = s.connected[id]
		m[id] = st
	}
	return m
}

func (s *ZKService) ConnectDevice(deviceID string) (models.DeviceStatus, error) {
	dev, err := s.findDevice(deviceID)
	if err != nil {
		return models.DeviceStatus{}, err
	}
	s.emitProgress("connect", fmt.Sprintf("正在连接 %s (%s)...", dev.Name, dev.IP), 1, 1, false)
	st := s.connectOne(dev)
	s.emitProgress("connect", "连接操作完成", 1, 1, true)
	return st, nil
}

func (s *ZKService) DisconnectDevice(deviceID string) (models.DeviceStatus, error) {
	dev, err := s.findDevice(deviceID)
	if err != nil {
		return models.DeviceStatus{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.worker.Call(ctx, "disconnect_device", deviceParams(dev), nil)

	s.connMu.Lock()
	delete(s.connected, dev.ID)
	s.connMu.Unlock()

	cached := s.store.LoadStatuses()
	st := cached[dev.ID]
	st.DeviceID = dev.ID
	st.Connected = false
	st.Error = ""
	_ = s.store.SaveStatus(st)
	return st, nil
}

func (s *ZKService) ConnectAllDevices() (map[string]models.DeviceStatus, error) {
	devs, err := s.store.LoadDevices()
	if err != nil {
		return nil, err
	}
	enabled := make([]models.DeviceConfig, 0, len(devs))
	for _, d := range devs {
		if d.Enabled {
			enabled = append(enabled, d)
		}
	}
	res := make(map[string]models.DeviceStatus, len(enabled))
	for i, d := range enabled {
		s.emitProgress("connect", fmt.Sprintf("正在连接 %s (%s)...", d.Name, d.IP), i+1, len(enabled), false)
		res[d.ID] = s.connectOne(d)
	}
	s.emitProgress("connect", "全部设备连接完成", len(enabled), len(enabled), true)
	return res, nil
}

func (s *ZKService) DisconnectAllDevices() (map[string]models.DeviceStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.worker.Call(ctx, "disconnect_all", nil, nil)

	s.connMu.Lock()
	s.connected = map[string]bool{}
	s.connMu.Unlock()

	m, _ := s.store.ResetAllConnectedStatuses()
	return m, nil
}

func (s *ZKService) isDeviceConnected(deviceID string) bool {
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return s.connected[deviceID]
}

func (s *ZKService) requireConnectedDevices(devs ...models.DeviceConfig) error {
	var disconnected []string
	for _, d := range devs {
		if !s.isDeviceConnected(d.ID) {
			disconnected = append(disconnected, d.Name)
		}
	}
	if len(disconnected) > 0 {
		return fmt.Errorf("考勤机「%s」尚未连接，请先在「设备总览」中手动连接后再操作", strings.Join(disconnected, "、"))
	}
	return nil
}

func (s *ZKService) requireConnectedIDs(deviceIDs ...string) error {
	devs := make([]models.DeviceConfig, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		dev, err := s.findDevice(id)
		if err != nil {
			return err
		}
		devs = append(devs, dev)
	}
	return s.requireConnectedDevices(devs...)
}

func (s *ZKService) ProbeDevice(deviceID string) (models.DeviceStatus, error) {
	dev, err := s.findDevice(deviceID)
	if err != nil {
		return models.DeviceStatus{}, err
	}
	if err := s.requireConnectedDevices(dev); err != nil {
		return models.DeviceStatus{}, err
	}
	return s.probeOne(dev), nil
}

func (s *ZKService) ProbeAllDevices() (map[string]models.DeviceStatus, error) {
	devs, err := s.store.LoadDevices()
	if err != nil {
		return nil, err
	}
	connectedDevs := make([]models.DeviceConfig, 0, len(devs))
	for _, d := range devs {
		if d.Enabled && s.isDeviceConnected(d.ID) {
			connectedDevs = append(connectedDevs, d)
		}
	}
	if len(connectedDevs) == 0 {
		return nil, errors.New("当前没有已连接的考勤机，请先在「设备总览」中手动连接")
	}
	res := make(map[string]models.DeviceStatus, len(connectedDevs))
	for i, d := range connectedDevs {
		s.emitProgress("probe", fmt.Sprintf("正在刷新 %s (%s)...", d.Name, d.IP), i+1, len(connectedDevs), false)
		st := s.probeOne(d)
		res[d.ID] = st
	}
	s.emitProgress("probe", "已连接设备状态刷新完成", len(connectedDevs), len(connectedDevs), true)
	return res, nil
}

func (s *ZKService) connectOne(dev models.DeviceConfig) models.DeviceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var info struct {
		SN          string `json:"sn"`
		Firmware    string `json:"firmware"`
		Platform    string `json:"platform"`
		DeviceTime  string `json:"device_time"`
		FaceVersion string `json:"face_version"`
		BioVersion  string `json:"bio_version"`
		UserCount   int    `json:"user_count"`
		FPCount     int    `json:"fp_count"`
		FaceCount   int    `json:"face_count"`
		LogCount    int    `json:"log_count"`
		LogMode     string `json:"log_mode"`
		Connected   bool   `json:"connected"`
	}
	st := models.DeviceStatus{
		DeviceID:  dev.ID,
		CheckedAt: time.Now().Format("2006-01-02 15:04:05"),
	}
	if err := s.worker.Call(ctx, "connect_device", deviceParams(dev), &info); err != nil {
		st.Online = false
		st.Connected = false
		st.Error = err.Error()
		s.connMu.Lock()
		delete(s.connected, dev.ID)
		s.connMu.Unlock()
	} else {
		st.Online = true
		st.Connected = true
		st.SN = info.SN
		st.Firmware = info.Firmware
		st.Platform = info.Platform
		st.DeviceTime = info.DeviceTime
		st.TimeDriftSec, st.HasTimeDrift = computeTimeDrift(info.DeviceTime, time.Now())
		st.FaceVersion = info.FaceVersion
		st.BioVersion = info.BioVersion
		st.UserCount = info.UserCount
		st.FPCount = info.FPCount
		st.FaceCount = info.FaceCount
		st.LogCount = info.LogCount
		st.LogMode = info.LogMode
		s.connMu.Lock()
		s.connected[dev.ID] = true
		s.connMu.Unlock()
	}
	_ = s.store.SaveStatus(st)
	return st
}

func (s *ZKService) SyncDeviceTime(deviceID string) (models.DeviceStatus, error) {
	dev, err := s.findDevice(deviceID)
	if err != nil {
		return models.DeviceStatus{}, err
	}
	if err := s.requireConnectedDevices(dev); err != nil {
		return models.DeviceStatus{}, err
	}
	s.emitProgress("sync_time", fmt.Sprintf("正在为 %s 校准时间...", dev.Name), 1, 1, false)
	st := s.syncTimeOne(dev)
	s.emitProgress("sync_time", "校时完成", 1, 1, true)
	return st, nil
}

func (s *ZKService) SyncAllDeviceTimes() (map[string]models.DeviceStatus, error) {
	devs, err := s.store.LoadDevices()
	if err != nil {
		return nil, err
	}
	targets := make([]models.DeviceConfig, 0, len(devs))
	for _, d := range devs {
		if d.Enabled && s.isDeviceConnected(d.ID) {
			targets = append(targets, d)
		}
	}
	if len(targets) == 0 {
		return nil, errors.New("当前没有已连接的考勤机，请先在「设备总览」中手动连接")
	}
	res := make(map[string]models.DeviceStatus, len(targets))
	okCnt := 0
	var errs []string
	for i, d := range targets {
		s.emitProgress("sync_time", fmt.Sprintf("正在为 %s 校准时间...", d.Name), i+1, len(targets), false)
		st := s.syncTimeOne(d)
		res[d.ID] = st
		if st.Error == "" {
			okCnt++
		} else {
			errs = append(errs, fmt.Sprintf("%s: %s", d.Name, st.Error))
		}
	}
	s.emitProgress("sync_time", fmt.Sprintf("批量校时完成（成功 %d 台）", okCnt), len(targets), len(targets), true)
	if len(errs) > 0 && okCnt == 0 {
		return res, errors.New(strings.Join(errs, "; "))
	}
	return res, nil
}

func (s *ZKService) syncTimeOne(dev models.DeviceConfig) models.DeviceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	now := time.Now()
	params := deviceParams(dev)
	params["year"] = now.Year()
	params["month"] = int(now.Month())
	params["day"] = now.Day()
	params["hour"] = now.Hour()
	params["minute"] = now.Minute()
	params["second"] = now.Second()

	cached := s.store.LoadStatuses()
	st := cached[dev.ID]
	st.DeviceID = dev.ID
	st.CheckedAt = time.Now().Format("2006-01-02 15:04:05")

	var resp struct {
		OK         bool   `json:"ok"`
		DeviceTime string `json:"device_time"`
		Connected  bool   `json:"connected"`
	}
	if err := s.worker.Call(ctx, "sync_time", params, &resp); err != nil {
		st.Error = err.Error()
		st.Connected = false
		s.connMu.Lock()
		delete(s.connected, dev.ID)
		s.connMu.Unlock()
		s.recordAudit("sync_time", "同步时间", dev.Name, "校时失败", false, err.Error(), "")
	} else {
		st.Online = true
		st.Connected = resp.Connected
		st.Error = ""
		st.DeviceTime = resp.DeviceTime
		st.TimeDriftSec, st.HasTimeDrift = computeTimeDrift(resp.DeviceTime, time.Now())
		s.connMu.Lock()
		if resp.Connected {
			s.connected[dev.ID] = true
		} else {
			delete(s.connected, dev.ID)
		}
		s.connMu.Unlock()
		s.recordAudit("sync_time", "同步时间", dev.Name, fmt.Sprintf("已校准为 %s", resp.DeviceTime), true, "", "")
	}
	_ = s.store.SaveStatus(st)
	return st
}

func (s *ZKService) probeOne(dev models.DeviceConfig) models.DeviceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var info struct {
		SN          string `json:"sn"`
		Firmware    string `json:"firmware"`
		Platform    string `json:"platform"`
		DeviceTime  string `json:"device_time"`
		FaceVersion string `json:"face_version"`
		BioVersion  string `json:"bio_version"`
		UserCount   int    `json:"user_count"`
		FPCount     int    `json:"fp_count"`
		FaceCount   int    `json:"face_count"`
		LogCount    int    `json:"log_count"`
		LogMode     string `json:"log_mode"`
		Connected   bool   `json:"connected"`
	}
	st := models.DeviceStatus{
		DeviceID:  dev.ID,
		CheckedAt: time.Now().Format("2006-01-02 15:04:05"),
	}
	if err := s.worker.Call(ctx, "device_info", deviceParams(dev), &info); err != nil {
		st.Online = false
		st.Connected = false
		st.Error = err.Error()
		s.connMu.Lock()
		delete(s.connected, dev.ID)
		s.connMu.Unlock()
	} else {
		st.Online = true
		st.Connected = info.Connected
		st.SN = info.SN
		st.Firmware = info.Firmware
		st.Platform = info.Platform
		st.DeviceTime = info.DeviceTime
		st.TimeDriftSec, st.HasTimeDrift = computeTimeDrift(info.DeviceTime, time.Now())
		st.FaceVersion = info.FaceVersion
		st.BioVersion = info.BioVersion
		st.UserCount = info.UserCount
		st.FPCount = info.FPCount
		st.FaceCount = info.FaceCount
		st.LogCount = info.LogCount
		st.LogMode = info.LogMode
		s.connMu.Lock()
		if info.Connected {
			s.connected[dev.ID] = true
		} else {
			delete(s.connected, dev.ID)
		}
		s.connMu.Unlock()
	}
	_ = s.store.SaveStatus(st)
	return st
}
