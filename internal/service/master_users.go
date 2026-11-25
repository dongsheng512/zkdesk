package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"zk_mac/internal/exporter"
	"zk_mac/internal/models"
)

// ==================== User Matrix & Cross-Device Sync ====================

func (s *ZKService) GetUserMatrix() (models.UserMatrixResponse, error) {
	devs, err := s.store.LoadDevices()
	if err != nil {
		return models.UserMatrixResponse{}, err
	}
	enabled := make([]models.DeviceConfig, 0, len(devs))
	for _, d := range devs {
		if d.Enabled {
			enabled = append(enabled, d)
		}
	}
	return s.buildMatrix(enabled, s.store.LoadSnapshots()), nil
}

func (s *ZKService) RefreshUsers(deviceIDs []string, includeFP bool) (models.UserMatrixResponse, error) {
	allDevs, err := s.store.LoadDevices()
	if err != nil {
		return models.UserMatrixResponse{}, err
	}
	targetMap := map[string]bool{}
	for _, id := range deviceIDs {
		targetMap[id] = true
	}
	targets := make([]models.DeviceConfig, 0, len(allDevs))
	for _, d := range allDevs {
		if !d.Enabled {
			continue
		}
		if len(targetMap) == 0 || targetMap[d.ID] {
			targets = append(targets, d)
		}
	}
	if len(targets) == 0 {
		return models.UserMatrixResponse{}, errors.New("未选择有效设备")
	}
	if err := s.requireConnectedDevices(targets...); err != nil {
		return models.UserMatrixResponse{}, err
	}

	var devErrs []string
	for i, d := range targets {
		s.emitProgress("users", fmt.Sprintf("正在拉取 %s 员工与指纹信息...", d.Name), i+1, len(targets), false)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		params := deviceParams(d)
		params["include_fp"] = includeFP

		var resp struct {
			Users []struct {
				PIN       string `json:"pin"`
				Name      string `json:"name"`
				Password  string `json:"password"`
				Privilege int    `json:"privilege"`
				Enabled   bool   `json:"enabled"`
				Card      string `json:"card"`
				HasFace   bool   `json:"has_face"`
				FPIndices []int  `json:"fp_indices"`
				FPCount   int    `json:"fp_count"`
			} `json:"users"`
			FacePhotoPINs []string `json:"face_photo_pins"`
		}
		err := s.worker.Call(ctx, "list_users", params, &resp)
		cancel()
		if err != nil {
			devErrs = append(devErrs, fmt.Sprintf("%s: %v", d.Name, err))
			continue
		}
		users := make([]models.DeviceUser, 0, len(resp.Users))
		for _, u := range resp.Users {
			users = append(users, models.DeviceUser{
				PIN:       u.PIN,
				Name:      u.Name,
				Password:  u.Password,
				Privilege: u.Privilege,
				Enabled:   u.Enabled,
				Card:      u.Card,
				HasFace:   u.HasFace,
				FPIndices: u.FPIndices,
				FPCount:   u.FPCount,
			})
		}
		if saveErr := s.store.SaveSnapshot(models.DeviceUserSnapshot{
			DeviceID:      d.ID,
			UpdatedAt:     time.Now().Format("2006-01-02 15:04:05"),
			Users:         users,
			FacePhotoPINs: resp.FacePhotoPINs,
		}); saveErr != nil {
			devErrs = append(devErrs, fmt.Sprintf("%s 保存本地缓存失败: %v", d.Name, saveErr))
		}
	}
	s.emitProgress("users", "员工列表刷新完成", len(targets), len(targets), true)

	matrix, err := s.GetUserMatrix()
	if err != nil {
		return matrix, err
	}
	if len(devErrs) > 0 {
		return matrix, fmt.Errorf("刷新考勤机名单失败：%s", strings.Join(devErrs, "；"))
	}
	return matrix, nil
}

