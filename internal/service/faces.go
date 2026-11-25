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
	"zk_mac/internal/store"
)

// ==================== Face Photo Management ====================

func (s *ZKService) GetFaceGallery() ([]models.FacePhotoItem, error) {
	matrix, err := s.GetUserMatrix()
	if err != nil {
		return nil, err
	}
	devIDs := make([]string, 0, len(matrix.Devices))
	for _, d := range matrix.Devices {
		devIDs = append(devIDs, d.ID)
	}

	items := make([]models.FacePhotoItem, 0)
	for _, row := range matrix.Rows {
		faceDevs := []string{}
		for _, d := range matrix.Devices {
			if cell, ok := row.Devices[d.ID]; ok && cell.HasFace {
				faceDevs = append(faceDevs, d.ID)
			}
		}
		localPath, srcDev, size := s.store.FindLocalFace(row.PIN, devIDs...)
		if len(faceDevs) == 0 && localPath == "" {
			continue
		}
		// Return lightweight metadata only; individual JPEG data URLs are lazy-loaded
		// via GetLocalFaceDataURL when the user explicitly reveals a photo.
		items = append(items, models.FacePhotoItem{
			PIN:        row.PIN,
			Name:       row.PrimaryName,
			Devices:    faceDevs,
			LocalPath:  localPath,
			DataURL:    "",
			SizeBytes:  size,
			CachedFrom: srcDev,
		})
	}
	return items, nil
}

// GetLocalFaceDataURL lazily loads a single locally cached face JPEG as a data URL
// without contacting any attendance terminal.
func (s *ZKService) GetLocalFaceDataURL(pin string) (string, error) {
	pin = strings.TrimSpace(pin)
	if err := store.ValidatePIN(pin); err != nil {
		return "", err
	}
	devs, _ := s.store.LoadDevices()
	devIDs := make([]string, 0, len(devs))
	for _, d := range devs {
		if d.Enabled {
			devIDs = append(devIDs, d.ID)
		}
	}
	localPath, _, _ := s.store.FindLocalFace(pin, devIDs...)
	if localPath == "" {
		return "", errors.New("本地暂未缓存该员工人脸照片")
	}
	return store.ReadJPEGDataURL(localPath)
}

