package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"zk_mac/internal/exporter"
	"zk_mac/internal/models"
)

func (s *ZKService) UpsertUser(req models.UpsertUserRequest) ([]models.ActionDeviceResult, error) {
	req.PIN = strings.TrimSpace(req.PIN)
	req.Name = strings.TrimSpace(req.Name)
	if req.PIN == "" {
		return nil, errors.New("工号不能为空")
	}
	if len(req.DeviceIDs) == 0 {
		return nil, errors.New("请至少选择一台目标考勤机")
	}
	if err := s.requireConnectedIDs(req.DeviceIDs...); err != nil {
		return nil, err
	}

	// Persist local HR metadata (department / work status / note) alongside hardware write
	if req.Department != "" || req.WorkStatus != "" || req.ProfileNote != "" {
		_ = s.store.SaveProfile(models.UserProfile{
			PIN:        req.PIN,
			Department: strings.TrimSpace(req.Department),
			WorkStatus: strings.TrimSpace(req.WorkStatus),
			Note:       strings.TrimSpace(req.ProfileNote),
		})
	}

	backupPath, _ := s.store.WriteBackup("upsert_user_"+req.PIN, req)
	results := make([]models.ActionDeviceResult, 0, len(req.DeviceIDs))
	okCnt := 0
	var errParts []string

	for i, devID := range req.DeviceIDs {
		dev, err := s.findDevice(devID)
		if err != nil {
			results = append(results, models.ActionDeviceResult{DeviceID: devID, OK: false, Error: err.Error()})
			errParts = append(errParts, devID+": "+err.Error())
			continue
		}
		s.emitProgress("upsert", fmt.Sprintf("正在写入 %s (%s) 到 %s...", req.Name, req.PIN, dev.Name), i+1, len(req.DeviceIDs), false)

		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		params := deviceParams(dev)
		params["pin"] = req.PIN
		params["name"] = req.Name
		params["user_password"] = req.Password
		params["privilege"] = req.Privilege
		params["enabled"] = req.Enabled
		params["card"] = req.Card
		params["preserve_card_if_empty"] = req.PreserveCardIfEmpty
		params["preserve_pwd_if_empty"] = req.PreservePwdIfEmpty

		err = s.worker.Call(ctx, "upsert_user", params, nil)
		cancel()
		if err != nil {
			results = append(results, models.ActionDeviceResult{DeviceID: dev.ID, DeviceName: dev.Name, OK: false, Error: err.Error()})
			errParts = append(errParts, dev.Name+": "+err.Error())
		} else {
			okCnt++
			results = append(results, models.ActionDeviceResult{DeviceID: dev.ID, DeviceName: dev.Name, OK: true, Message: "写入成功"})
		}
	}
	s.recordAudit(
		"upsert_user",
		"写入员工",
		fmt.Sprintf("%s (%s)", req.Name, req.PIN),
		fmt.Sprintf("写入 %d/%d 台设备", okCnt, len(req.DeviceIDs)),
		len(errParts) == 0,
		strings.Join(errParts, "；"),
		backupPath,
	)
	s.emitProgress("upsert", "员工信息写入完成", len(req.DeviceIDs), len(req.DeviceIDs), true)
	return results, nil
}

// SaveUserProfile updates local department/status/note without touching any device.
func (s *ZKService) SaveUserProfile(profile models.UserProfile) (models.UserMatrixResponse, error) {
	profile.PIN = strings.TrimSpace(profile.PIN)
	if profile.PIN == "" {
		return models.UserMatrixResponse{}, errors.New("工号不能为空")
	}
	profile.Department = strings.TrimSpace(profile.Department)
	profile.WorkStatus = strings.TrimSpace(profile.WorkStatus)
	profile.Note = strings.TrimSpace(profile.Note)
	if err := s.store.SaveProfile(profile); err != nil {
		return models.UserMatrixResponse{}, err
	}
	return s.GetUserMatrix()
}