func (s *ZKService) buildMatrix(devs []models.DeviceConfig, snaps map[string]models.DeviceUserSnapshot) models.UserMatrixResponse {
	masterUsers, masterUpdatedAt := s.store.LoadMasterUsers()
	profiles := s.store.LoadProfiles()
	byPIN := map[string]*models.MergedUserRow{}
	lastUpdated := ""
	deptSet := map[string]bool{}

	devIDs := make([]string, 0, len(devs))
	hasSnapByDev := make(map[string]bool, len(devs))
	for _, d := range devs {
		devIDs = append(devIDs, d.ID)
		if snap, ok := snaps[d.ID]; ok {
			hasSnapByDev[d.ID] = true
			if snap.UpdatedAt > lastUpdated {
				lastUpdated = snap.UpdatedAt
			}
		}
	}

	// 1. Seed rows from authoritative Local Master DB
	for pin, mu := range masterUsers {
		dept := mu.Department
		workStatus := mu.WorkStatus
		note := mu.Note
		if prof, ok := profiles[pin]; ok {
			if dept == "" {
				dept = prof.Department
			}
			if workStatus == "" {
				workStatus = prof.WorkStatus
			}
			if note == "" {
				note = prof.Note
			}
		}
		if dept != "" {
			deptSet[dept] = true
		}
		if workStatus == "" {
			workStatus = "active"
		}
		fpIndices := make([]int, 0, len(mu.Fingerprints))
		for _, fp := range mu.Fingerprints {
			fpIndices = append(fpIndices, fp.FingerID)
		}
		sort.Ints(fpIndices)
		byPIN[pin] = &models.MergedUserRow{
			PIN:               pin,
			InMaster:          true,
			PrimaryName:       mu.Name,
			Department:        dept,
			WorkStatus:        workStatus,
			ProfileNote:       note,
			Privilege:         mu.Privilege,
			Enabled:           mu.Enabled,
			Card:              mu.Card,
			MasterPasswordSet: mu.Password != "",
			MasterFPCount:     len(mu.Fingerprints),
			MasterFPIndices:   fpIndices,
			MasterUpdatedAt:   mu.UpdatedAt,
			TotalDevices:      len(devs),
			Devices:           make(map[string]models.MatrixCell, len(devs)),
		}
	}

	// 2. Overlay device snapshots onto rows (and include device-only PINs not yet in local master)
	for _, d := range devs {
		snap, ok := snaps[d.ID]
		if !ok {
			continue
		}
		for _, u := range snap.Users {
			row, exists := byPIN[u.PIN]
			if !exists {
				prof := profiles[u.PIN]
				if prof.Department != "" {
					deptSet[prof.Department] = true
				}
				row = &models.MergedUserRow{
					PIN:          u.PIN,
					InMaster:     false,
					PrimaryName:  u.Name,
					Department:   prof.Department,
					WorkStatus:   prof.WorkStatus,
					ProfileNote:  prof.Note,
					Privilege:    u.Privilege,
					Enabled:      u.Enabled,
					Card:         u.Card,
					TotalDevices: len(devs),
					Devices:      make(map[string]models.MatrixCell, len(devs)),
				}
				byPIN[u.PIN] = row
			}
			if !row.InMaster {
				if row.PrimaryName == "" && u.Name != "" {
					row.PrimaryName = u.Name
				}
				if row.Card == "" && u.Card != "" {
					row.Card = u.Card
				}
			}
			row.Devices[d.ID] = models.MatrixCell{
				Present:   true,
				Name:      u.Name,
				Privilege: u.Privilege,
				Enabled:   u.Enabled,
				Card:      u.Card,
				HasFace:   u.HasFace,
				FPCount:   u.FPCount,
			}
		}
	}

	// 3. Compare EVERY device against the Local Master DB (Local <-> Device reconciliation)
	rows := make([]models.MergedUserRow, 0, len(byPIN))
	diffCount := 0
	pendingPushCount := 0
	pendingPullCount := 0
	resignedInDev := 0

	for _, row := range byPIN {
		if path, srcDev, _ := s.store.FindLocalFace(row.PIN, devIDs...); path != "" {
			row.HasLocalFace = true
			row.LocalFaceDevID = srcDev
		}

		present := 0
		pushDevs := 0
		pullDevs := 0
		resignedDevs := 0
		reasonSet := map[string]bool{}
		reasons := make([]string, 0)
		addReason := func(r string) {
			if !reasonSet[r] {
				reasonSet[r] = true
				reasons = append(reasons, r)
			}
		}

		for _, d := range devs {
			cell, exists := row.Devices[d.ID]
			if exists && cell.Present {
				present++
			}
			if !hasSnapByDev[d.ID] {
				cell.SyncStatus = "no_snapshot"
				row.Devices[d.ID] = cell
				continue
			}

			if !row.InMaster {
				if cell.Present {
					if row.WorkStatus == "resigned" {
						cell.SyncStatus = "resigned_present"
						cell.DiffDetails = []string{"已离职仍在考勤机"}
						resignedDevs++
					} else {
						cell.SyncStatus = "need_pull"
						cell.DiffDetails = []string{"未入本地库"}
						pullDevs++
					}
				} else {
					cell.SyncStatus = "synced"
				}
				row.Devices[d.ID] = cell
				continue
			}

			if row.WorkStatus == "resigned" {
				if cell.Present {
					cell.SyncStatus = "resigned_present"
					cell.DiffDetails = []string{"已离职仍在考勤机"}
					resignedDevs++
				} else {
					cell.SyncStatus = "synced"
				}
				row.Devices[d.ID] = cell
				continue
			}

			// Active user in Local Master DB
			// Each attendance terminal maintains its own independent staff roster:
			// absence on a device is normal ("not_assigned"), NOT a sync error.
			if !cell.Present {
				cell.SyncStatus = "not_assigned"
				row.Devices[d.ID] = cell
				continue
			}

			pushDiffs := make([]string, 0)
			pullDiffs := make([]string, 0)

			if row.PrimaryName != "" && cell.Name != row.PrimaryName {
				pushDiffs = append(pushDiffs, "姓名待更新")
				addReason("姓名待下发")
			}
			if cell.Privilege != row.Privilege {
				pushDiffs = append(pushDiffs, "权限待更新")
				addReason("权限待下发")
			}
			if cell.Enabled != row.Enabled {
				pushDiffs = append(pushDiffs, "启用状态待更新")
				addReason("状态待下发")
			}
			if row.Card != "" && cell.Card != row.Card {
				pushDiffs = append(pushDiffs, "卡号待更新")
				addReason("卡号待下发")
			}
			if row.HasLocalFace && !cell.HasFace {
				pushDiffs = append(pushDiffs, "待下发人脸")
				addReason("人脸待下发")
			} else if !row.HasLocalFace && cell.HasFace {
				pullDiffs = append(pullDiffs, "机器有新人脸")
				addReason("机器有新人脸")
			}
			if row.MasterFPCount > cell.FPCount {
				pushDiffs = append(pushDiffs, fmt.Sprintf("待下发指纹(%d→%d)", row.MasterFPCount, cell.FPCount))
				addReason("指纹待下发")
			} else if cell.FPCount > row.MasterFPCount {
				pullDiffs = append(pullDiffs, fmt.Sprintf("机器有新指纹(%d>%d)", cell.FPCount, row.MasterFPCount))
				addReason("机器有新指纹")
			}

			switch {
			case len(pushDiffs) > 0 && len(pullDiffs) > 0:
				cell.SyncStatus = "both"
				cell.DiffDetails = append(pushDiffs, pullDiffs...)
				pushDevs++
				pullDevs++
			case len(pushDiffs) > 0:
				cell.SyncStatus = "need_push"
				cell.DiffDetails = pushDiffs
				pushDevs++
			case len(pullDiffs) > 0:
				cell.SyncStatus = "need_pull"
				cell.DiffDetails = pullDiffs
				pullDevs++
			default:
				cell.SyncStatus = "synced"
			}
			row.Devices[d.ID] = cell
		}

		row.PresentCount = present
		if !row.InMaster && pullDevs > 0 {
			addReason(fmt.Sprintf("仅在 %d 台考勤机（未入本地库）", pullDevs))
		}
		if resignedDevs > 0 {
			row.HasResignedInDev = true
			resignedInDev++
			addReason(fmt.Sprintf("已离职，仍在 %d 台设备", resignedDevs))
		}
		if pushDevs > 0 {
			row.HasPendingPush = true
			pendingPushCount++
		}
		if pullDevs > 0 {
			row.HasPendingPull = true
			pendingPullCount++
		}
		if len(reasons) > 0 || row.HasPendingPush || row.HasPendingPull || row.HasResignedInDev {
			row.HasDiff = true
			row.DiffReasons = reasons
			diffCount++
		}
		rows = append(rows, *row)
	}

	sort.Slice(rows, func(i, j int) bool {
		ni, ei := strconv.Atoi(rows[i].PIN)
		nj, ej := strconv.Atoi(rows[j].PIN)
		if ei == nil && ej == nil {
			return ni < nj
		}
		return rows[i].PIN < rows[j].PIN
	})

	depts := make([]string, 0, len(deptSet))
	for d := range deptSet {
		depts = append(depts, d)
	}
	sort.Strings(depts)

	return models.UserMatrixResponse{
		Devices:          devs,
		Rows:             rows,
		Departments:      depts,
		TotalUsers:       len(rows),
		MasterCount:      len(masterUsers),
		DiffUsers:        diffCount,
		PendingPushCount: pendingPushCount,
		PendingPullCount: pendingPullCount,
		ResignedInDev:    resignedInDev,
		LastUpdated:      lastUpdated,
		MasterUpdatedAt:  masterUpdatedAt,
		MasterFilePath:   s.store.MasterUsersPath(),
	}
}

