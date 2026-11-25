import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowLeftRight,
  ArrowRight,
  Camera,
  Download,
  Eye,
  EyeOff,
  FolderUp,
  Loader2,
  RefreshCw,
  Save,
  Search,
  Trash2,
  Upload,
} from 'lucide-react';
import {
  BatchFaceCandidate,
  BatchFaceUploadResponse,
  DeviceConfig,
  DeviceStatus,
  FacePhotoItem,
} from '../../bindings/zk_mac/internal/models';
import * as ZK from '../../bindings/zk_mac/internal/service/zkservice';
import { ModalDialog } from '../components/ModalDialog';

interface FacesViewProps {
  devices: DeviceConfig[];
  statuses: Record<string, DeviceStatus | undefined>;
  gallery: FacePhotoItem[];
  busy: boolean;
  onReloadGallery: () => Promise<void>;
  onAuditChanged: () => void;
  onGoToDevices: () => void;
  onNotify: (type: 'info' | 'success' | 'error', text: string) => void;
  setBusy: (v: boolean) => void;
}

export function FacesView({
  devices,
  statuses,
  gallery,
  busy,
  onReloadGallery,
  onAuditChanged,
  onGoToDevices,
  onNotify,
  setBusy,
}: FacesViewProps) {
  const enabledDevices = useMemo(() => devices.filter((d) => d.enabled), [devices]);
  const connectedDeviceIds = useMemo(
    () => enabledDevices.filter((d) => statuses[d.id]?.connected).map((d) => d.id),
    [enabledDevices, statuses]
  );

  const ensureConnectedOrNotify = (ids: string[]): boolean => {
    const missing = ids
      .map((id) => enabledDevices.find((d) => d.id === id))
      .filter((d): d is DeviceConfig => !!d && !statuses[d.id]?.connected)
      .map((d) => d.name);
    if (missing.length > 0) {
      onNotify('error', `考勤机「${missing.join('、')}」尚未连接，请先在「设备总览」中手动连接后再操作`);
      return false;
    }
    return true;
  };
  const [downloadDev, setDownloadDev] = useState<string>(() => enabledDevices[0]?.id || '');
  const [query, setQuery] = useState('');
  const [showAllFaces, setShowAllFaces] = useState(false);
  const [revealedPins, setRevealedPins] = useState<Record<string, boolean>>({});
  const [photoUrls, setPhotoUrls] = useState<Record<string, string>>({});
  const [loadingPins, setLoadingPins] = useState<Record<string, boolean>>({});
  const inFlightPinsRef = useRef<Set<string>>(new Set());

  const loadLocalPhoto = useCallback(async (pin: string) => {
    if (!pin || inFlightPinsRef.current.has(pin)) return;
    inFlightPinsRef.current.add(pin);
    setLoadingPins((prev) => ({ ...prev, [pin]: true }));
    try {
      const url = await ZK.GetLocalFaceDataURL(pin);
      if (url) {
        setPhotoUrls((prev) => ({ ...prev, [pin]: url }));
      }
    } catch {
      // Ignore missing/unreadable local cache; user can re-download from device.
    } finally {
      inFlightPinsRef.current.delete(pin);
      setLoadingPins((prev) => {
        if (!prev[pin]) return prev;
        const next = { ...prev };
        delete next[pin];
        return next;
      });
    }
  }, []);

  const toggleRevealPin = (pin: string) => {
    const nextOpen = !revealedPins[pin];
    setRevealedPins((prev) => ({ ...prev, [pin]: nextOpen }));
    if (nextOpen && !photoUrls[pin]) {
      void loadLocalPhoto(pin);
    }
  };

  const hasAnyRevealed = showAllFaces || Object.values(revealedPins).some(Boolean);

  const handleToggleShowAll = () => {
    if (showAllFaces) {
      setShowAllFaces(false);
      setRevealedPins({});
    } else {
      setShowAllFaces(true);
    }
  };

  // Upload modal
  const [uploadOpen, setUploadOpen] = useState(false);
  const [uploadPin, setUploadPin] = useState('');
  const [uploadPath, setUploadPath] = useState('');
  const [uploadTargets, setUploadTargets] = useState<string[]>(() =>
    enabledDevices.map((d) => d.id)
  );

  // Delete face confirm
  const [deleteTarget, setDeleteTarget] = useState<FacePhotoItem | null>(null);
  const [deleteDevIds, setDeleteDevIds] = useState<string[]>([]);

  // Explicit target-device face sync modal (respects per-device independent rosters)
  const [syncTarget, setSyncTarget] = useState<FacePhotoItem | null>(null);
  const [syncDevIds, setSyncDevIds] = useState<string[]>([]);

  // P1-7 Folder batch upload modal
  const [batchOpen, setBatchOpen] = useState(false);
  const [batchDir, setBatchDir] = useState('');
  const [batchCandidates, setBatchCandidates] = useState<BatchFaceCandidate[]>([]);
  const [batchSelectedFiles, setBatchSelectedFiles] = useState<string[]>([]);
  const [batchTargets, setBatchTargets] = useState<string[]>(() =>
    enabledDevices.map((d) => d.id)
  );
  const [batchResult, setBatchResult] = useState<BatchFaceUploadResponse | null>(null);

  // Keep device selectors synchronized when devices load asynchronously or change.
  useEffect(() => {
    const validIds = new Set(enabledDevices.map((d) => d.id));
    setDownloadDev((prev) => (prev && validIds.has(prev) ? prev : enabledDevices[0]?.id || ''));
    setUploadTargets((prev) => {
      const kept = prev.filter((id) => validIds.has(id));
      return kept.length > 0 ? kept : enabledDevices.map((d) => d.id);
    });
    setBatchTargets((prev) => {
      const kept = prev.filter((id) => validIds.has(id));
      return kept.length > 0 ? kept : enabledDevices.map((d) => d.id);
    });
  }, [enabledDevices]);

  const handleOpenFolderBatch = async () => {
    try {
      const dir = await ZK.PickLocalFolder();
      if (!dir) return;
      const list = (await ZK.ScanFaceFolder(dir)) || [];
      setBatchDir(dir);
      setBatchCandidates(list);
      setBatchSelectedFiles(list.map((x) => x.filePath));
      setBatchTargets(connectedDeviceIds.length > 0 ? connectedDeviceIds : enabledDevices.map((d) => d.id));
      setBatchResult(null);
      setBatchOpen(true);
      if (list.length === 0) {
        onNotify('info', '该文件夹内未识别到以数字工号开头的 JPG/PNG 照片（如 103.jpg 或 103_姓名.jpg）');
      }
    } catch (err: any) {
      onNotify('error', `扫描文件夹失败：${err?.message || err}`);
    }
  };

  const handleRunBatchUpload = async () => {
    const picked = batchCandidates.filter((c) => batchSelectedFiles.includes(c.filePath));
    if (picked.length === 0 || batchTargets.length === 0) {
      onNotify('error', '请至少选择一张照片和一台目标设备');
      return;
    }
    if (!ensureConnectedOrNotify(batchTargets)) return;
    setBusy(true);
    setBatchResult(null);
    try {
      const res = await ZK.BatchUploadFaceFolder(picked, batchTargets);
      setBatchResult(res);
      onAuditChanged();
      onNotify(
        res.successAll === res.totalFiles ? 'success' : 'info',
        `文件夹批量上传完成：${res.successAll} / ${res.totalFiles} 张全部成功`
      );
      await ZK.RefreshUsers(batchTargets, false);
      await onReloadGallery();
    } catch (err: any) {
      onNotify('error', `批量上传失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (gallery || []).filter((item) => {
      if (!q) return true;
      return (
        item.pin.toLowerCase().includes(q) ||
        (item.name || '').toLowerCase().includes(q)
      );
    });
  }, [gallery, query]);

  // Lazily load visible cached photos in small batches only when "show all" is enabled.
  useEffect(() => {
    if (!showAllFaces) return;
    let cancelled = false;
    const pending = filtered
      .filter((item) => !!item.localPath && !item.dataUrl && !photoUrls[item.pin])
      .map((item) => item.pin);
    if (pending.length === 0) return;

    (async () => {
      const BATCH_SIZE = 8;
      for (let i = 0; i < pending.length && !cancelled; i += BATCH_SIZE) {
        const chunk = pending.slice(i, i + BATCH_SIZE);
        await Promise.all(chunk.map((pin) => loadLocalPhoto(pin)));
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [showAllFaces, filtered, photoUrls, loadLocalPhoto]);

  const handleRefreshGallery = async () => {
    setPhotoUrls({});
    await onReloadGallery();
  };

  const handleBatchDownload = async () => {
    const devId = downloadDev || enabledDevices[0]?.id;
    if (!devId) return;
    if (!ensureConnectedOrNotify([devId])) return;
    setBusy(true);
    try {
      const resp = await ZK.DownloadAllFaces(devId, []);
      setPhotoUrls({});
      await onReloadGallery();
      onAuditChanged();
      onNotify(
        'success',
        `已从设备 ${devId} 下载 ${resp.downloaded} 张人脸照片到本地（失败 ${resp.failed} 张）`
      );
    } catch (err: any) {
      onNotify('error', `批量下载失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const handleDownloadOne = async (item: FacePhotoItem) => {
    const srcDev = item.devices?.[0] || enabledDevices[0]?.id;
    if (!srcDev) return;
    if (!ensureConnectedOrNotify([srcDev])) return;
    setBusy(true);
    try {
      const fetched = await ZK.FetchFacePhoto(srcDev, item.pin);
      if (fetched?.dataUrl) {
        setPhotoUrls((prev) => ({ ...prev, [item.pin]: fetched.dataUrl }));
      }
      await onReloadGallery();
      setRevealedPins((prev) => ({ ...prev, [item.pin]: true }));
      onNotify('success', `已下载工号 ${item.pin} 人脸照片`);
    } catch (err: any) {
      onNotify('error', `下载失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const openSyncModal = (item: FacePhotoItem) => {
    const existing = new Set(item.devices || []);
    const defaultConnected = connectedDeviceIds.filter((id) => !existing.has(id));
    setSyncTarget(item);
    setSyncDevIds(defaultConnected);
  };

  const handleConfirmSyncFace = async () => {
    if (!syncTarget || syncDevIds.length === 0) {
      onNotify('error', '请至少选择一台目标考勤机');
      return;
    }
    const targets = [...syncDevIds];
    const neededDevs = !syncTarget.localPath && syncTarget.devices?.length
      ? [syncTarget.devices[0], ...targets]
      : targets;
    if (!ensureConnectedOrNotify(Array.from(new Set(neededDevs)))) return;

    const item = syncTarget;
    setSyncTarget(null);
    setBusy(true);
    try {
      // Ensure photo is cached locally first
      if (!item.localPath && item.devices?.length) {
        await ZK.FetchFacePhoto(item.devices[0], item.pin);
      }
      const res = (await ZK.UploadFacePhoto(item.pin, '', targets)) || [];
      const okCnt = res.filter((r) => r.ok).length;
      const errs = res.filter((r) => !r.ok);
      if (errs.length > 0) {
        onNotify('error', `同步人脸部分失败：${errs.map((e) => `${e.deviceName}:${e.error}`).join('；')}`);
      } else {
        onNotify('success', `已将工号 ${item.pin} 人脸同步到 ${okCnt} 台设备`);
      }
      await ZK.RefreshUsers(targets, false);
      await onReloadGallery();
      onAuditChanged();
    } catch (err: any) {
      onNotify('error', `同步人脸失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const handleExportOne = async (pin: string) => {
    try {
      const saved = await ZK.ExportFacePhotoToDisk(pin);
      if (saved) {
        onNotify('success', `已导出人脸原图到：${saved}`);
      }
    } catch (err: any) {
      onNotify('error', `导出失败：${err?.message || err}`);
    }
  };

  const openUploadModal = (pin = '') => {
    setUploadPin(pin);
    setUploadPath('');
    setUploadTargets(connectedDeviceIds.length > 0 ? connectedDeviceIds : enabledDevices.map((d) => d.id));
    setUploadOpen(true);
  };

  const handlePickFile = async () => {
    try {
      const p = await ZK.PickLocalImage();
      if (p) setUploadPath(p);
    } catch (err: any) {
      onNotify('error', String(err?.message || err));
    }
  };

  const handleSubmitUpload = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!uploadPin.trim() || !uploadPath) {
      onNotify('error', '请填写工号并选择本地照片');
      return;
    }
    if (!ensureConnectedOrNotify(uploadTargets)) return;
    setBusy(true);
    try {
      const res = (await ZK.UploadFacePhoto(uploadPin.trim(), uploadPath, uploadTargets)) || [];
      const okCnt = res.filter((r) => r.ok).length;
      const errs = res.filter((r) => !r.ok);
      if (errs.length > 0) {
        onNotify('error', `上传部分失败：${errs.map((x) => `${x.deviceName}:${x.error}`).join('；')}`);
      } else {
        onNotify('success', `工号 ${uploadPin.trim()} 人脸已上传并注册到 ${okCnt} 台考勤机`);
        setUploadOpen(false);
      }
      await ZK.RefreshUsers(uploadTargets, false);
      setPhotoUrls((prev) => {
        const next = { ...prev };
        delete next[uploadPin.trim()];
        return next;
      });
      await onReloadGallery();
      onAuditChanged();
    } catch (err: any) {
      onNotify('error', `上传失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const handleDeleteFace = async () => {
    if (!deleteTarget || deleteDevIds.length === 0) return;
    if (!ensureConnectedOrNotify(deleteDevIds)) return;
    setBusy(true);
    try {
      await ZK.DeleteFacePhoto(deleteTarget.pin, deleteDevIds);
      onNotify('success', `已从所选设备删除工号 ${deleteTarget.pin} 的人脸`);
      const devs = [...deleteDevIds];
      setDeleteTarget(null);
      await ZK.RefreshUsers(devs, false);
      await onReloadGallery();
      onAuditChanged();
    } catch (err: any) {
      onNotify('error', `删除人脸失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      {connectedDeviceIds.length === 0 && (
        <div className="notice-bar info" role="status">
          <span>
            当前没有已连接的考勤机：可正常浏览/导出本地缓存人脸，如需从考勤机下载或上传人脸，请先在「设备总览」中手动连接。
          </span>
          <button type="button" className="btn btn-primary btn-sm" onClick={onGoToDevices}>
            去设备总览连接 <ArrowRight size={13} />
          </button>
        </div>
      )}
      <div className="toolbar-row" style={{ justifyContent: 'space-between' }}>
        <div className="toolbar-row">
          <select
            className="select"
            aria-label="选择下载源考勤机"
            value={downloadDev}
            onChange={(e) => setDownloadDev(e.target.value)}
          >
            {enabledDevices.map((d) => (
              <option key={d.id} value={d.id}>
                从 {d.name} 下载
              </option>
            ))}
          </select>
          <button
            type="button"
            className="btn btn-primary"
            disabled={busy}
            onClick={handleBatchDownload}
          >
            <Download size={14} /> 批量下载该机全部人脸
          </button>
          <button type="button" className="btn" disabled={busy} onClick={() => openUploadModal('')}>
            <Upload size={14} /> 单张上传照片
          </button>
          <button
            type="button"
            className="btn"
            disabled={busy}
            title="选择包含 工号.jpg 或 工号_姓名.jpg 的文件夹一键批量上传"
            onClick={handleOpenFolderBatch}
          >
            <FolderUp size={14} /> 文件夹批量上传
          </button>
          <button
            type="button"
            className="btn"
            onClick={handleToggleShowAll}
            title={showAllFaces ? '隐藏全部人脸照片' : '一次性显示全部已缓存人脸照片'}
          >
            {showAllFaces ? <EyeOff size={14} /> : <Eye size={14} />}
            {showAllFaces ? '全部隐藏' : '全部显示'}
          </button>
          {!showAllFaces && hasAnyRevealed && (
            <button
              type="button"
              className="btn btn-ghost"
              onClick={() => setRevealedPins({})}
              title="收起当前已点开的人脸照片"
            >
              <EyeOff size={14} /> 收起已展开
            </button>
          )}
          <button type="button" className="btn btn-ghost" disabled={busy} onClick={handleRefreshGallery}>
            <RefreshCw size={14} />
          </button>
        </div>

        <div className="search-box">
          <Search size={14} />
          <input
            className="input"
            placeholder="搜索工号或姓名..."
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
      </div>

      {filtered.length === 0 ? (
        <div className="card empty-state">
          <Camera size={32} strokeWidth={1.5} />
          <div>暂无人脸索引记录。请先在“人员同步”刷新员工，或点击上方“批量下载该机全部人脸”。</div>
        </div>
      ) : (
        <div className="face-grid">
          {filtered.map((item) => {
            const isRevealed = showAllFaces || !!revealedPins[item.pin];
            const imgSrc = item.dataUrl || photoUrls[item.pin] || '';
            const hasLocalFace = Boolean(item.localPath || imgSrc);
            const isLoadingImg = !!loadingPins[item.pin];
            return (
              <div key={item.pin} className="face-card">
                <div className="face-thumb">
                  {hasLocalFace ? (
                    isRevealed ? (
                      imgSrc ? (
                        <button
                          type="button"
                          onClick={() => {
                            if (!showAllFaces) toggleRevealPin(item.pin);
                          }}
                          title={showAllFaces ? '点击上方「全部隐藏」可隐藏照片' : '点击隐藏照片'}
                          style={{
                            padding: 0,
                            border: 0,
                            background: 'transparent',
                            width: '100%',
                            height: '100%',
                            cursor: showAllFaces ? 'default' : 'pointer',
                            display: 'block',
                          }}
                        >
                          <img src={imgSrc} alt={`${item.name || item.pin} 人脸照片`} />
                        </button>
                      ) : (
                        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6, color: 'var(--text-muted)', fontSize: 12 }}>
                          <Loader2 size={16} className="spin" />
                          <span>{isLoadingImg ? '加载照片...' : '准备显示...'}</span>
                        </div>
                      )
                    ) : (
                      <button
                        type="button"
                        className="btn btn-sm"
                        onClick={() => toggleRevealPin(item.pin)}
                        style={{
                          flexDirection: 'column',
                          height: 'auto',
                          padding: '10px 14px',
                          gap: 6,
                        }}
                      >
                        <Eye size={16} />
                        <span>点击查看人脸</span>
                      </button>
                    )
                  ) : (
                    <button
                      type="button"
                      className="btn btn-sm"
                      disabled={busy}
                      onClick={() => handleDownloadOne(item)}
                    >
                      <Download size={12} /> 点击下载预览
                    </button>
                  )}
                </div>
                <div className="face-card-body">
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                    <strong style={{ fontSize: 13 }}>{item.name || '-'}</strong>
                    <span className="mono" style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>
                      #{item.pin}
                    </span>
                  </div>
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                    {(item.devices || []).length > 0 ? (
                      (item.devices || []).map((id) => (
                        <span key={id} className="badge badge-ok">
                          {id}
                        </span>
                      ))
                    ) : (
                      <span className="badge badge-muted">仅本地缓存</span>
                    )}
                  </div>
                  <div className="toolbar-row" style={{ gap: 4, marginTop: 2 }}>
                    <button
                      type="button"
                      className="btn btn-sm"
                      style={{ flex: 1 }}
                      disabled={busy}
                      title="手动选择目标考勤机同步人脸照片"
                      onClick={() => openSyncModal(item)}
                    >
                      <ArrowLeftRight size={11} /> 同步到设备
                    </button>
                    {hasLocalFace && (
                      <button
                        type="button"
                        className="btn btn-sm btn-ghost"
                        title="导出原图到电脑"
                        onClick={() => handleExportOne(item.pin)}
                      >
                        <Save size={12} />
                      </button>
                    )}
                    <button
                      type="button"
                      className="btn btn-sm btn-ghost"
                      title="替换为本地新照片"
                      onClick={() => openUploadModal(item.pin)}
                    >
                      <Upload size={12} />
                    </button>
                    <button
                      type="button"
                      className="btn btn-sm btn-ghost"
                      style={{ color: 'var(--danger)' }}
                      title="删除设备人脸"
                      onClick={() => {
                        setDeleteTarget(item);
                        setDeleteDevIds(item.devices || []);
                      }}
                    >
                      <Trash2 size={12} />
                    </button>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}

      <ModalDialog
        open={uploadOpen}
        title="上传本地照片注册/同步人脸"
        onClose={() => setUploadOpen(false)}
        width={500}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setUploadOpen(false)}>
              取消
            </button>
            <button type="submit" form="face-upload-form" className="btn btn-primary" disabled={busy}>
              <Upload size={14} /> 压缩合规并上传
            </button>
          </>
        }
      >
        <form id="face-upload-form" onSubmit={handleSubmitUpload} style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div className="notice-bar info">
            系统会自动将选中的 JPG/PNG 照片规范缩放为考勤机标准可见光尺寸（≤640px，基线 JPEG），并兼容 ZAM180 与 ZAM230。
          </div>
          <div className="field">
            <label htmlFor="f-pin">目标员工工号</label>
            <input
              id="f-pin"
              className="input mono"
              required
              value={uploadPin}
              onChange={(e) => setUploadPin(e.target.value)}
              placeholder="输入考勤机上已存在的工号"
              autoFocus
            />
          </div>
          <div className="field">
            <label>照片文件</label>
            <div className="toolbar-row">
              <button type="button" className="btn" onClick={handlePickFile}>
                选择 JPG / PNG 图片...
              </button>
              <span style={{ fontSize: 12, color: 'var(--text-muted)', wordBreak: 'break-all' }}>
                {uploadPath || '尚未选择文件'}
              </span>
            </div>
          </div>
          <div className="field">
            <label>上传并注册到以下考勤机</label>
            <div className="check-group">
              {enabledDevices.map((d) => (
                <label key={d.id} className="check-item">
                  <input
                    type="checkbox"
                    checked={uploadTargets.includes(d.id)}
                    onChange={() =>
                      setUploadTargets((prev) =>
                        prev.includes(d.id) ? prev.filter((x) => x !== d.id) : [...prev, d.id]
                      )
                    }
                  />
                  {d.name}
                </label>
              ))}
            </div>
          </div>
        </form>
      </ModalDialog>

      <ModalDialog
        open={!!syncTarget}
        title="同步人脸到指定考勤机"
        onClose={() => setSyncTarget(null)}
        width={460}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setSyncTarget(null)}>
              取消
            </button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || syncDevIds.length === 0}
              onClick={handleConfirmSyncFace}
            >
              <ArrowLeftRight size={13} /> 同步到所选 ({syncDevIds.length} 台)
            </button>
          </>
        }
      >
        <p style={{ margin: 0 }}>
          选择要接收员工 <strong>{syncTarget?.name || syncTarget?.pin}</strong>（工号 {syncTarget?.pin}）人脸照片的目标考勤机：
        </p>
        <div className="toolbar-row" style={{ justifyContent: 'space-between' }}>
          <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>
            已有设备：{(syncTarget?.devices || []).length > 0 ? (syncTarget?.devices || []).join('、') : '仅本地缓存'}
          </span>
          <div className="toolbar-row" style={{ gap: 6 }}>
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              onClick={() => setSyncDevIds([...connectedDeviceIds])}
            >
              选已连接
            </button>
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              onClick={() => setSyncDevIds([])}
            >
              清空
            </button>
          </div>
        </div>
        <div className="check-group">
          {enabledDevices.map((d) => {
            const isConn = !!statuses[d.id]?.connected;
            const hasFace = (syncTarget?.devices || []).includes(d.id);
            return (
              <label key={d.id} className="check-item">
                <input
                  type="checkbox"
                  checked={syncDevIds.includes(d.id)}
                  onChange={() =>
                    setSyncDevIds((prev) =>
                      prev.includes(d.id) ? prev.filter((x) => x !== d.id) : [...prev, d.id]
                    )
                  }
                />
                {d.name}
                {hasFace ? ' (已有)' : ''}
                {!isConn ? ' [未连接]' : ''}
              </label>
            );
          })}
        </div>
      </ModalDialog>

      <ModalDialog
        open={!!deleteTarget}
        title="确认删除设备人脸"
        onClose={() => setDeleteTarget(null)}
        width={440}
        footer={
          <>
            <button type="button" className="btn" autoFocus onClick={() => setDeleteTarget(null)}>
              取消
            </button>
            <button
              type="button"
              className="btn btn-danger"
              disabled={busy || deleteDevIds.length === 0}
              onClick={handleDeleteFace}
            >
              确认删除人脸
            </button>
          </>
        }
      >
        <p style={{ margin: 0 }}>
          将从所选设备删除员工 <strong>{deleteTarget?.name || deleteTarget?.pin}</strong>（工号 {deleteTarget?.pin}）的人脸数据（保留基础信息与指纹）。
        </p>
        <div className="check-group">
          {enabledDevices.map((d) => (
            <label key={d.id} className="check-item">
              <input
                type="checkbox"
                checked={deleteDevIds.includes(d.id)}
                onChange={() =>
                  setDeleteDevIds((prev) =>
                    prev.includes(d.id) ? prev.filter((x) => x !== d.id) : [...prev, d.id]
                  )
                }
              />
              {d.name}
            </label>
          ))}
        </div>
      </ModalDialog>

      {/* P1-7 Folder Batch Face Upload Modal */}
      <ModalDialog
        open={batchOpen}
        title="文件夹批量上传人脸（按工号自动匹配）"
        onClose={() => setBatchOpen(false)}
        width={680}
        footer={
          <>
            <button type="button" className="btn" autoFocus onClick={() => setBatchOpen(false)}>
              关闭
            </button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || batchSelectedFiles.length === 0 || batchTargets.length === 0}
              onClick={handleRunBatchUpload}
            >
              <FolderUp size={14} /> 批量上传 ({batchSelectedFiles.length} 张)
            </button>
          </>
        }
      >
        <div className="notice-bar info">
          源文件夹：<span className="mono">{batchDir}</span> · 识别到 <strong>{batchCandidates.length}</strong> 张合规命名的照片（支持 `103.jpg`、`103_姓名.jpg`）。
        </div>

        <div className="field">
          <label>上传并注册到以下考勤机</label>
          <div className="check-group">
            {enabledDevices.map((d) => (
              <label key={d.id} className="check-item">
                <input
                  type="checkbox"
                  checked={batchTargets.includes(d.id)}
                  onChange={() =>
                    setBatchTargets((prev) =>
                      prev.includes(d.id) ? prev.filter((x) => x !== d.id) : [...prev, d.id]
                    )
                  }
                />
                {d.name}
              </label>
            ))}
          </div>
        </div>

        {batchCandidates.length > 0 && (
          <div
            tabIndex={0}
            role="region"
            aria-label="文件夹人脸匹配列表"
            className="table-wrapper"
            style={{ maxHeight: 250, border: '1px solid var(--border)', borderRadius: 8 }}
          >
            <table className="data-table">
              <thead>
                <tr>
                  <th style={{ width: 34 }}>
                    <input
                      type="checkbox"
                      aria-label="全选照片"
                      checked={batchSelectedFiles.length === batchCandidates.length}
                      onChange={() =>
                        setBatchSelectedFiles(
                          batchSelectedFiles.length === batchCandidates.length
                            ? []
                            : batchCandidates.map((c) => c.filePath)
                        )
                      }
                    />
                  </th>
                  <th style={{ width: 75 }}>工号</th>
                  <th style={{ width: 100 }}>匹配姓名</th>
                  <th>文件名</th>
                  <th style={{ width: 75 }}>大小</th>
                  <th style={{ width: 95 }}>设备状态</th>
                </tr>
              </thead>
              <tbody>
                {batchCandidates.map((c) => (
                  <tr key={c.filePath}>
                    <td>
                      <input
                        type="checkbox"
                        checked={batchSelectedFiles.includes(c.filePath)}
                        onChange={() =>
                          setBatchSelectedFiles((prev) =>
                            prev.includes(c.filePath)
                              ? prev.filter((x) => x !== c.filePath)
                              : [...prev, c.filePath]
                          )
                        }
                      />
                    </td>
                    <td className="mono">{c.pin}</td>
                    <td>{c.name || <span style={{ color: 'var(--text-muted)' }}>-</span>}</td>
                    <td className="mono" style={{ fontSize: 11.5 }}>{c.fileName}</td>
                    <td className="mono">{Math.max(1, Math.round(c.sizeBytes / 1024))} KB</td>
                    <td>
                      {c.existsInDevs ? (
                        <span className="badge badge-ok">已存在</span>
                      ) : (
                        <span className="badge badge-warn">未在缓存中</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {batchResult && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <div className={`notice-bar ${batchResult.successAll === batchResult.totalFiles ? 'success' : 'error'}`}>
              上传结果：{batchResult.successAll} / {batchResult.totalFiles} 张全部设备成功
            </div>
            {(batchResult.items || [])
              .filter((x) => x.failCnt > 0)
              .slice(0, 6)
              .map((x) => (
                <div key={x.pin} style={{ fontSize: 11.5, color: 'var(--danger)' }}>
                  工号 {x.pin} ({x.fileName}): {(x.errors || []).join('；')}
                </div>
              ))}
          </div>
        )}
      </ModalDialog>
    </>
  );
}