// ImportProfilesFromDialog opens a native macOS file picker to import Excel/CSV department & status profiles.
func (s *ZKService) ImportProfilesFromDialog() (models.ImportProfilesResponse, error) {
	if s.app == nil {
		return models.ImportProfilesResponse{}, errors.New("应用上下文未就绪")
	}
	path, err := s.app.Dialog.OpenFile().
		SetTitle("导入员工部门 / 离职状态表 (.xlsx / .csv)").
		AddFilter("表格文件", "*.xlsx;*.csv;*.XLSX;*.CSV").
		PromptForSingleSelection()
	if err != nil || path == "" {
		return models.ImportProfilesResponse{}, err
	}
	profiles, skipped, err := exporter.ParseProfilesFile(path)
	if err != nil {
		return models.ImportProfilesResponse{}, err
	}
	imported, err := s.store.MergeProfiles(profiles)
	if err != nil {
		return models.ImportProfilesResponse{}, err
	}
	s.recordAudit(
		"import_profiles",
		"导入部门档案",
		filepath.Base(path),
		fmt.Sprintf("已更新 %d 人部门/状态（跳过 %d 行）", imported, skipped),
		true,
		"",
		"",
	)
	return models.ImportProfilesResponse{
		FilePath: path,
		Imported: imported,
		Skipped:  skipped,
	}, nil
}

// ClearUserBiometric clears only fingerprints, password, or face while keeping the employee record.
func (s *ZKService) ClearUserBiometric(req models.ClearBiometricRequest) ([]models.ActionDeviceResult, error) {
	req.PIN = strings.TrimSpace(req.PIN)
	if req.PIN == "" || len(req.DeviceIDs) == 0 {
		return nil, errors.New("请指定工号和目标考勤机")
	}
	if err := s.requireConnectedIDs(req.DeviceIDs...); err != nil {
		return nil, err
	}
	targetLabels := map[string]string{
		"fingerprints": "指纹",
		"password":     "密码",
		"face":         "人脸",
	}
	lbl := targetLabels[req.Target]
	if lbl == "" {
		return nil, fmt.Errorf("不支持的清理类型: %s", req.Target)
	}

	backupPath, _ := s.store.WriteBackup("clear_"+req.Target+"_"+req.PIN, req)
	results := make([]models.ActionDeviceResult, 0, len(req.DeviceIDs))
	okCnt := 0
	var errs []string

	for i, devID := range req.DeviceIDs {
		dev, err := s.findDevice(devID)
		if err != nil {
			results = append(results, models.ActionDeviceResult{DeviceID: devID, OK: false, Error: err.Error()})
			errs = append(errs, devID+": "+err.Error())
			continue
		}
		s.emitProgress("clear_bio", fmt.Sprintf("正在清空 %s 上工号 %s 的%s...", dev.Name, req.PIN, lbl), i+1, len(req.DeviceIDs), false)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		params := deviceParams(dev)
		params["pin"] = req.PIN
		params["target"] = req.Target
		err = s.worker.Call(ctx, "clear_user_biometric", params, nil)
		cancel()
		if err != nil {
			results = append(results, models.ActionDeviceResult{DeviceID: dev.ID, DeviceName: dev.Name, OK: false, Error: err.Error()})
			errs = append(errs, dev.Name+": "+err.Error())
		} else {
			okCnt++
			results = append(results, models.ActionDeviceResult{DeviceID: dev.ID, DeviceName: dev.Name, OK: true, Message: "已清空" + lbl})
		}
	}

	s.recordAudit(
		"clear_biometric",
		"精细清理"+lbl,
		fmt.Sprintf("工号 %s @ %d 台设备", req.PIN, len(req.DeviceIDs)),
		fmt.Sprintf("成功 %d / %d 台", okCnt, len(req.DeviceIDs)),
		len(errs) == 0,
		strings.Join(errs, "；"),
		backupPath,
	)
	s.emitProgress("clear_bio", "精细清理完成", len(req.DeviceIDs), len(req.DeviceIDs), true)
	return results, nil
}