// SaveMasterUser creates or updates an employee in the local master DB without modifying any hardware.
func (s *ZKService) SaveMasterUser(req models.SaveMasterUserRequest) (models.UserMatrixResponse, error) {
	req.PIN = strings.TrimSpace(req.PIN)
	req.Name = strings.TrimSpace(req.Name)
	if req.PIN == "" {
		return models.UserMatrixResponse{}, errors.New("工号不能为空")
	}

	existingMap, _ := s.store.LoadMasterUsers()
	cur, existed := existingMap[req.PIN]

	card := strings.TrimSpace(req.Card)
	if card == "" && req.PreserveCardIfEmpty && existed {
		card = cur.Card
	}
	pwd := strings.TrimSpace(req.Password)
	if pwd == "" && req.PreservePwdIfEmpty && existed {
		pwd = cur.Password
	}
	status := strings.TrimSpace(req.WorkStatus)
	if status == "" {
		if existed && cur.WorkStatus != "" {
			status = cur.WorkStatus
		} else {
			status = "active"
		}
	}

	mu := models.MasterUser{
		PIN:          req.PIN,
		Name:         req.Name,
		Department:   strings.TrimSpace(req.Department),
		WorkStatus:   status,
		Privilege:    req.Privilege,
		Enabled:      req.Enabled,
		Card:         card,
		Password:     pwd,
		Note:         strings.TrimSpace(req.Note),
		Fingerprints: cur.Fingerprints, // Always preserve stored fingerprint templates
	}

	if req.LocalFaceFilePath != "" {
		norm, err := exporter.NormalizeFaceJPEG(req.LocalFaceFilePath)
		if err != nil {
			return models.UserMatrixResponse{}, fmt.Errorf("处理人脸照片失败: %w", err)
		}
		if _, err := s.store.SaveFaceJPEG("master", req.PIN, norm); err != nil {
			return models.UserMatrixResponse{}, fmt.Errorf("保存本地人脸照片失败: %w", err)
		}
	}

	if err := s.store.SaveMasterUser(mu); err != nil {
		return models.UserMatrixResponse{}, err
	}

	actionLabel := "新增本地员工"
	if existed {
		actionLabel = "编辑本地员工"
	}
	s.recordAudit(
		"save_master_user",
		actionLabel,
		fmt.Sprintf("%s (%s)", mu.Name, mu.PIN),
		fmt.Sprintf("部门=%s，状态=%s，权限=%d（已保存本地主库，未改动考勤机）", mu.Department, mu.WorkStatus, mu.Privilege),
		true,
		"",
		"",
	)
	return s.GetUserMatrix()
}