func (s *ZKService) FetchFacePhoto(deviceID, pin string) (models.FacePhotoItem, error) {
	pin = strings.TrimSpace(pin)
	if err := store.ValidatePIN(pin); err != nil {
		return models.FacePhotoItem{}, err
	}

	// If deviceID empty, check cached local first
	if deviceID == "" {
		if path, src, sz := s.store.FindLocalFace(pin); path != "" {
			url, err := store.ReadJPEGDataURL(path)
			if err == nil {
				return models.FacePhotoItem{
					PIN:        pin,
					LocalPath:  path,
					DataURL:    url,
					SizeBytes:  sz,
					CachedFrom: src,
				}, nil
			}
		}
		return models.FacePhotoItem{}, errors.New("请指定考勤机下载人脸")
	}

	dev, err := s.findDevice(deviceID)
	if err != nil {
		return models.FacePhotoItem{}, err
	}
	if err := s.requireConnectedDevices(dev); err != nil {
		return models.FacePhotoItem{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	params := deviceParams(dev)
	params["pin"] = pin

	var resp struct {
		PIN    string `json:"pin"`
		Base64 string `json:"base64"`
		Size   int64  `json:"size"`
	}
	if err := s.worker.Call(ctx, "get_face_photo", params, &resp); err != nil {
		return models.FacePhotoItem{}, err
	}

	raw, err := base64.StdEncoding.DecodeString(resp.Base64)
	if err != nil {
		return models.FacePhotoItem{}, err
	}
	localPath, err := s.store.SaveFaceJPEG(dev.ID, pin, raw)
	if err != nil {
		return models.FacePhotoItem{}, err
	}
	return models.FacePhotoItem{
		PIN:        pin,
		Devices:    []string{dev.ID},
		LocalPath:  localPath,
		DataURL:    "data:image/jpeg;base64," + resp.Base64,
		SizeBytes:  int64(len(raw)),
		CachedFrom: dev.ID,
	}, nil
}

func (s *ZKService) DownloadAllFaces(deviceID string, pins []string) (models.DownloadFacesResponse, error) {
	dev, err := s.findDevice(deviceID)
	if err != nil {
		return models.DownloadFacesResponse{}, err
	}
	if err := s.requireConnectedDevices(dev); err != nil {
		return models.DownloadFacesResponse{}, err
	}
	outDir := s.store.DeviceFaceDir(dev.ID)
	s.emitProgress("faces", fmt.Sprintf("正在从 %s 批量下载人脸照片...", dev.Name), 1, 2, false)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	params := deviceParams(dev)
	params["out_dir"] = outDir
	if len(pins) > 0 {
		params["pins"] = pins
	}

	var resp struct {
		Downloaded []struct {
			PIN  string `json:"pin"`
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"downloaded"`
		Failed []struct {
			PIN   string `json:"pin"`
			Error string `json:"error"`
		} `json:"failed"`
	}
	if err := s.worker.CallWithProgress(ctx, "download_faces", params, &resp, func(cur, total int, msg string) {
		s.emitProgress("faces", fmt.Sprintf("[%s] %s", dev.Name, msg), cur, total, false)
	}); err != nil {
		s.emitProgress("faces", "批量下载失败: "+err.Error(), 0, 1, true)
		return models.DownloadFacesResponse{}, err
	}

	failedPINs := make([]string, 0, len(resp.Failed))
	for _, f := range resp.Failed {
		failedPINs = append(failedPINs, fmt.Sprintf("%s(%s)", f.PIN, f.Error))
	}
	s.recordAudit(
		"download_faces",
		"批量下载人脸",
		dev.Name,
		fmt.Sprintf("成功 %d 张，失败 %d 张", len(resp.Downloaded), len(resp.Failed)),
		len(resp.Failed) == 0,
		strings.Join(failedPINs, "；"),
		"",
	)
	s.emitProgress("faces", fmt.Sprintf("已下载 %d 张人脸照片", len(resp.Downloaded)), 2, 2, true)
	return models.DownloadFacesResponse{
		DeviceID:   dev.ID,
		Downloaded: len(resp.Downloaded),
		Failed:     len(resp.Failed),
		FailedPINs: failedPINs,
		SaveDir:    outDir,
	}, nil
}

// PickLocalImage opens a native macOS file picker to choose a face photo.
func (s *ZKService) PickLocalImage() (string, error) {
	if s.app == nil {
		return "", errors.New("应用上下文未就绪")
	}
	path, err := s.app.Dialog.OpenFile().
		SetTitle("选择员工正脸照片 (JPG/PNG)").
		AddFilter("图片文件", "*.jpg;*.jpeg;*.png;*.JPG;*.JPEG;*.PNG").
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return path, nil
}

// PreviewLocalImage normalizes a local JPG/PNG and returns a data URL for UI preview.
func (s *ZKService) PreviewLocalImage(path string) (string, error) {
	norm, err := exporter.NormalizeFaceJPEG(path)
	if err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(norm), nil
}

// PickLocalFolder opens a native macOS folder picker for batch face upload.
func (s *ZKService) PickLocalFolder() (string, error) {
	if s.app == nil {
		return "", errors.New("应用上下文未就绪")
	}
	dir, err := s.app.Dialog.OpenFile().
		SetTitle("选择包含员工照片（如 103.jpg / 103_姓名.jpg）的文件夹").
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return dir, nil
}

// ScanFaceFolder scans a local folder for JPG/PNG photos and matches leading PINs against cached staff.
func (s *ZKService) ScanFaceFolder(dirPath string) ([]models.BatchFaceCandidate, error) {
	dirPath = strings.TrimSpace(dirPath)
	if dirPath == "" {
		return nil, errors.New("文件夹路径为空")
	}
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	knownNames := map[string]string{}
	for _, snap := range s.store.LoadSnapshots() {
		for _, u := range snap.Users {
			if u.PIN != "" {
				if knownNames[u.PIN] == "" || u.Name != "" {
					knownNames[u.PIN] = u.Name
				}
			}
		}
	}

	out := make([]models.BatchFaceCandidate, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") && !strings.HasSuffix(lower, ".png") {
			continue
		}
		pin := exporter.ExtractPINFromFilename(e.Name())
		if pin == "" {
			continue
		}
		info, _ := e.Info()
		var sz int64
		if info != nil {
			sz = info.Size()
		}
		name, exists := knownNames[pin]
		out = append(out, models.BatchFaceCandidate{
			PIN:          pin,
			Name:         name,
			FileName:     e.Name(),
			FilePath:     filepath.Join(dirPath, e.Name()),
			SizeBytes:    sz,
			ExistsInDevs: exists,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ni, ei := strconv.Atoi(out[i].PIN)
		nj, ej := strconv.Atoi(out[j].PIN)
		if ei == nil && ej == nil {
			return ni < nj
		}
		return out[i].PIN < out[j].PIN
	})
	return out, nil
}

// BatchUploadFaceFolder normalizes and uploads multiple local face photos to selected devices with per-file progress.
func (s *ZKService) BatchUploadFaceFolder(items []models.BatchFaceCandidate, targetDeviceIDs []string) (models.BatchFaceUploadResponse, error) {
	if len(items) == 0 {
		return models.BatchFaceUploadResponse{}, errors.New("请至少选择一张照片")
	}
	if len(targetDeviceIDs) == 0 {
		return models.BatchFaceUploadResponse{}, errors.New("请至少选择一台目标考勤机")
	}
	if err := s.requireConnectedIDs(targetDeviceIDs...); err != nil {
		return models.BatchFaceUploadResponse{}, err
	}

	devs := make([]models.DeviceConfig, 0, len(targetDeviceIDs))
	for _, id := range targetDeviceIDs {
		if d, err := s.findDevice(id); err == nil {
			devs = append(devs, d)
		}
	}
	if len(devs) == 0 {
		return models.BatchFaceUploadResponse{}, errors.New("未找到有效目标设备")
	}

	resp := models.BatchFaceUploadResponse{
		TotalFiles: len(items),
		Items:      make([]models.BatchFaceUploadItem, 0, len(items)),
	}
	totalSteps := len(items) * len(devs)
	step := 0

	for _, it := range items {
		itemRes := models.BatchFaceUploadItem{
			PIN:      it.PIN,
			FileName: it.FileName,
		}
		normalized, err := exporter.NormalizeFaceJPEG(it.FilePath)
		if err != nil {
			itemRes.FailCnt = len(devs)
			itemRes.Errors = []string{"图片处理失败: " + err.Error()}
			resp.Items = append(resp.Items, itemRes)
			step += len(devs)
			continue
		}
		cachedPath, err := s.store.SaveFaceJPEG("custom", it.PIN, normalized)
		if err != nil {
			itemRes.FailCnt = len(devs)
			itemRes.Errors = []string{"缓存照片失败: " + err.Error()}
			resp.Items = append(resp.Items, itemRes)
			step += len(devs)
			continue
		}

		for _, dev := range devs {
			step++
			s.emitProgress("face_batch", fmt.Sprintf("批量上传人脸 %s (%s) → %s (%d/%d)", it.PIN, it.FileName, dev.Name, step, totalSteps), step, totalSteps, false)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			params := deviceParams(dev)
			params["pin"] = it.PIN
			params["file_path"] = cachedPath
			err := s.worker.Call(ctx, "upload_face_photo", params, nil)
			cancel()
			if err != nil {
				itemRes.FailCnt++
				itemRes.Errors = append(itemRes.Errors, fmt.Sprintf("%s: %v", dev.Name, err))
			} else {
				itemRes.SuccessCnt++
				_, _ = s.store.SaveFaceJPEG(dev.ID, it.PIN, normalized)
			}
		}
		if itemRes.FailCnt == 0 && itemRes.SuccessCnt == len(devs) {
			resp.SuccessAll++
		}
		resp.Items = append(resp.Items, itemRes)
	}

	s.recordAudit(
		"batch_upload_faces",
		"批量上传人脸",
		fmt.Sprintf("%d 张照片 → %d 台设备", len(items), len(devs)),
		fmt.Sprintf("全部成功 %d / %d 张", resp.SuccessAll, resp.TotalFiles),
		resp.SuccessAll == resp.TotalFiles,
		"",
		"",
	)
	s.emitProgress("face_batch", fmt.Sprintf("批量上传完成（%d/%d 张全部成功）", resp.SuccessAll, resp.TotalFiles), totalSteps, totalSteps, true)
	return resp, nil
}

// ExportFacePhotoToDisk copies a cached face JPEG to a user-chosen path via native Save dialog.
func (s *ZKService) ExportFacePhotoToDisk(pin string) (string, error) {
	if s.app == nil {
		return "", errors.New("应用上下文未就绪")
	}
	localPath, _, _ := s.store.FindLocalFace(pin)
	if localPath == "" {
		return "", fmt.Errorf("本地尚未缓存工号 %s 的人脸照片，请先从考勤机下载", pin)
	}
	dest, err := s.app.Dialog.SaveFile().
		SetMessage("导出人脸照片").
		SetFilename(fmt.Sprintf("%s.jpg", pin)).
		PromptForSingleSelection()
	if err != nil || dest == "" {
		return "", err
	}
	raw, err := os.ReadFile(localPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, raw, 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

// UploadFacePhoto normalizes a local JPG/PNG (or cached face) and uploads it to selected devices.
func (s *ZKService) UploadFacePhoto(pin, sourcePath string, targetDeviceIDs []string) ([]models.ActionDeviceResult, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return nil, errors.New("工号不能为空")
	}
	if len(targetDeviceIDs) == 0 {
		return nil, errors.New("请至少选择一台目标考勤机")
	}
	if err := s.requireConnectedIDs(targetDeviceIDs...); err != nil {
		return nil, err
	}

	if sourcePath == "" {
		localPath, _, _ := s.store.FindLocalFace(pin)
		if localPath == "" {
			return nil, errors.New("请选择本地照片或先从考勤机下载人脸")
		}
		sourcePath = localPath
	}

	normalized, err := exporter.NormalizeFaceJPEG(sourcePath)
	if err != nil {
		return nil, err
	}
	cachedPath, err := s.store.SaveFaceJPEG("custom", pin, normalized)
	if err != nil {
		return nil, err
	}

	results := make([]models.ActionDeviceResult, 0, len(targetDeviceIDs))
	for i, devID := range targetDeviceIDs {
		dev, err := s.findDevice(devID)
		if err != nil {
			results = append(results, models.ActionDeviceResult{DeviceID: devID, OK: false, Error: err.Error()})
			continue
		}
		s.emitProgress("face_upload", fmt.Sprintf("正在上传 %s.jpg 到 %s...", pin, dev.Name), i+1, len(targetDeviceIDs), false)

		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		params := deviceParams(dev)
		params["pin"] = pin
		params["file_path"] = cachedPath
		err = s.worker.Call(ctx, "upload_face_photo", params, nil)
		cancel()

		if err != nil {
			results = append(results, models.ActionDeviceResult{DeviceID: dev.ID, DeviceName: dev.Name, OK: false, Error: err.Error()})
		} else {
			_, _ = s.store.SaveFaceJPEG(dev.ID, pin, normalized)
			results = append(results, models.ActionDeviceResult{DeviceID: dev.ID, DeviceName: dev.Name, OK: true, Message: "人脸上传并注册成功"})
		}
	}
	okFaceCnt := 0
	var faceErrs []string
	for _, r := range results {
		if r.OK {
			okFaceCnt++
		} else {
			faceErrs = append(faceErrs, r.DeviceName+": "+r.Error)
		}
	}
	s.recordAudit(
		"upload_face",
		"上传同步人脸",
		fmt.Sprintf("工号 %s → %d 台设备", pin, len(targetDeviceIDs)),
		fmt.Sprintf("成功 %d / %d 台", okFaceCnt, len(targetDeviceIDs)),
		len(faceErrs) == 0,
		strings.Join(faceErrs, "；"),
		"",
	)
	s.emitProgress("face_upload", "人脸上传同步完成", len(targetDeviceIDs), len(targetDeviceIDs), true)
	return results, nil
}

func (s *ZKService) DeleteFacePhoto(pin string, deviceIDs []string) ([]models.ActionDeviceResult, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" || len(deviceIDs) == 0 {
		return nil, errors.New("请指定工号和设备")
	}
	if err := s.requireConnectedIDs(deviceIDs...); err != nil {
		return nil, err
	}
	results := make([]models.ActionDeviceResult, 0, len(deviceIDs))
	for i, devID := range deviceIDs {
		dev, err := s.findDevice(devID)
		if err != nil {
			results = append(results, models.ActionDeviceResult{DeviceID: devID, OK: false, Error: err.Error()})
			continue
		}
		s.emitProgress("face_delete", fmt.Sprintf("正在从 %s 删除 %s 人脸...", dev.Name, pin), i+1, len(deviceIDs), false)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		params := deviceParams(dev)
		params["pin"] = pin
		err = s.worker.Call(ctx, "delete_face_photo", params, nil)
		cancel()
		if err != nil {
			results = append(results, models.ActionDeviceResult{DeviceID: dev.ID, DeviceName: dev.Name, OK: false, Error: err.Error()})
		} else {
			results = append(results, models.ActionDeviceResult{DeviceID: dev.ID, DeviceName: dev.Name, OK: true, Message: "已删除设备人脸"})
		}
	}
	s.recordAudit(
		"delete_face",
		"删除设备人脸",
		fmt.Sprintf("工号 %s @ %d 台设备", pin, len(deviceIDs)),
		fmt.Sprintf("执行完成 (%d 台)", len(deviceIDs)),
		true,
		"",
		"",
	)
	s.emitProgress("face_delete", "人脸删除完成", len(deviceIDs), len(deviceIDs), true)
	return results, nil
}