func (s *ZKService) DeleteUsers(req models.DeleteUsersRequest) ([]models.ActionDeviceResult, error) {
	if len(req.PINs) == 0 || len(req.DeviceIDs) == 0 {
		return nil, errors.New("请选择要删除的工号和目标设备")
	}
	if err := s.requireConnectedIDs(req.DeviceIDs...); err != nil {
		return nil, err
	}

	// Backup affected cached snapshots before deleting from hardware
	snaps := s.store.LoadSnapshots()
	backupPath, _ := s.store.WriteBackup("delete_users", map[string]any{
		"request":   req,
		"snapshots": snaps,
	})

	results := make([]models.ActionDeviceResult, 0, len(req.DeviceIDs))
	total := len(req.DeviceIDs) * len(req.PINs)
	done := 0
	var allErrs []string

	for _, devID := range req.DeviceIDs {
		dev, err := s.findDevice(devID)
		if err != nil {
			results = append(results, models.ActionDeviceResult{DeviceID: devID, OK: false, Error: err.Error()})
			allErrs = append(allErrs, devID+": "+err.Error())
			continue
		}
		okCount := 0
		var errs []string
		for _, pin := range req.PINs {
			done++
			s.emitProgress("delete", fmt.Sprintf("正在从 %s 删除工号 %s...", dev.Name, pin), done, total, false)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			params := deviceParams(dev)
			params["pin"] = pin
			err := s.worker.Call(ctx, "delete_user", params, nil)
			cancel()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", pin, err))
			} else {
				okCount++
			}
		}
		if len(errs) > 0 {
			allErrs = append(allErrs, fmt.Sprintf("%s(%s)", dev.Name, strings.Join(errs, "; ")))
			results = append(results, models.ActionDeviceResult{
				DeviceID:   dev.ID,
				DeviceName: dev.Name,
				OK:         okCount > 0 && len(errs) == 0,
				Message:    fmt.Sprintf("成功 %d / %d", okCount, len(req.PINs)),
				Error:      strings.Join(errs, "; "),
			})
		} else {
			results = append(results, models.ActionDeviceResult{
				DeviceID:   dev.ID,
				DeviceName: dev.Name,
				OK:         true,
				Message:    fmt.Sprintf("已删除 %d 人", okCount),
			})
		}
	}
	s.recordAudit(
		"delete_user",
		"删除员工",
		fmt.Sprintf("%d 人 @ %d 台设备", len(req.PINs), len(req.DeviceIDs)),
		fmt.Sprintf("工号: %s", strings.Join(req.PINs, ", ")),
		len(allErrs) == 0,
		strings.Join(allErrs, "；"),
		backupPath,
	)
	s.emitProgress("delete", "删除完成（已自动本地备份）", total, total, true)
	return results, nil
}