// DeleteMasterUser removes a user from the local master DB (keeping a backup in backups/).
func (s *ZKService) DeleteMasterUser(pin string) (models.UserMatrixResponse, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return models.UserMatrixResponse{}, errors.New("工号不能为空")
	}
	backupPath, err := s.store.DeleteMasterUser(pin)
	if err != nil {
		return models.UserMatrixResponse{}, err
	}
	s.recordAudit("delete_master_user", "删除本地主库员工", fmt.Sprintf("工号 %s", pin), "已从本地主库移除（不影响考勤机）", true, "", backupPath)
	return s.GetUserMatrix()
}

// PullDevicesToMaster reads users/fingerprints/faces from selected devices (strictly READ-ONLY on hardware)
// and merges them into the durable local master DB.
func (s *ZKService) PullDevicesToMaster(req models.PullDevicesToMasterRequest) (models.PullToMasterResponse, error) {
	if len(req.DeviceIDs) == 0 {
		return models.PullToMasterResponse{}, errors.New("请至少选择一台源考勤机")
	}
	if !req.IncludeInfo && !req.IncludeFP && !req.IncludeFace {
		return models.PullToMasterResponse{}, errors.New("请至少勾选一项要吸纳的内容")
	}
	if err := s.requireConnectedIDs(req.DeviceIDs...); err != nil {
		return models.PullToMasterResponse{}, err
	}

	masterUsers, _ := s.store.LoadMasterUsers()
	profiles := s.store.LoadProfiles()
	backupPath := ""
	if len(masterUsers) > 0 {
		backupPath, _ = s.store.WriteBackup("pre_pull_to_master", masterUsers)
	}

	resp := models.PullToMasterResponse{
		DeviceIDs:  req.DeviceIDs,
		BackupPath: backupPath,
	}
	processedSet := map[string]bool{}
	now := time.Now().Format("2006-01-02 15:04:05")

	for devIdx, devID := range req.DeviceIDs {
		dev, err := s.findDevice(devID)
		if err != nil {
			resp.Errors = append(resp.Errors, devID+": "+err.Error())
			continue
		}
		s.emitProgress("pull_master", fmt.Sprintf("正在从 %s 读取员工数据到本地库 (%d/%d)...", dev.Name, devIdx+1, len(req.DeviceIDs)), devIdx+1, len(req.DeviceIDs), false)

		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		params := deviceParams(dev)
		params["pins"] = req.PINs
		params["include_fp"] = req.IncludeFP
		params["include_face"] = req.IncludeFace

		var bundleResp struct {
			Bundles []struct {
				PIN          string `json:"pin"`
				Name         string `json:"name"`
				Password     string `json:"password"`
				Privilege    int    `json:"privilege"`
				Enabled      bool   `json:"enabled"`
				Card         string `json:"card"`
				FaceBase64   string `json:"face_base64"`
				Fingerprints []struct {
					FingerID int    `json:"finger_id"`
					Flag     int    `json:"flag"`
					Template string `json:"template"`
				} `json:"fingerprints"`
			} `json:"bundles"`
		}
		err = s.worker.CallWithProgress(ctx, "read_user_bundle", params, &bundleResp, func(cur, total int, msg string) {
			s.emitProgress("pull_master", fmt.Sprintf("[%s] %s", dev.Name, msg), cur, total, false)
		})
		cancel()
		if err != nil {
			resp.Errors = append(resp.Errors, fmt.Sprintf("%s: %v", dev.Name, err))
			continue
		}

		for _, b := range bundleResp.Bundles {
			if b.PIN == "" {
				continue
			}
			processedSet[b.PIN] = true
			cur, existed := masterUsers[b.PIN]
			if !existed {
				prof := profiles[b.PIN]
				status := prof.WorkStatus
				if status == "" {
					status = "active"
				}
				cur = models.MasterUser{
					PIN:        b.PIN,
					Name:       b.Name,
					Department: prof.Department,
					WorkStatus: status,
					Privilege:  b.Privilege,
					Enabled:    b.Enabled,
					Card:       b.Card,
					Password:   b.Password,
					Note:       prof.Note,
					UpdatedAt:  now,
				}
				resp.Created++
			} else {
				if req.IncludeInfo {
					if req.OverwriteExistingInfo || cur.Name == "" {
						if b.Name != "" {
							cur.Name = b.Name
						}
					}
					if req.OverwriteExistingInfo {
						cur.Privilege = b.Privilege
						cur.Enabled = b.Enabled
					}
					if (req.OverwriteExistingInfo || cur.Card == "") && b.Card != "" {
						cur.Card = b.Card
					}
					if (req.OverwriteExistingInfo || cur.Password == "") && b.Password != "" {
						cur.Password = b.Password
					}
				}
				cur.UpdatedAt = now
				resp.Updated++
			}

			if req.IncludeFP && len(b.Fingerprints) > 0 {
				fpByID := make(map[int]models.FingerprintTemplate, len(cur.Fingerprints)+len(b.Fingerprints))
				for _, fp := range cur.Fingerprints {
					fpByID[fp.FingerID] = fp
				}
				for _, fp := range b.Fingerprints {
					if fp.Template == "" {
						continue
					}
					if _, has := fpByID[fp.FingerID]; !has || req.OverwriteExistingInfo {
						if !has {
							resp.FPsAbsorbed++
						}
						fpByID[fp.FingerID] = models.FingerprintTemplate{
							FingerID: fp.FingerID,
							Flag:     fp.Flag,
							Template: fp.Template,
						}
					}
				}
				mergedFPs := make([]models.FingerprintTemplate, 0, len(fpByID))
				for _, fp := range fpByID {
					mergedFPs = append(mergedFPs, fp)
				}
				sort.Slice(mergedFPs, func(i, j int) bool { return mergedFPs[i].FingerID < mergedFPs[j].FingerID })
				cur.Fingerprints = mergedFPs
			}

			if req.IncludeFace && b.FaceBase64 != "" {
				if raw, err := base64.StdEncoding.DecodeString(b.FaceBase64); err == nil && len(raw) > 0 {
					_, _ = s.store.SaveFaceJPEG(dev.ID, b.PIN, raw)
					existingPath, srcDev, _ := s.store.FindLocalFace(b.PIN)
					if existingPath == "" || srcDev != "master" || req.OverwriteExistingInfo {
						if _, err := s.store.SaveFaceJPEG("master", b.PIN, raw); err == nil {
							resp.FacesAbsorbed++
						}
					}
				}
			}

			masterUsers[b.PIN] = cur
		}
	}

	resp.TotalProcessed = len(processedSet)
	if err := s.store.SaveMasterUsersMap(masterUsers); err != nil {
		return resp, fmt.Errorf("保存本地人员主库失败: %w", err)
	}

	s.recordAudit(
		"pull_to_master",
		"吸纳到本地主库",
		fmt.Sprintf("%d 台设备 → 本地主库", len(req.DeviceIDs)),
		fmt.Sprintf("处理 %d 人（新增 %d，更新 %d，新指纹 %d 枚，人脸 %d 张）", resp.TotalProcessed, resp.Created, resp.Updated, resp.FPsAbsorbed, resp.FacesAbsorbed),
		len(resp.Errors) == 0,
		strings.Join(resp.Errors, "；"),
		backupPath,
	)
	s.emitProgress("pull_master", fmt.Sprintf("本地主库吸纳完成：共处理 %d 人", resp.TotalProcessed), len(req.DeviceIDs), len(req.DeviceIDs), true)
	return resp, nil
}

// PushMasterToDevices pushes selected users from the local master DB to selected attendance terminals.
func (s *ZKService) PushMasterToDevices(req models.PushMasterToDevicesRequest) (models.SyncUsersResponse, error) {
	if len(req.PINs) == 0 {
		return models.SyncUsersResponse{}, errors.New("请至少选择一名要下发的员工")
	}
	if len(req.DeviceIDs) == 0 {
		return models.SyncUsersResponse{}, errors.New("请至少选择一台目标考勤机")
	}
	if err := s.requireConnectedIDs(req.DeviceIDs...); err != nil {
		return models.SyncUsersResponse{}, err
	}

	masterUsers, _ := s.store.LoadMasterUsers()
	allDevs, _ := s.store.LoadDevices()
	devIDs := make([]string, 0, len(allDevs))
	for _, d := range allDevs {
		devIDs = append(devIDs, d.ID)
	}

	bundles := make([]map[string]any, 0, len(req.PINs))
	for _, pin := range req.PINs {
		mu, ok := masterUsers[pin]
		if !ok {
			continue
		}
		fps := make([]map[string]any, 0, len(mu.Fingerprints))
		if req.SyncFP {
			for _, fp := range mu.Fingerprints {
				if fp.Template == "" {
					continue
				}
				fps = append(fps, map[string]any{
					"finger_id": fp.FingerID,
					"flag":      fp.Flag,
					"template":  fp.Template,
				})
			}
		}
		faceB64 := ""
		if req.SyncFace {
			if p, _, _ := s.store.FindLocalFace(pin, devIDs...); p != "" {
				if raw, err := os.ReadFile(p); err == nil && len(raw) > 0 {
					faceB64 = base64.StdEncoding.EncodeToString(raw)
				}
			}
		}
		bundles = append(bundles, map[string]any{
			"pin":          mu.PIN,
			"name":         mu.Name,
			"password":     mu.Password,
			"privilege":    mu.Privilege,
			"enabled":      mu.Enabled,
			"card":         mu.Card,
			"fingerprints": fps,
			"face_base64":  faceB64,
		})
	}
	if len(bundles) == 0 {
		return models.SyncUsersResponse{}, errors.New("所选工号均不在本地主库中，请先吸纳或保存到本地主库")
	}

	backupPath, _ := s.store.WriteBackup("push_master_to_devices", map[string]any{
		"request": req,
		"count":   len(bundles),
	})

	resp := models.SyncUsersResponse{
		SourceDeviceID: "master",
		RequestedPINs:  len(req.PINs),
		ReadBundles:    len(bundles),
		BackupPath:     backupPath,
		Results:        make([]models.SyncDeviceResult, 0, len(req.DeviceIDs)),
	}

	for i, targetID := range req.DeviceIDs {
		tDev, err := s.findDevice(targetID)
		if err != nil {
			resp.Results = append(resp.Results, models.SyncDeviceResult{DeviceID: targetID, Success: false, Error: err.Error()})
			continue
		}
		s.emitProgress("push_master", fmt.Sprintf("正在下发 %d 人到 %s (%d/%d)...", len(bundles), tDev.Name, i+1, len(req.DeviceIDs)), i+1, len(req.DeviceIDs), false)

		wCtx, cancelW := context.WithTimeout(context.Background(), 180*time.Second)
		wParams := deviceParams(tDev)
		wParams["bundles"] = bundles
		wParams["sync_info"] = req.SyncInfo
		wParams["sync_fp"] = req.SyncFP
		wParams["sync_face"] = req.SyncFace
		wParams["preserve_card_if_empty"] = req.PreserveCardIfEmpty
		wParams["preserve_pwd_if_empty"] = req.PreservePwdIfEmpty

		var wResp struct {
			Results []struct {
				PIN      string   `json:"pin"`
				InfoOK   bool     `json:"info_ok"`
				FPSynced int      `json:"fp_synced"`
				FaceOK   bool     `json:"face_ok"`
				Errors   []string `json:"errors"`
			} `json:"results"`
		}
		err = s.worker.CallWithProgress(wCtx, "write_user_bundle", wParams, &wResp, func(cur, total int, msg string) {
			s.emitProgress("push_master", fmt.Sprintf("[%s] %s", tDev.Name, msg), cur, total, false)
		})
		cancelW()
		if err != nil {
			resp.Results = append(resp.Results, models.SyncDeviceResult{
				DeviceID:   tDev.ID,
				DeviceName: tDev.Name,
				Success:    false,
				Error:      err.Error(),
			})
			continue
		}
		items := make([]models.SyncUserItemResult, 0, len(wResp.Results))
		allOK := true
		for _, it := range wResp.Results {
			if len(it.Errors) > 0 {
				allOK = false
			}
			items = append(items, models.SyncUserItemResult{
				PIN:      it.PIN,
				InfoOK:   it.InfoOK,
				FPSynced: it.FPSynced,
				FaceOK:   it.FaceOK,
				Errors:   it.Errors,
			})
		}
		resp.Results = append(resp.Results, models.SyncDeviceResult{
			DeviceID:   tDev.ID,
			DeviceName: tDev.Name,
			Success:    allOK,
			Items:      items,
		})
	}

	allSyncOK := true
	var details []string
	for _, r := range resp.Results {
		if !r.Success {
			allSyncOK = false
			details = append(details, fmt.Sprintf("%s: %s", r.DeviceName, r.Error))
		} else {
			details = append(details, fmt.Sprintf("%s: 成功 %d 人", r.DeviceName, len(r.Items)))
		}
	}
	s.recordAudit(
		"push_master",
		"本地库下发考勤机",
		fmt.Sprintf("本地主库 → %d 台设备", len(resp.Results)),
		fmt.Sprintf("下发 %d 人（信息=%v 指纹=%v 人脸=%v）", len(bundles), req.SyncInfo, req.SyncFP, req.SyncFace),
		allSyncOK,
		strings.Join(details, "；"),
		backupPath,
	)
	s.emitProgress("push_master", "本地库下发完成", len(req.DeviceIDs), len(req.DeviceIDs), true)
	return resp, nil
}