func (s *ZKService) SyncUsersBetweenDevices(req models.SyncUsersRequest) (models.SyncUsersResponse, error) {
	if req.SourceDeviceID == "" {
		return models.SyncUsersResponse{}, errors.New("请选择源考勤机")
	}
	if len(req.TargetDeviceIDs) == 0 {
		return models.SyncUsersResponse{}, errors.New("请至少选择一台目标考勤机")
	}
	if len(req.PINs) == 0 {
		return models.SyncUsersResponse{}, errors.New("请至少选择一名要同步的员工")
	}

	srcDev, err := s.findDevice(req.SourceDeviceID)
	if err != nil {
		return models.SyncUsersResponse{}, err
	}
	if err := s.requireConnectedDevices(srcDev); err != nil {
		return models.SyncUsersResponse{}, err
	}
	if err := s.requireConnectedIDs(req.TargetDeviceIDs...); err != nil {
		return models.SyncUsersResponse{}, err
	}

	s.emitProgress("sync", fmt.Sprintf("正在从源设备 %s 读取 %d 名员工的完整数据...", srcDev.Name, len(req.PINs)), 1, len(req.TargetDeviceIDs)+1, false)
	readCtx, cancelRead := context.WithTimeout(context.Background(), 120*time.Second)
	readParams := deviceParams(srcDev)
	readParams["pins"] = req.PINs
	readParams["include_fp"] = req.SyncFP
	readParams["include_face"] = req.SyncFace

	var bundleResp struct {
		Bundles []map[string]any `json:"bundles"`
	}
	err = s.worker.CallWithProgress(readCtx, "read_user_bundle", readParams, &bundleResp, func(cur, total int, msg string) {
		s.emitProgress("sync", fmt.Sprintf("[%s] %s", srcDev.Name, msg), cur, total, false)
	})
	cancelRead()
	if err != nil {
		s.emitProgress("sync", "读取源设备失败: "+err.Error(), 0, 1, true)
		return models.SyncUsersResponse{}, err
	}
	if len(bundleResp.Bundles) == 0 {
		s.emitProgress("sync", "源设备上未找到所选员工", 1, 1, true)
		return models.SyncUsersResponse{}, errors.New("源设备上未找到所选工号")
	}

	// Cache downloaded faces locally & save backup
	for _, b := range bundleResp.Bundles {
		pin, _ := b["pin"].(string)
		b64, _ := b["face_base64"].(string)
		if pin != "" && b64 != "" {
			if raw, err := base64.StdEncoding.DecodeString(b64); err == nil {
				_, _ = s.store.SaveFaceJPEG(srcDev.ID, pin, raw)
			}
		}
	}
	backupPath, _ := s.store.WriteBackup("sync_bundles_"+srcDev.ID, map[string]any{
		"request": req,
		"bundles": bundleResp.Bundles,
	})

	resp := models.SyncUsersResponse{
		SourceDeviceID: srcDev.ID,
		RequestedPINs:  len(req.PINs),
		ReadBundles:    len(bundleResp.Bundles),
		BackupPath:     backupPath,
		Results:        make([]models.SyncDeviceResult, 0, len(req.TargetDeviceIDs)),
	}

	for i, targetID := range req.TargetDeviceIDs {
		if targetID == srcDev.ID {
			continue
		}
		tDev, err := s.findDevice(targetID)
		if err != nil {
			resp.Results = append(resp.Results, models.SyncDeviceResult{DeviceID: targetID, Success: false, Error: err.Error()})
			continue
		}
		s.emitProgress("sync", fmt.Sprintf("正在同步 %d 人到 %s...", len(bundleResp.Bundles), tDev.Name), i+2, len(req.TargetDeviceIDs)+1, false)

		wCtx, cancelW := context.WithTimeout(context.Background(), 180*time.Second)
		wParams := deviceParams(tDev)
		wParams["bundles"] = bundleResp.Bundles
		wParams["sync_info"] = req.SyncInfo
		wParams["sync_fp"] = req.SyncFP
		wParams["sync_face"] = req.SyncFace

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
			s.emitProgress("sync", fmt.Sprintf("[%s] %s", tDev.Name, msg), cur, total, false)
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
	var syncDetails []string
	for _, r := range resp.Results {
		if !r.Success {
			allSyncOK = false
			syncDetails = append(syncDetails, fmt.Sprintf("%s: %s", r.DeviceName, r.Error))
		} else {
			syncDetails = append(syncDetails, fmt.Sprintf("%s: 成功 %d 人", r.DeviceName, len(r.Items)))
		}
	}
	s.recordAudit(
		"sync_users",
		"跨机同步",
		fmt.Sprintf("%s → %d 台目标机", srcDev.Name, len(resp.Results)),
		fmt.Sprintf("同步 %d 人（信息=%v 指纹=%v 人脸=%v）", resp.ReadBundles, req.SyncInfo, req.SyncFP, req.SyncFace),
		allSyncOK,
		strings.Join(syncDetails, "；"),
		backupPath,
	)

	s.emitProgress("sync", "跨设备同步完成", len(req.TargetDeviceIDs)+1, len(req.TargetDeviceIDs)+1, true)
	return resp, nil
}

// ==================== P0-3: Smart Diff Reconciliation ====================

func scoreCell(c models.MatrixCell) int {
	if !c.Present {
		return -1
	}
	score := 10
	if c.Name != "" {
		score += 5
	}
	if c.HasFace {
		score += 4
	}
	score += c.FPCount
	if c.Card != "" {
		score += 1
	}
	return score
}

// PreviewSmartSync computes a read-only reconciliation plan without touching any hardware.
// mode: "smart" (pick richest device per user) | "master" (use masterDeviceID as source of truth).
func (s *ZKService) PreviewSmartSync(mode, masterDeviceID string, pins []string, syncInfo, syncFP, syncFace bool) (models.SmartSyncPlan, error) {
	matrix, err := s.GetUserMatrix()
	if err != nil {
		return models.SmartSyncPlan{}, err
	}
	if mode != "master" {
		mode = "smart"
	}
	devNameByID := map[string]string{}
	for _, d := range matrix.Devices {
		devNameByID[d.ID] = d.Name
	}
	if mode == "master" && devNameByID[masterDeviceID] == "" {
		return models.SmartSyncPlan{}, errors.New("请指定有效的主基准考勤机")
	}

	pinFilter := map[string]bool{}
	for _, p := range pins {
		if p != "" {
			pinFilter[p] = true
		}
	}

	plan := models.SmartSyncPlan{
		Mode:           mode,
		MasterDeviceID: masterDeviceID,
		SyncInfo:       syncInfo,
		SyncFP:         syncFP,
		SyncFace:       syncFace,
		Items:          make([]models.SmartSyncPlanItem, 0),
	}

	for _, row := range matrix.Rows {
		if len(pinFilter) > 0 && !pinFilter[row.PIN] {
			continue
		}
		if !row.HasDiff {
			continue
		}

		srcID := ""
		if mode == "master" {
			if c, ok := row.Devices[masterDeviceID]; ok && c.Present {
				srcID = masterDeviceID
			} else {
				plan.Skipped++
				continue
			}
		} else {
			bestScore := -1
			for _, d := range matrix.Devices {
				if c, ok := row.Devices[d.ID]; ok {
					if sc := scoreCell(c); sc > bestScore {
						bestScore = sc
						srcID = d.ID
					}
				}
			}
			if srcID == "" {
				plan.Skipped++
				continue
			}
		}

		srcCell := row.Devices[srcID]
		targetIDs := make([]string, 0)
		targetNames := make([]string, 0)
		reasons := make([]string, 0)
		reasonSet := map[string]bool{}
		addReason := func(r string) {
			if !reasonSet[r] {
				reasonSet[r] = true
				reasons = append(reasons, r)
			}
		}

		for _, d := range matrix.Devices {
			if d.ID == srcID {
				continue
			}
			tc := row.Devices[d.ID]
			need := false
			if !tc.Present {
				need = true
				addReason("补齐缺失设备")
			} else {
				if syncInfo && ((srcCell.Name != "" && tc.Name != srcCell.Name) || tc.Privilege != srcCell.Privilege) {
					need = true
					addReason("对齐姓名/权限")
				}
				if syncFace && srcCell.HasFace && !tc.HasFace {
					need = true
					addReason("补齐人脸")
				}
				if syncFP && srcCell.FPCount > tc.FPCount {
					need = true
					addReason("补齐指纹")
				}
			}
			if need {
				targetIDs = append(targetIDs, d.ID)
				targetNames = append(targetNames, d.Name)
			}
		}

		if len(targetIDs) == 0 {
			plan.Skipped++
			continue
		}

		name := srcCell.Name
		if name == "" {
			name = row.PrimaryName
		}
		plan.Items = append(plan.Items, models.SmartSyncPlanItem{
			PIN:               row.PIN,
			Name:              name,
			SourceDeviceID:    srcID,
			SourceDeviceName:  devNameByID[srcID],
			TargetDeviceIDs:   targetIDs,
			TargetDeviceNames: targetNames,
			Reasons:           reasons,
			SourceFPCount:     srcCell.FPCount,
			SourceHasFace:     srcCell.HasFace,
		})
	}
	plan.TotalAffected = len(plan.Items)
	return plan, nil
}

// ExecuteSmartSync runs a previously previewed SmartSyncPlan grouped by (source, targetSet).
func (s *ZKService) ExecuteSmartSync(plan models.SmartSyncPlan) (models.SmartSyncExecuteResponse, error) {
	if len(plan.Items) == 0 {
		return models.SmartSyncExecuteResponse{}, errors.New("当前补齐计划为空")
	}

	type batchKey struct {
		src     string
		targets string
	}
	type batchGroup struct {
		srcID     string
		targetIDs []string
		pins      []string
	}
	groups := map[batchKey]*batchGroup{}
	order := make([]batchKey, 0)

	for _, it := range plan.Items {
		tSorted := append([]string(nil), it.TargetDeviceIDs...)
		sort.Strings(tSorted)
		k := batchKey{src: it.SourceDeviceID, targets: strings.Join(tSorted, ",")}
		g, ok := groups[k]
		if !ok {
			g = &batchGroup{srcID: it.SourceDeviceID, targetIDs: tSorted}
			groups[k] = g
			order = append(order, k)
		}
		g.pins = append(g.pins, it.PIN)
	}

	out := models.SmartSyncExecuteResponse{
		TotalPlanned: len(plan.Items),
		GroupCount:   len(order),
		AllSuccess:   true,
		BackupPaths:  make([]string, 0, len(order)),
		Results:      make([]models.SyncUsersResponse, 0, len(order)),
	}

	for _, k := range order {
		g := groups[k]
		res, err := s.SyncUsersBetweenDevices(models.SyncUsersRequest{
			SourceDeviceID:  g.srcID,
			TargetDeviceIDs: g.targetIDs,
			PINs:            g.pins,
			SyncInfo:        plan.SyncInfo,
			SyncFP:          plan.SyncFP,
			SyncFace:        plan.SyncFace,
		})
		if err != nil {
			out.AllSuccess = false
			return out, err
		}
		if res.BackupPath != "" {
			out.BackupPaths = append(out.BackupPaths, res.BackupPath)
		}
		for _, r := range res.Results {
			if !r.Success {
				out.AllSuccess = false
			}
		}
		out.Results = append(out.Results, res)
	}

	s.recordAudit(
		"smart_sync",
		"一键智能补齐",
		fmt.Sprintf("%d 人 (%d 组任务)", out.TotalPlanned, out.GroupCount),
		fmt.Sprintf("策略=%s，结果=%v", plan.Mode, out.AllSuccess),
		out.AllSuccess,
		"",
		strings.Join(out.BackupPaths, "; "),
	)
	return out, nil
}