// ExportMasterUsersBackup opens a native Save dialog to back up master_users.json.
func (s *ZKService) ExportMasterUsersBackup() (models.MasterBackupResponse, error) {
	if s.app == nil {
		return models.MasterBackupResponse{}, errors.New("应用上下文未就绪")
	}
	defaultName := fmt.Sprintf("zk_master_users_%s.json", time.Now().Format("20060102_1504"))
	dest, err := s.app.Dialog.SaveFile().
		SetMessage("备份本地人员主库（含部门、在职状态与指纹模板）").
		SetFilename(defaultName).
		AddFilter("JSON 备份文件", "*.json").
		PromptForSingleSelection()
	if err != nil || dest == "" {
		return models.MasterBackupResponse{}, err
	}
	cnt, err := s.store.ExportMasterUsersBackup(dest)
	if err != nil {
		return models.MasterBackupResponse{}, err
	}
	s.recordAudit("backup_master", "备份本地人员库", filepath.Base(dest), fmt.Sprintf("已导出 %d 名员工主库档案", cnt), true, "", dest)
	return models.MasterBackupResponse{FilePath: dest, UserCount: cnt}, nil
}

// ExportMasterUsersExcel opens a native Save dialog to export the local master directory to Excel (.xlsx).
func (s *ZKService) ExportMasterUsersExcel() (models.MasterBackupResponse, error) {
	if s.app == nil {
		return models.MasterBackupResponse{}, errors.New("应用上下文未就绪")
	}
	masterUsers, _ := s.store.LoadMasterUsers()
	if len(masterUsers) == 0 {
		return models.MasterBackupResponse{}, errors.New("本地人员主库为空，无法导出")
	}
	defaultName := fmt.Sprintf("本地人员花名册_%s.xlsx", time.Now().Format("20060102_1504"))
	dest, err := s.app.Dialog.SaveFile().
		SetMessage("导出本地人员主库花名册 (Excel)").
		SetFilename(defaultName).
		AddFilter("Excel 工作簿", "*.xlsx").
		PromptForSingleSelection()
	if err != nil || dest == "" {
		return models.MasterBackupResponse{}, err
	}
	allDevs, _ := s.store.LoadDevices()
	devIDs := make([]string, 0, len(allDevs))
	for _, d := range allDevs {
		devIDs = append(devIDs, d.ID)
	}
	list := make([]models.MasterUser, 0, len(masterUsers))
	hasFace := make(map[string]bool, len(masterUsers))
	for _, u := range masterUsers {
		list = append(list, u)
		if p, _, _ := s.store.FindLocalFace(u.PIN, devIDs...); p != "" {
			hasFace[u.PIN] = true
		}
	}
	if err := exporter.ExportMasterUsersXLSX(dest, list, hasFace); err != nil {
		return models.MasterBackupResponse{}, err
	}
	s.recordAudit("export_master_xlsx", "导出本地花名册", filepath.Base(dest), fmt.Sprintf("已导出 %d 名员工到 Excel", len(list)), true, "", dest)
	return models.MasterBackupResponse{FilePath: dest, UserCount: len(list)}, nil
}

// ImportMasterUsersBackup restores master_users.json from a JSON backup (saving a pre-restore backup first).
func (s *ZKService) ImportMasterUsersBackup() (models.MasterBackupResponse, error) {
	if s.app == nil {
		return models.MasterBackupResponse{}, errors.New("应用上下文未就绪")
	}
	src, err := s.app.Dialog.OpenFile().
		SetTitle("选择本地人员主库备份文件 (.json)").
		AddFilter("JSON 备份文件", "*.json").
		PromptForSingleSelection()
	if err != nil || src == "" {
		return models.MasterBackupResponse{}, err
	}
	cnt, preBak, err := s.store.ImportMasterUsersBackup(src)
	if err != nil {
		return models.MasterBackupResponse{}, err
	}
	s.recordAudit("restore_master", "恢复本地人员库", filepath.Base(src), fmt.Sprintf("已恢复 %d 名员工主库档案", cnt), true, "", preBak)
	return models.MasterBackupResponse{FilePath: src, UserCount: cnt, BackupPath: preBak}, nil
}
