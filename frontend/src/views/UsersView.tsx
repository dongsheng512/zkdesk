import { useEffect, useId, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  ArrowDownToLine,
  ArrowRight,
  ArrowUpFromLine,
  Camera,
  CheckCircle2,
  ChevronDown,
  Database,
  Download,
  FileSpreadsheet,
  Fingerprint,
  FolderOpen,
  HardDriveDownload,
  HardDriveUpload,
  KeyRound,
  Layers,
  MonitorSmartphone,
  Pencil,
  RefreshCw,
  Search,
  SlidersHorizontal,
  Trash2,
  Upload,
  UserPlus,
  Users,
} from 'lucide-react';
import {
  ActionDeviceResult,
  DeviceConfig,
  DeviceStatus,
  MergedUserRow,
  PullToMasterResponse,
  SyncUsersResponse,
  UserMatrixResponse,
} from '../../bindings/zk_mac/internal/models';
import * as ZK from '../../bindings/zk_mac/internal/service/zkservice';
import { ModalDialog } from '../components/ModalDialog';

interface UsersViewProps {
  devices: DeviceConfig[];
  statuses: Record<string, DeviceStatus | undefined>;
  matrix: UserMatrixResponse;
  busy: boolean;
  onMatrixChange: (m: UserMatrixResponse) => void;
  onAuditChanged: () => void;
  onGoToDevices: () => void;
  onNotify: (type: 'info' | 'success' | 'error', text: string) => void;
  setBusy: (v: boolean) => void;
}

type PerspectiveKey = 'local' | 'diff' | 'device';

interface EditState {
  isNew: boolean;
  inMaster: boolean;
  pin: string;
  name: string;
  password: string;
  privilege: number;
  enabled: boolean;
  card: string;
  department: string;
  workStatus: string;
  note: string;
  masterFpCount: number;
  masterPasswordSet: boolean;
  preserveCardIfEmpty: boolean;
  preservePwdIfEmpty: boolean;
  faceDataUrl: string;
  faceSourceInfo: string;
  pickedLocalFile: string;
  bioClearDeviceId: string;
}

interface MultiPushModalState {
  pins: string[];
  allowScopeToggle: boolean;
  pushScope: 'pending' | 'all_active';
  pendingPins: string[];
  allActivePins: string[];
  targetDeviceIds: string[];
  syncInfo: boolean;
  syncFP: boolean;
  syncFace: boolean;
  preserveCard: boolean;
  preservePwd: boolean;
  result: SyncUsersResponse | null;
}

interface MultiPullModalState {
  sourceDeviceIds: string[];
  pins: string[];
  pullAll: boolean;
  includeInfo: boolean;
  includeFP: boolean;
  includeFace: boolean;
  overwriteInfo: boolean;
  result: PullToMasterResponse | null;
}

interface MultiDeleteModalState {
  pins: string[];
  targetDeviceIds: string[];
  result: ActionDeviceResult[] | null;
}

function shortDeviceName(name: string): string {
  return name.replace(/\s*\([^)]*\)\s*$/, '').trim() || name;
}

function privilegeLabel(priv: number): string {
  if (priv === 14) return '管理员';
  if (priv === 2) return '登记员';
  return '普通员工';
}

export function UsersView({
  devices,
  statuses,
  matrix,
  busy,
  onMatrixChange,
  onAuditChanged,
  onGoToDevices,
  onNotify,
  setBusy,
}: UsersViewProps) {
  const localToolsPopoverId = useId();
  const localToolsPopoverRef = useRef<HTMLDivElement | null>(null);

  const enabledDevices = useMemo(() => devices.filter((d) => d.enabled), [devices]);
  const connectedDevices = useMemo(
    () => enabledDevices.filter((d) => statuses[d.id]?.connected),
    [enabledDevices, statuses]
  );
  const connectedDeviceIds = useMemo(
    () => connectedDevices.map((d) => d.id),
    [connectedDevices]
  );

  // Default first screen: 'local' (Local Staff Master DB)
  const [perspective, setPerspective] = useState<PerspectiveKey>('local');
  const [activeDeviceId, setActiveDeviceId] = useState<string>('');

  useEffect(() => {
    if (enabledDevices.length === 0) {
      setActiveDeviceId('');
      return;
    }
    if (!activeDeviceId || !enabledDevices.some((d) => d.id === activeDeviceId)) {
      setActiveDeviceId(connectedDeviceIds[0] || enabledDevices[0].id);
    }
  }, [enabledDevices, connectedDeviceIds, activeDeviceId]);

  const activeDevice = useMemo(
    () => enabledDevices.find((d) => d.id === activeDeviceId) || null,
    [enabledDevices, activeDeviceId]
  );

  // Filters
  const [query, setQuery] = useState('');
  const [filterDept, setFilterDept] = useState('');
  const [localDeviceFilter, setLocalDeviceFilter] = useState<string>(''); // '' | '__unassigned__' | <devId>
  const [localFilter, setLocalFilter] = useState<'all' | 'active' | 'resigned' | 'no_face' | 'no_fp'>('all');
  const [diffFilter, setDiffFilter] = useState<'diff' | 'pull' | 'push' | 'resigned'>('diff');
  const [diffDeviceFilter, setDiffDeviceFilter] = useState<string>('');
  const [devFilter, setDevFilter] = useState<'present' | 'not_assigned' | 'pull' | 'push' | 'resigned' | 'all'>('present');

  // Selection (cleared on perspective switch)
  const [selectedPins, setSelectedPins] = useState<string[]>([]);

  // Modals
  const [editState, setEditState] = useState<EditState | null>(null);
  const [faceLoading, setFaceLoading] = useState(false);
  const [empSyncPin, setEmpSyncPin] = useState<string | null>(null);
  const [pushModal, setPushModal] = useState<MultiPushModalState | null>(null);
  const [pullModal, setPullModal] = useState<MultiPullModalState | null>(null);
  const [deleteDevModal, setDeleteDevModal] = useState<MultiDeleteModalState | null>(null);
  const [deleteMasterPin, setDeleteMasterPin] = useState<string | null>(null);
  const [clearBioConfirm, setClearBioConfirm] = useState<{
    pin: string;
    target: 'fingerprints' | 'password' | 'face';
    label: string;
    device: DeviceConfig;
  } | null>(null);

  const switchPerspective = (next: PerspectiveKey, targetDevId?: string) => {
    setPerspective(next);
    if (targetDevId) {
      setActiveDeviceId(targetDevId);
    }
    setSelectedPins([]);
  };

  // ==================== Per-Device Stats ====================
  const perDeviceStats = useMemo(() => {
    const map: Record<
      string,
      { present: number; notAssigned: number; push: number; pull: number; resigned: number; synced: number; diff: number; hasSnapshot: boolean }
    > = {};
    for (const d of enabledDevices) {
      map[d.id] = { present: 0, notAssigned: 0, push: 0, pull: 0, resigned: 0, synced: 0, diff: 0, hasSnapshot: false };
    }
    for (const r of matrix.rows || []) {
      for (const d of enabledDevices) {
        const cell = r.devices?.[d.id];
        if (!cell) continue;
        const st = map[d.id];
        if (cell.syncStatus !== 'no_snapshot') st.hasSnapshot = true;
        if (cell.present) st.present++;
        if (!r.inMaster && !cell.present) continue;
        switch (cell.syncStatus) {
          case 'not_assigned':
            st.notAssigned++;
            break;
          case 'need_push':
            st.push++;
            st.diff++;
            break;
          case 'need_pull':
            st.pull++;
            st.diff++;
            break;
          case 'both':
            st.push++;
            st.pull++;
            st.diff++;
            break;
          case 'resigned_present':
            st.resigned++;
            st.diff++;
            break;
          case 'synced':
            st.synced++;
            break;
        }
      }
    }
    return map;
  }, [enabledDevices, matrix.rows]);

  const scannedDeviceCount = useMemo(
    () => enabledDevices.filter((d) => perDeviceStats[d.id]?.hasSnapshot).length,
    [enabledDevices, perDeviceStats]
  );

  // ==================== Row Presence & Diff Summary ====================
  const summarizeRowAcrossDevices = (row: MergedUserRow) => {
    const presentDevices: Array<{ dev: DeviceConfig; status: string; detail: string }> = [];
    const diffDevices: Array<{ dev: DeviceConfig; status: string; detail: string; present: boolean }> = [];
    let scanned = 0;

    for (const d of enabledDevices) {
      const cell = row.devices?.[d.id];
      const st = cell?.syncStatus || 'no_snapshot';
      if (st === 'no_snapshot') continue;
      scanned++;

      if (cell?.present) {
        presentDevices.push({
          dev: d,
          status: st,
          detail: (cell.diffDetails || []).join('、'),
        });
      }
      if (st === 'need_push' || st === 'need_pull' || st === 'both' || st === 'resigned_present') {
        diffDevices.push({
          dev: d,
          status: st,
          detail: (cell?.diffDetails || []).join('、'),
          present: !!cell?.present,
        });
      }
    }
    return { scanned, presentDevices, diffDevices };
  };

  // ==================== Filtering ====================
  const matchSearchAndDept = (r: MergedUserRow) => {
    if (filterDept && r.department !== filterDept) return false;
    const q = query.trim().toLowerCase();
    if (!q) return true;
    return (
      r.pin.toLowerCase().includes(q) ||
      (r.primaryName || '').toLowerCase().includes(q) ||
      (r.department || '').toLowerCase().includes(q) ||
      (r.card || '').toLowerCase().includes(q) ||
      (r.profileNote || '').toLowerCase().includes(q)
    );
  };

  const showDeptColumn = (matrix.departments || []).length > 0;
  const hasAnyNote = useMemo(
    () => (matrix.rows || []).some((r) => r.inMaster && !!(r.profileNote || '').trim()),
    [matrix.rows]
  );

  // 1. Local Master Directory rows
  const localAllRows = useMemo(
    () => (matrix.rows || []).filter((r) => r.inMaster),
    [matrix.rows]
  );

  const localStats = useMemo(() => {
    let active = 0;
    let resigned = 0;
    let noFace = 0;
    let noFp = 0;
    for (const r of localAllRows) {
      if (r.workStatus === 'resigned') resigned++;
      else active++;
      if (!r.hasLocalFace) noFace++;
      if ((r.masterFpCount || 0) === 0) noFp++;
    }
    return { total: localAllRows.length, active, resigned, noFace, noFp };
  }, [localAllRows]);

  const localFilteredRows = useMemo(() => {
    return localAllRows.filter((r) => {
      if (localFilter === 'active' && r.workStatus === 'resigned') return false;
      if (localFilter === 'resigned' && r.workStatus !== 'resigned') return false;
      if (localFilter === 'no_face' && r.hasLocalFace) return false;
      if (localFilter === 'no_fp' && (r.masterFpCount || 0) > 0) return false;
      if (localDeviceFilter === '__unassigned__') {
        if (enabledDevices.some((d) => r.devices?.[d.id]?.present)) return false;
      } else if (localDeviceFilter) {
        if (!r.devices?.[localDeviceFilter]?.present) return false;
      }
      return matchSearchAndDept(r);
    });
  }, [localAllRows, localFilter, localDeviceFilter, enabledDevices, filterDept, query]);

  // 2. Real Inconsistencies / Reconciliation rows (Tab 2)
  const diffFilteredRows = useMemo(() => {
    return (matrix.rows || []).filter((r) => {
      if (diffDeviceFilter) {
        const cell = r.devices?.[diffDeviceFilter];
        const st = cell?.syncStatus || 'no_snapshot';
        if (diffFilter === 'diff' && st !== 'need_push' && st !== 'need_pull' && st !== 'both' && st !== 'resigned_present') {
          return false;
        }
        if (diffFilter === 'push' && st !== 'need_push' && st !== 'both') return false;
        if (diffFilter === 'pull' && st !== 'need_pull' && st !== 'both') return false;
        if (diffFilter === 'resigned' && st !== 'resigned_present') return false;
      } else {
        if (diffFilter === 'diff' && !r.hasDiff && !r.hasPendingPush && !r.hasPendingPull && !r.hasResignedInDev) {
          return false;
        }
        if (diffFilter === 'push' && !r.hasPendingPush) return false;
        if (diffFilter === 'pull' && !r.hasPendingPull) return false;
        if (diffFilter === 'resigned' && !r.hasResignedInDev) return false;
      }
      return matchSearchAndDept(r);
    });
  }, [matrix.rows, diffFilter, diffDeviceFilter, filterDept, query]);

  // 3. Single-Device Roster rows (Tab 3)
  const activeDevAllRows = useMemo(() => {
    if (!activeDevice) return [];
    return (matrix.rows || []).filter(
      (r) => r.inMaster || !!r.devices?.[activeDevice.id]?.present
    );
  }, [matrix.rows, activeDevice]);

  const activeDevFilteredRows = useMemo(() => {
    if (!activeDevice) return [];
    return activeDevAllRows.filter((r) => {
      const cell = r.devices?.[activeDevice.id];
      const st = cell?.syncStatus || 'no_snapshot';
      if (devFilter === 'present' && !cell?.present) return false;
      if (devFilter === 'not_assigned' && !(r.inMaster && !cell?.present && r.workStatus !== 'resigned')) return false;
      if (devFilter === 'push' && st !== 'need_push' && st !== 'both') return false;
      if (devFilter === 'pull' && st !== 'need_pull' && st !== 'both') return false;
      if (devFilter === 'resigned' && st !== 'resigned_present') return false;
      return matchSearchAndDept(r);
    });
  }, [activeDevAllRows, activeDevice, devFilter, filterDept, query]);

  const empSyncRow = useMemo(
    () => (empSyncPin ? (matrix.rows || []).find((r) => r.pin === empSyncPin) || null : null),
    [empSyncPin, matrix.rows]
  );

  // ==================== Hardware Connection & Scan ====================
  const ensureConnectedOrNotify = (ids: string[]): boolean => {
    const missing = ids
      .map((id) => enabledDevices.find((d) => d.id === id))
      .filter((d): d is DeviceConfig => !!d && !statuses[d.id]?.connected)
      .map((d) => shortDeviceName(d.name));
    if (missing.length > 0) {
      onNotify('error', `考勤机「${missing.join('、')}」尚未连接，请先在「设备总览」中手动连接后再操作`);
      return false;
    }
    return true;
  };

  const handleScanDevices = async (targets: string[]) => {
    const valid = targets.filter((id) => enabledDevices.some((d) => d.id === id));
    if (valid.length === 0) {
      onNotify('error', '请先在「设备总览」中连接至少一台考勤机');
      return;
    }
    if (!ensureConnectedOrNotify(valid)) return;
    setBusy(true);
    try {
      const m = await ZK.RefreshUsers(valid, true);
      onMatrixChange(m);
      setSelectedPins([]);
      if (valid.length === 1) {
        const dev = enabledDevices.find((d) => d.id === valid[0]);
        const devCount = (m.rows || []).filter((r) => r.devices?.[valid[0]]?.present).length;
        onNotify(
          'success',
          `已刷新 ${dev ? shortDeviceName(dev.name) : valid[0]} 名单：机上 ${devCount} 人（本地主库 ${m.masterCount} 人）`
        );
      } else {
        onNotify(
          'success',
          `已刷新 ${valid.length} 台已连接考勤机名单：本地主库 ${m.masterCount} 人${m.diffUsers > 0 ? ` · ${m.diffUsers} 人有差异` : ''}`
        );
      }
    } catch (err: any) {
      // Refresh local matrix in case some devices succeeded before another failed
      ZK.GetUserMatrix()
        .then((latest) => {
          if (latest) onMatrixChange(latest);
        })
        .catch(() => {});
      onNotify('error', `扫描考勤机失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  // ==================== Local Master Tools ====================
  const hideLocalToolsPopover = () => {
    (localToolsPopoverRef.current as (HTMLDivElement & { hidePopover?: () => void }) | null)?.hidePopover?.();
  };

  const handleImportProfiles = async () => {
    hideLocalToolsPopover();
    try {
      const res = await ZK.ImportProfilesFromDialog();
      if (!res || !res.filePath) return;
      const m = await ZK.GetUserMatrix();
      onMatrixChange(m);
      onAuditChanged();
      onNotify('success', `已导入 ${res.imported} 条部门/离职档案（跳过 ${res.skipped} 行）`);
    } catch (err: any) {
      onNotify('error', `导入部门档案失败：${err?.message || err}`);
    }
  };

  const handleExportMasterExcel = async () => {
    hideLocalToolsPopover();
    try {
      const res = await ZK.ExportMasterUsersExcel();
      if (!res || !res.filePath) return;
      onAuditChanged();
      onNotify('success', `已导出本地人员花名册（${res.userCount} 人）到：${res.filePath}`);
    } catch (err: any) {
      onNotify('error', `导出 Excel 失败：${err?.message || err}`);
    }
  };

  const handleExportMasterBackup = async () => {
    hideLocalToolsPopover();
    try {
      const res = await ZK.ExportMasterUsersBackup();
      if (!res || !res.filePath) return;
      onAuditChanged();
      onNotify('success', `已备份本地人员主库（${res.userCount} 人）到：${res.filePath}`);
    } catch (err: any) {
      onNotify('error', `备份本地人员库失败：${err?.message || err}`);
    }
  };

  const handleImportMasterBackup = async () => {
    hideLocalToolsPopover();
    try {
      const res = await ZK.ImportMasterUsersBackup();
      if (!res || !res.filePath) return;
      const m = await ZK.GetUserMatrix();
      onMatrixChange(m);
      onAuditChanged();
      onNotify(
        'success',
        `已恢复本地人员主库（${res.userCount} 人）${res.backupPath ? '，原主库已自动快照备份' : ''}`
      );
    } catch (err: any) {
      onNotify('error', `恢复本地人员库失败：${err?.message || err}`);
    }
  };

  // ==================== Edit / New Employee (Local-First) ====================
  const openNewUser = () => {
    setEditState({
      isNew: true,
      inMaster: false,
      pin: '',
      name: '',
      password: '',
      privilege: 0,
      enabled: true,
      card: '',
      department: '',
      workStatus: 'active',
      note: '',
      masterFpCount: 0,
      masterPasswordSet: false,
      preserveCardIfEmpty: false,
      preservePwdIfEmpty: false,
      faceDataUrl: '',
      faceSourceInfo: '',
      pickedLocalFile: '',
      bioClearDeviceId: perspective === 'device' ? activeDevice?.id || '' : '',
    });
  };

  const openEditUser = async (row: MergedUserRow, clearBioDevId?: string) => {
    const devForClear =
      clearBioDevId || (perspective === 'device' && activeDevice && row.devices?.[activeDevice.id]?.present ? activeDevice.id : '');
    setEditState({
      isNew: false,
      inMaster: row.inMaster,
      pin: row.pin,
      name: row.primaryName || '',
      password: '',
      privilege: row.privilege,
      enabled: row.enabled ?? true,
      card: row.card || '',
      department: row.department || '',
      workStatus: row.workStatus || 'active',
      note: row.profileNote || '',
      masterFpCount: row.masterFpCount || 0,
      masterPasswordSet: !!row.masterPasswordSet,
      preserveCardIfEmpty: true,
      preservePwdIfEmpty: true,
      faceDataUrl: '',
      faceSourceInfo: '',
      pickedLocalFile: '',
      bioClearDeviceId: devForClear,
    });
    setFaceLoading(true);
    try {
      const photo = await ZK.FetchFacePhoto('', row.pin);
      if (photo && photo.dataUrl) {
        setEditState((cur) =>
          cur && cur.pin === row.pin
            ? {
                ...cur,
                faceDataUrl: photo.dataUrl,
                faceSourceInfo:
                  photo.cachedFrom === 'master'
                    ? '本地主库人脸照'
                    : photo.cachedFrom
                      ? `本地缓存（来源：${photo.cachedFrom}）`
                      : '本地人脸照',
              }
            : cur
        );
      }
    } catch {
      // ignore
    } finally {
      setFaceLoading(false);
    }
  };

  const handlePickFaceForEdit = async () => {
    try {
      const path = await ZK.PickLocalImage();
      if (!path) return;
      const preview = await ZK.PreviewLocalImage(path);
      setEditState((cur) =>
        cur
          ? {
              ...cur,
              pickedLocalFile: path,
              faceDataUrl: preview,
              faceSourceInfo: `待保存到主库：${path.split('/').pop()}`,
            }
          : cur
      );
    } catch (err: any) {
      onNotify('error', `选择照片失败：${err?.message || err}`);
    }
  };

  const saveEditToLocal = async (openPushAfter: boolean) => {
    if (!editState) return;
    const pin = editState.pin.trim();
    if (!pin) {
      onNotify('error', '请输入工号');
      return;
    }
    setBusy(true);
    try {
      const updated = await ZK.SaveMasterUser({
        pin,
        name: editState.name.trim(),
        password: editState.password,
        privilege: editState.privilege,
        enabled: editState.enabled,
        card: editState.card.trim(),
        department: editState.department.trim(),
        workStatus: editState.workStatus || 'active',
        note: editState.note.trim(),
        preserveCardIfEmpty: editState.preserveCardIfEmpty,
        preservePwdIfEmpty: editState.preservePwdIfEmpty,
        localFaceFilePath: editState.pickedLocalFile,
      });
      onMatrixChange(updated);
      onAuditChanged();
      setEditState(null);
      if (openPushAfter && connectedDeviceIds.length > 0) {
        onNotify('info', `已保存工号 ${pin} 到本地主库，请手动选择要上传的考勤机`);
        setPushModal({
          pins: [pin],
          allowScopeToggle: false,
          pushScope: 'all_active',
          pendingPins: [],
          allActivePins: [pin],
          targetDeviceIds: [],
          syncInfo: true,
          syncFP: true,
          syncFace: true,
          preserveCard: true,
          preservePwd: true,
          result: null,
        });
      } else {
        onNotify('success', `已保存工号 ${pin} 到本地人员主库（自动 fsync + 滚动备份，未改动考勤机）`);
      }
    } catch (err: any) {
      onNotify('error', `保存到本地主库失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  // ==================== Manual-Device Push / Pull / Delete Modals ====================
  const openMultiPushModal = (opts?: { pins?: string[]; preselectDeviceIds?: string[] }) => {
    if (connectedDeviceIds.length === 0) {
      onNotify('error', '当前没有已连接的考勤机，请先在「设备总览」中手动连接后再上传');
      return;
    }

    const masterSet = new Set(localAllRows.map((r) => r.pin));
    const explicitPins = (opts?.pins && opts.pins.length > 0 ? opts.pins : selectedPins).filter((p) =>
      masterSet.has(p)
    );

    const allActivePins = localAllRows.filter((r) => r.workStatus !== 'resigned').map((r) => r.pin);
    const pendingPins = localAllRows.filter((r) => r.hasPendingPush).map((r) => r.pin);

    let pins = explicitPins;
    let allowScopeToggle = false;
    let pushScope: 'pending' | 'all_active' = 'all_active';

    if (pins.length === 0) {
      if (pendingPins.length > 0 && allActivePins.length > pendingPins.length) {
        allowScopeToggle = true;
        pushScope = 'pending';
        pins = pendingPins;
      } else {
        pins = allActivePins;
        pushScope = 'all_active';
      }
    }

    if (pins.length === 0) {
      onNotify('info', '本地主库中暂无可上传的在职员工，请先新增或从考勤机拉取');
      return;
    }

    // Strictly manual selection by default (empty), unless explicitly targeting 1 specific device
    const initialDevices = (opts?.preselectDeviceIds || []).filter((id) => connectedDeviceIds.includes(id));

    setPushModal({
      pins,
      allowScopeToggle,
      pushScope,
      pendingPins,
      allActivePins,
      targetDeviceIds: initialDevices,
      syncInfo: true,
      syncFP: true,
      syncFace: true,
      preserveCard: true,
      preservePwd: true,
      result: null,
    });
  };

  const handleConfirmMultiPush = async () => {
    if (!pushModal) return;
    if (pushModal.targetDeviceIds.length === 0) {
      onNotify('error', '请手动勾选至少一台已连接的目标考勤机');
      return;
    }
    const pinsToPush = pushModal.allowScopeToggle
      ? pushModal.pushScope === 'pending'
        ? pushModal.pendingPins
        : pushModal.allActivePins
      : pushModal.pins;
    if (pinsToPush.length === 0) {
      onNotify('error', '当前没有可上传的员工');
      return;
    }
    if (!ensureConnectedOrNotify(pushModal.targetDeviceIds)) return;

    setBusy(true);
    try {
      const res = await ZK.PushMasterToDevices({
        pins: pinsToPush,
        deviceIds: pushModal.targetDeviceIds,
        syncInfo: pushModal.syncInfo,
        syncFp: pushModal.syncFP,
        syncFace: pushModal.syncFace,
        preserveCardIfEmpty: pushModal.preserveCard,
        preservePwdIfEmpty: pushModal.preservePwd,
      });
      setPushModal((cur) => (cur ? { ...cur, result: res } : null));
      onAuditChanged();
      const okDevs = (res.results || []).filter((r) => r.success).length;
      const totalDevs = (res.results || []).length;
      onNotify(
        okDevs === totalDevs ? 'success' : 'info',
        `上传完成：${res.readBundles} 人 → ${okDevs}/${totalDevs} 台考勤机成功`
      );
      const refreshed = await ZK.RefreshUsers(pushModal.targetDeviceIds, true);
      onMatrixChange(refreshed);
      setSelectedPins([]);
    } catch (err: any) {
      onNotify('error', `上传失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const openMultiPullModal = (opts?: { pins?: string[]; preselectDeviceIds?: string[]; forceAll?: boolean }) => {
    if (connectedDeviceIds.length === 0) {
      onNotify('error', '当前没有已连接的考勤机，请先在「设备总览」中手动连接后再拉取');
      return;
    }
    const explicitPins = opts?.forceAll ? [] : opts?.pins && opts.pins.length > 0 ? opts.pins : selectedPins;
    const preselect = (opts?.preselectDeviceIds || []).filter((id) => connectedDeviceIds.includes(id));

    setPullModal({
      sourceDeviceIds: preselect, // Default empty = manual selection!
      pins: explicitPins,
      pullAll: opts?.forceAll ? true : explicitPins.length === 0,
      includeInfo: true,
      includeFP: true,
      includeFace: true,
      overwriteInfo: false,
      result: null,
    });
  };

  const handleConfirmMultiPull = async () => {
    if (!pullModal) return;
    if (pullModal.sourceDeviceIds.length === 0) {
      onNotify('error', '请手动勾选至少一台已连接的来源考勤机');
      return;
    }
    if (!ensureConnectedOrNotify(pullModal.sourceDeviceIds)) return;

    setBusy(true);
    try {
      const res = await ZK.PullDevicesToMaster({
        deviceIds: pullModal.sourceDeviceIds,
        pins: pullModal.pullAll ? [] : pullModal.pins,
        includeInfo: pullModal.includeInfo,
        includeFp: pullModal.includeFP,
        includeFace: pullModal.includeFace,
        overwriteExistingInfo: pullModal.overwriteInfo,
      });
      setPullModal((cur) => (cur ? { ...cur, result: res } : null));
      onAuditChanged();
      // Refresh snapshots for pulled devices so UI immediately reflects latest presence & master state
      const refreshed = await ZK.RefreshUsers(pullModal.sourceDeviceIds, true);
      onMatrixChange(refreshed);
      setSelectedPins([]);
      onNotify(
        (res.errors || []).length === 0 ? 'success' : 'info',
        `已从 ${pullModal.sourceDeviceIds.length} 台考勤机拉取到本地主库：处理 ${res.totalProcessed} 人（新增 ${res.created} · 更新 ${res.updated} · 指纹 ${res.fpsAbsorbed} 枚 · 人脸 ${res.facesAbsorbed} 张）`
      );
    } catch (err: any) {
      onNotify('error', `拉取到本地主库失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const openMultiDeleteModal = (opts?: { pins?: string[]; preselectDeviceIds?: string[] }) => {
    if (connectedDeviceIds.length === 0) {
      onNotify('error', '当前没有已连接的考勤机，请先在「设备总览」中手动连接后再移除');
      return;
    }

    let pins = opts?.pins && opts.pins.length > 0 ? opts.pins : selectedPins;
    if (pins.length === 0) {
      pins = (matrix.rows || []).filter((r) => r.hasResignedInDev).map((r) => r.pin);
    }
    if (pins.length === 0) {
      onNotify('info', '请先勾选要从考勤机移除的员工');
      return;
    }

    const preselect = (opts?.preselectDeviceIds || []).filter((id) => connectedDeviceIds.includes(id));

    setDeleteDevModal({
      pins,
      targetDeviceIds: preselect, // Default empty = manual selection!
      result: null,
    });
  };

  const handleConfirmMultiDelete = async () => {
    if (!deleteDevModal) return;
    if (deleteDevModal.targetDeviceIds.length === 0) {
      onNotify('error', '请手动勾选至少一台已连接的考勤机');
      return;
    }
    if (!ensureConnectedOrNotify(deleteDevModal.targetDeviceIds)) return;

    setBusy(true);
    try {
      const res = await ZK.DeleteUsers({
        pins: deleteDevModal.pins,
        deviceIds: deleteDevModal.targetDeviceIds,
      });
      onAuditChanged();
      const okCnt = (res || []).filter((r) => r.ok).length;
      onNotify(
        okCnt === deleteDevModal.targetDeviceIds.length ? 'success' : 'info',
        `已从 ${okCnt}/${deleteDevModal.targetDeviceIds.length} 台考勤机移除 ${deleteDevModal.pins.length} 人（本地主库已保留）`
      );
      const devIds = deleteDevModal.targetDeviceIds;
      setDeleteDevModal(null);
      setSelectedPins([]);
      const refreshed = await ZK.RefreshUsers(devIds, true);
      onMatrixChange(refreshed);
    } catch (err: any) {
      onNotify('error', `移除失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const handleConfirmDeleteMaster = async () => {
    if (!deleteMasterPin) return;
    setBusy(true);
    try {
      const m = await ZK.DeleteMasterUser(deleteMasterPin);
      onMatrixChange(m);
      onAuditChanged();
      onNotify('info', `已从本地主库移除工号 ${deleteMasterPin}（已自动备份，不影响考勤机）`);
      setDeleteMasterPin(null);
      setEditState(null);
      if (empSyncPin === deleteMasterPin) setEmpSyncPin(null);
    } catch (err: any) {
      onNotify('error', `移除失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const handleConfirmClearBio = async () => {
    if (!clearBioConfirm) return;
    setBusy(true);
    try {
      const res = await ZK.ClearUserBiometric({
        pin: clearBioConfirm.pin,
        target: clearBioConfirm.target,
        deviceIds: [clearBioConfirm.device.id],
      });
      onAuditChanged();
      onNotify(
        res?.[0]?.ok ? 'success' : 'info',
        `已清空工号 ${clearBioConfirm.pin} 在 ${shortDeviceName(clearBioConfirm.device.name)} 上的${clearBioConfirm.label}`
      );
      const devId = clearBioConfirm.device.id;
      setClearBioConfirm(null);
      const refreshed = await ZK.RefreshUsers([devId], true);
      onMatrixChange(refreshed);
    } catch (err: any) {
      onNotify('error', `清理失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  // ==================== Checkbox Selection Helpers ====================
  const toggleSelect = (pin: string) => {
    setSelectedPins((prev) => (prev.includes(pin) ? prev.filter((p) => p !== pin) : [...prev, pin]));
  };

  const toggleSelectAll = (rows: MergedUserRow[]) => {
    const visible = rows.map((r) => r.pin);
    const allSelected = visible.length > 0 && visible.every((p) => selectedPins.includes(p));
    if (allSelected) {
      setSelectedPins((prev) => prev.filter((p) => !visible.includes(p)));
    } else {
      setSelectedPins(Array.from(new Set([...selectedPins, ...visible])));
    }
  };

  const activeStats = activeDevice ? perDeviceStats[activeDevice.id] : null;
  const activeConnected = activeDevice ? !!statuses[activeDevice.id]?.connected : false;

  const pushCountPreview = pushModal
    ? pushModal.allowScopeToggle
      ? pushModal.pushScope === 'pending'
        ? pushModal.pendingPins.length
        : pushModal.allActivePins.length
      : pushModal.pins.length
    : 0;

  return (
    <>
      {/* ==================== Top 3-Tab Switcher (Scales to Unlimited Devices) ==================== */}
      <div className="users-mode-bar" role="tablist" aria-label="人员管理视角切换">
        <button
          type="button"
          role="tab"
          aria-selected={perspective === 'local'}
          className={`mode-tab ${perspective === 'local' ? 'active' : ''}`}
          onClick={() => switchPerspective('local')}
        >
          <Database size={14} />
          本地人员库
          <span className="mode-tab-count">{matrix.masterCount || 0}</span>
        </button>

        <button
          type="button"
          role="tab"
          aria-selected={perspective === 'diff'}
          className={`mode-tab ${perspective === 'diff' ? 'active' : ''}`}
          onClick={() => switchPerspective('diff')}
        >
          <Layers size={14} />
          人员比对
          {(matrix.diffUsers || 0) > 0 ? (
            <span className="mode-tab-count warn">{matrix.diffUsers} 差异</span>
          ) : (
            <span className="mode-tab-count ok">一致</span>
          )}
        </button>

        <button
          type="button"
          role="tab"
          aria-selected={perspective === 'device'}
          className={`mode-tab ${perspective === 'device' ? 'active' : ''}`}
          onClick={() => switchPerspective('device')}
        >
          <MonitorSmartphone size={14} />
          单机名单
          <span className="mode-tab-count">{enabledDevices.length} 台</span>
        </button>

        <div style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 12, color: 'var(--text-muted)' }}>
          <span>
            已连接 <strong style={{ color: connectedDeviceIds.length > 0 ? 'var(--success)' : 'var(--text-secondary)' }}>{connectedDeviceIds.length}</strong>/{enabledDevices.length} 台
          </span>
          {connectedDeviceIds.length === 0 && (
            <button type="button" className="btn btn-sm btn-ghost" onClick={onGoToDevices}>
              去连接 <ArrowRight size={12} />
            </button>
          )}
        </div>
      </div>

      {/* ==================== TAB 1 (DEFAULT): 本地人员库 ==================== */}
      {perspective === 'local' && (
        <>
          <div className="card logs-control-card">
            <div className="logs-control-row">
              <div className="toolbar-row" style={{ gap: 6 }}>
                <button type="button" className="btn btn-primary btn-sm" disabled={busy} onClick={openNewUser}>
                  <UserPlus size={13} /> 新增员工
                </button>

                <button
                  type="button"
                  className="btn btn-sm"
                  disabled={busy || connectedDeviceIds.length === 0}
                  onClick={() => openMultiPullModal()}
                  title={
                    connectedDeviceIds.length === 0
                      ? '请先在「设备总览」中连接至少一台考勤机'
                      : '手动选择考勤机，将其上的人员资料、指纹与人脸拉取到本地主库（只读不改机器）'
                  }
                >
                  <ArrowDownToLine size={13} />
                  从考勤机拉取
                  {selectedPins.length > 0 ? ` (${selectedPins.length} 人)` : ''}
                </button>

                <button
                  type="button"
                  className="btn btn-sm"
                  disabled={busy || connectedDeviceIds.length === 0 || localStats.active === 0}
                  onClick={() => openMultiPushModal()}
                  title={
                    connectedDeviceIds.length === 0
                      ? '请先在「设备总览」中连接至少一台考勤机'
                      : '手动选择目标考勤机，将已勾选或本地在职员工上传到指定设备'
                  }
                >
                  <ArrowUpFromLine size={13} />
                  上传到考勤机
                  {selectedPins.length > 0 ? ` (${selectedPins.length} 人)` : ''}
                </button>

                {(selectedPins.length > 0 || (matrix.resignedInDev || 0) > 0) && (
                  <button
                    type="button"
                    className="btn btn-sm"
                    style={{ color: 'var(--danger)' }}
                    disabled={busy || connectedDeviceIds.length === 0}
                    onClick={() => openMultiDeleteModal()}
                    title="手动选择考勤机移除人员（本地主库与历史考勤保留不变）"
                  >
                    <Trash2 size={13} />
                    {selectedPins.length > 0
                      ? `从考勤机移除 (${selectedPins.length})`
                      : `清理离职残留 (${matrix.resignedInDev})`}
                  </button>
                )}
              </div>

              <div className="toolbar-row" style={{ gap: 6, flexShrink: 0 }}>
                {connectedDeviceIds.length === 0 ? (
                  <button type="button" className="btn btn-sm" onClick={onGoToDevices}>
                    去设备总览连接 <ArrowRight size={13} />
                  </button>
                ) : (
                  <button
                    type="button"
                    className="btn btn-sm"
                    disabled={busy}
                    onClick={() => handleScanDevices(connectedDeviceIds)}
                    title="只读刷新已连接考勤机的名单快照，更新下方「所在考勤机」分布"
                  >
                    <RefreshCw size={13} className={busy ? 'spin' : ''} /> 刷新设备名单 ({connectedDeviceIds.length})
                  </button>
                )}

                <button
                  type="button"
                  className="btn btn-sm"
                  disabled={busy}
                  {...({ popoverTarget: localToolsPopoverId } as Record<string, string>)}
                >
                  <SlidersHorizontal size={13} /> 导入 / 备份 <ChevronDown size={13} />
                </button>

                <div
                  id={localToolsPopoverId}
                  ref={localToolsPopoverRef}
                  {...({ popover: 'auto' } as Record<string, string>)}
                  className="export-popover"
                  style={{ minWidth: 200 }}
                >
                  <button type="button" className="export-popover-item" onClick={handleImportProfiles}>
                    <FileSpreadsheet size={14} />
                    <span>导入部门 / 离职表</span>
                    <span className="export-popover-sub">XLSX/CSV</span>
                  </button>
                  <button
                    type="button"
                    className="export-popover-item"
                    disabled={localStats.total === 0}
                    onClick={handleExportMasterExcel}
                  >
                    <Download size={14} />
                    <span>导出本地花名册</span>
                    <span className="export-popover-sub">Excel</span>
                  </button>
                  <div className="export-popover-divider" />
                  <button
                    type="button"
                    className="export-popover-item"
                    disabled={localStats.total === 0}
                    onClick={handleExportMasterBackup}
                  >
                    <HardDriveDownload size={14} />
                    <span>备份本地主库</span>
                    <span className="export-popover-sub">JSON</span>
                  </button>
                  <button type="button" className="export-popover-item" onClick={handleImportMasterBackup}>
                    <HardDriveUpload size={14} />
                    <span>从备份恢复主库</span>
                    <span className="export-popover-sub">JSON</span>
                  </button>
                  <div className="export-popover-divider" />
                  <button
                    type="button"
                    className="export-popover-item"
                    onClick={() => {
                      hideLocalToolsPopover();
                      void ZK.OpenDataDirectory();
                    }}
                  >
                    <FolderOpen size={14} />
                    <span>打开本地数据目录</span>
                  </button>
                </div>
              </div>
            </div>

            <div className="logs-control-footer">
              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                各考勤机人员名单相互独立，无需强制全机一致；点击「从考勤机拉取」或「上传到考勤机」可手动勾选具体设备。
              </span>
              {matrix.masterUpdatedAt && (
                <span style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>
                  主库更新：{matrix.masterUpdatedAt.slice(5)}
                </span>
              )}
            </div>
          </div>

          <div className="toolbar-row">
            <div className="search-box">
              <Search size={14} />
              <input
                className="input"
                placeholder="搜索工号 / 姓名 / 部门 / 卡号 / 备注..."
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>

            <select
              className="select"
              aria-label="按所在考勤机筛选"
              value={localDeviceFilter}
              onChange={(e) => setLocalDeviceFilter(e.target.value)}
            >
              <option value="">全部考勤机范围</option>
              {enabledDevices.map((d) => {
                const st = perDeviceStats[d.id];
                return (
                  <option key={d.id} value={d.id}>
                    仅看在「{shortDeviceName(d.name)}」({st?.hasSnapshot ? `${st.present}人` : '未扫描'})
                  </option>
                );
              })}
              <option value="__unassigned__">暂未分配到任何已扫描考勤机</option>
            </select>

            <div className="segmented" role="group" aria-label="本地人员库筛选">
              <button type="button" className={localFilter === 'all' ? 'active' : ''} onClick={() => setLocalFilter('all')}>
                全部 ({localStats.total})
              </button>
              <button type="button" className={localFilter === 'active' ? 'active' : ''} onClick={() => setLocalFilter('active')}>
                在职 ({localStats.active})
              </button>
              <button type="button" className={localFilter === 'resigned' ? 'active' : ''} onClick={() => setLocalFilter('resigned')}>
                离职 ({localStats.resigned})
              </button>
              <button type="button" className={localFilter === 'no_face' ? 'active' : ''} onClick={() => setLocalFilter('no_face')}>
                缺人脸 ({localStats.noFace})
              </button>
              <button type="button" className={localFilter === 'no_fp' ? 'active' : ''} onClick={() => setLocalFilter('no_fp')}>
                缺指纹 ({localStats.noFp})
              </button>
            </div>

            {showDeptColumn && (
              <select className="select" aria-label="按部门筛选" value={filterDept} onChange={(e) => setFilterDept(e.target.value)}>
                <option value="">全部部门</option>
                {(matrix.departments || []).map((dept) => (
                  <option key={dept} value={dept}>{dept}</option>
                ))}
              </select>
            )}

            <span style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--text-muted)' }}>
              显示 <strong style={{ color: 'var(--text-primary)' }}>{localFilteredRows.length}</strong> 人
              {selectedPins.length > 0 && (
                <>
                  {' · '}已选 <strong style={{ color: 'var(--accent)' }}>{selectedPins.length}</strong> 人
                  <button
                    type="button"
                    className="btn btn-sm btn-ghost"
                    style={{ marginLeft: 4, height: 22, padding: '0 6px' }}
                    onClick={() => setSelectedPins([])}
                  >
                    清空
                  </button>
                </>
              )}
            </span>
          </div>

          <div className="table-card">
            {localFilteredRows.length === 0 ? (
              <div className="empty-state">
                <Database size={34} />
                <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text-primary)' }}>
                  {localStats.total === 0 ? '本地人员主库为空' : '没有符合条件的本地人员'}
                </div>
                <div style={{ fontSize: 12.5 }}>
                  {localStats.total === 0
                    ? '你可以直接新增员工，或点击上方「从考勤机拉取」选择指定考勤机导入本地人员库。'
                    : '请调整搜索词、所在考勤机或筛选标签。'}
                </div>
                {localStats.total === 0 && (
                  <div className="toolbar-row" style={{ marginTop: 4 }}>
                    <button type="button" className="btn btn-primary btn-sm" onClick={openNewUser}>
                      <UserPlus size={13} /> 新增员工
                    </button>
                    {connectedDeviceIds.length > 0 ? (
                      <button type="button" className="btn btn-sm" onClick={() => openMultiPullModal({ forceAll: true })}>
                        <ArrowDownToLine size={13} /> 从考勤机拉取建库
                      </button>
                    ) : (
                      <button type="button" className="btn btn-sm" onClick={onGoToDevices}>
                        去设备总览连接 <ArrowRight size={13} />
                      </button>
                    )}
                  </div>
                )}
              </div>
            ) : (
              <div tabIndex={0} role="region" aria-label="本地人员主库列表" className="table-wrapper">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th style={{ width: 34 }}>
                        <input
                          type="checkbox"
                          aria-label="全选当前列表"
                          checked={
                            localFilteredRows.length > 0 &&
                            localFilteredRows.every((r) => selectedPins.includes(r.pin))
                          }
                          onChange={() => toggleSelectAll(localFilteredRows)}
                        />
                      </th>
                      <th className="cell-nowrap" style={{ width: 72 }}>工号</th>
                      <th className="cell-nowrap" style={{ minWidth: 118 }}>姓名</th>
                      {showDeptColumn && <th className="cell-nowrap" style={{ width: 112 }}>部门</th>}
                      <th className="cell-nowrap" style={{ width: 72 }}>状态</th>
                      <th className="cell-nowrap" style={{ minWidth: 160 }}>本地生物资产</th>
                      <th className="cell-nowrap" style={{ minWidth: 210 }}>所在考勤机</th>
                      {hasAnyNote && <th style={{ minWidth: 120 }}>备注</th>}
                      <th className="cell-nowrap" style={{ width: 108 }}>最后更新</th>
                      <th className="cell-nowrap" style={{ width: 108, textAlign: 'right' }}>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {localFilteredRows.map((row) => {
                      const sum = summarizeRowAcrossDevices(row);
                      const visibleDevs = sum.presentDevices.slice(0, 3);
                      const moreCount = sum.presentDevices.length > 3 ? sum.presentDevices.length - 3 : 0;
                      return (
                        <tr key={row.pin}>
                          <td>
                            <input
                              type="checkbox"
                              aria-label={`勾选工号 ${row.pin}`}
                              checked={selectedPins.includes(row.pin)}
                              onChange={() => toggleSelect(row.pin)}
                            />
                          </td>
                          <td className="mono cell-nowrap" style={{ fontWeight: 600 }}>{row.pin}</td>
                          <td className="cell-nowrap">
                            <div style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                              <span style={{ fontWeight: 600 }}>
                                {row.primaryName || <span style={{ color: 'var(--text-muted)' }}>（无姓名）</span>}
                              </span>
                              {row.privilege === 14 && <span className="badge badge-warn">管理员</span>}
                              {row.card && <span className="mono" style={{ fontSize: 11, color: 'var(--text-muted)' }}>卡:{row.card}</span>}
                            </div>
                          </td>
                          {showDeptColumn && (
                            <td className="cell-nowrap">
                              {row.department ? (
                                <span className="badge badge-neutral">{row.department}</span>
                              ) : (
                                <span style={{ color: 'var(--text-muted)' }}>—</span>
                              )}
                            </td>
                          )}
                          <td className="cell-nowrap">
                            {row.workStatus === 'resigned' ? (
                              <span className="badge badge-danger">离职</span>
                            ) : (
                              <span className="badge badge-ok">在职</span>
                            )}
                          </td>
                          <td className="cell-nowrap">
                            <div style={{ display: 'inline-flex', alignItems: 'center', gap: 10, fontSize: 12 }}>
                              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 3, color: row.hasLocalFace ? 'var(--accent)' : 'var(--text-muted)' }}>
                                <Camera size={12} /> {row.hasLocalFace ? '有人脸' : '无人脸'}
                              </span>
                              <span
                                style={{ display: 'inline-flex', alignItems: 'center', gap: 3, color: (row.masterFpCount || 0) > 0 ? 'var(--text-primary)' : 'var(--text-muted)' }}
                                title={(row.masterFpIndices || []).length > 0 ? `手指槽位：${(row.masterFpIndices || []).join(', ')}` : '尚未拉取指纹模板'}
                              >
                                <Fingerprint size={12} />
                                {(row.masterFpCount || 0) > 0 ? `${row.masterFpCount} 枚` : '0 枚'}
                              </span>
                            </div>
                          </td>
                          <td>
                            <button
                              type="button"
                              className="dev-summary-trigger"
                              onClick={() => setEmpSyncPin(row.pin)}
                              title="点击查看该员工在各考勤机的注册情况，并手动上传/拉取/移除"
                            >
                              {sum.scanned === 0 ? (
                                <span className="badge badge-neutral">尚未扫描设备</span>
                              ) : sum.presentDevices.length === 0 ? (
                                <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>暂未下发到已扫描设备</span>
                              ) : (
                                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, flexWrap: 'wrap' }}>
                                  {visibleDevs.map(({ dev, status }) => {
                                    const cls =
                                      status === 'resigned_present'
                                        ? 'badge-danger'
                                        : status === 'need_push'
                                          ? 'badge-info'
                                          : status === 'need_pull' || status === 'both'
                                            ? 'badge-warn'
                                            : 'badge-neutral';
                                    const mark =
                                      status === 'resigned_present'
                                        ? '✕ '
                                        : status === 'need_push'
                                          ? '↑ '
                                          : status === 'need_pull'
                                            ? '↓ '
                                            : status === 'both'
                                              ? '⇅ '
                                              : '';
                                    return (
                                      <span key={dev.id} className={`badge ${cls}`}>
                                        {mark}
                                        {shortDeviceName(dev.name)}
                                      </span>
                                    );
                                  })}
                                  {moreCount > 0 && (
                                    <span className="badge badge-neutral">+{moreCount} 台</span>
                                  )}
                                </span>
                              )}
                            </button>
                          </td>
                          {hasAnyNote && (
                            <td style={{ fontSize: 12, color: 'var(--text-secondary)', maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={row.profileNote || ''}>
                              {row.profileNote || '—'}
                            </td>
                          )}
                          <td className="mono cell-nowrap" style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>
                            {row.masterUpdatedAt ? row.masterUpdatedAt.slice(5, 16) : '—'}
                          </td>
                          <td className="cell-nowrap" style={{ textAlign: 'right' }}>
                            <button
                              type="button"
                              className="btn btn-sm btn-ghost"
                              title="查看/分配考勤机"
                              onClick={() => setEmpSyncPin(row.pin)}
                            >
                              <Layers size={13} />
                            </button>
                            <button type="button" className="btn btn-sm btn-ghost" title="编辑本地档案" onClick={() => openEditUser(row)}>
                              <Pencil size={13} />
                            </button>
                            <button
                              type="button"
                              className="btn btn-sm btn-ghost"
                              style={{ color: 'var(--danger)' }}
                              title="从本地主库移除（不影响考勤机）"
                              onClick={() => setDeleteMasterPin(row.pin)}
                            >
                              <Trash2 size={13} />
                            </button>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      )}

      {/* ==================== TAB 2: 人员比对（仅显示真正不一致的项，不把未分配设备算作差异） ==================== */}
      {perspective === 'diff' && (
        <>
          <div className="card logs-control-card">
            <div className="logs-control-row">
              <div className="toolbar-row" style={{ gap: 8 }}>
                <span className="logs-row-label">聚焦设备</span>
                <select
                  className="select"
                  aria-label="按设备筛选比对差异"
                  value={diffDeviceFilter}
                  onChange={(e) => setDiffDeviceFilter(e.target.value)}
                >
                  <option value="">全部考勤机 ({enabledDevices.length} 台)</option>
                  {enabledDevices.map((d) => {
                    const st = perDeviceStats[d.id];
                    const conn = statuses[d.id]?.connected;
                    return (
                      <option key={d.id} value={d.id}>
                        {conn ? '● ' : '○ '}
                        {shortDeviceName(d.name)}
                        {st?.hasSnapshot ? ` (${st.diff > 0 ? `${st.diff} 差异` : '一致'})` : ' (未扫描)'}
                      </option>
                    );
                  })}
                </select>

                <button
                  type="button"
                  className="btn btn-sm"
                  disabled={busy || connectedDeviceIds.length === 0}
                  onClick={() =>
                    openMultiPullModal({
                      preselectDeviceIds: diffDeviceFilter ? [diffDeviceFilter] : [],
                    })
                  }
                >
                  <ArrowDownToLine size={13} />
                  从考勤机拉取
                  {selectedPins.length > 0 ? ` (${selectedPins.length})` : ''}
                </button>

                <button
                  type="button"
                  className="btn btn-sm"
                  disabled={busy || connectedDeviceIds.length === 0 || (selectedPins.length === 0 && (matrix.pendingPushCount || 0) === 0)}
                  onClick={() =>
                    openMultiPushModal({
                      preselectDeviceIds: diffDeviceFilter ? [diffDeviceFilter] : [],
                    })
                  }
                >
                  <ArrowUpFromLine size={13} />
                  上传更新到考勤机
                  {selectedPins.length > 0 ? ` (${selectedPins.length})` : ''}
                </button>

                {(matrix.resignedInDev || 0) > 0 && (
                  <button
                    type="button"
                    className="btn btn-sm"
                    style={{ color: 'var(--danger)' }}
                    disabled={busy || connectedDeviceIds.length === 0}
                    onClick={() =>
                      openMultiDeleteModal({
                        preselectDeviceIds: diffDeviceFilter ? [diffDeviceFilter] : [],
                      })
                    }
                  >
                    <Trash2 size={13} /> 清理离职残留 ({matrix.resignedInDev})
                  </button>
                )}
              </div>

              <div className="toolbar-row" style={{ gap: 6, flexShrink: 0 }}>
                {connectedDeviceIds.length === 0 ? (
                  <button type="button" className="btn btn-sm" onClick={onGoToDevices}>
                    去设备总览连接 <ArrowRight size={13} />
                  </button>
                ) : (
                  <button
                    type="button"
                    className="btn btn-primary btn-sm"
                    disabled={busy}
                    onClick={() =>
                      handleScanDevices(
                        diffDeviceFilter && statuses[diffDeviceFilter]?.connected
                          ? [diffDeviceFilter]
                          : connectedDeviceIds
                      )
                    }
                  >
                    <RefreshCw size={13} className={busy ? 'spin' : ''} />
                    {diffDeviceFilter && statuses[diffDeviceFilter]?.connected
                      ? `刷新 ${shortDeviceName(enabledDevices.find((d) => d.id === diffDeviceFilter)?.name || '')}`
                      : `刷新已连接设备 (${connectedDeviceIds.length})`}
                  </button>
                )}
              </div>
            </div>

            <div className="logs-control-footer">
              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                仅比对 3 类情况：① 考勤机上有但本地未收录 / 有新指纹人脸；② 考勤机已有此人但姓名/人脸/指纹待更新；③ 离职残留。员工不在某台机上属正常情况，不报异常。
              </span>
              <span style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>
                已扫描 {scannedDeviceCount}/{enabledDevices.length} 台
              </span>
            </div>
          </div>

          <div className="toolbar-row">
            <div className="search-box">
              <Search size={14} />
              <input
                className="input"
                placeholder="搜索工号 / 姓名 / 部门 / 卡号..."
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>

            <div className="segmented" role="group" aria-label="人员比对筛选">
              <button type="button" className={diffFilter === 'diff' ? 'active' : ''} onClick={() => setDiffFilter('diff')}>
                全部差异 ({matrix.diffUsers || 0})
              </button>
              <button type="button" className={diffFilter === 'pull' ? 'active' : ''} onClick={() => setDiffFilter('pull')}>
                ↓ 未入库 / 待拉取 ({matrix.pendingPullCount || 0})
              </button>
              <button type="button" className={diffFilter === 'push' ? 'active' : ''} onClick={() => setDiffFilter('push')}>
                ↑ 已有人员资料待更新 ({matrix.pendingPushCount || 0})
              </button>
              <button type="button" className={diffFilter === 'resigned' ? 'active' : ''} onClick={() => setDiffFilter('resigned')}>
                ✕ 离职残留 ({matrix.resignedInDev || 0})
              </button>
            </div>

            {showDeptColumn && (
              <select className="select" aria-label="按部门筛选" value={filterDept} onChange={(e) => setFilterDept(e.target.value)}>
                <option value="">全部部门</option>
                {(matrix.departments || []).map((dept) => (
                  <option key={dept} value={dept}>{dept}</option>
                ))}
              </select>
            )}

            <span style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--text-muted)' }}>
              显示 <strong style={{ color: 'var(--text-primary)' }}>{diffFilteredRows.length}</strong> 人
            </span>
          </div>

          <div className="table-card">
            {diffFilteredRows.length === 0 ? (
              <div className="empty-state">
                <CheckCircle2 size={34} style={{ color: 'var(--success)' }} />
                <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text-primary)' }}>
                  {!query && !filterDept ? '已扫描考勤机上的现有人员信息均已比对一致' : '没有符合筛选条件的记录'}
                </div>
                <div style={{ fontSize: 12.5 }}>
                  {scannedDeviceCount < enabledDevices.length
                    ? `还有 ${enabledDevices.length - scannedDeviceCount} 台设备尚未扫描，连接后点击「刷新已连接设备」即可比对。`
                    : '回到「本地人员库」可随时手动向指定考勤机上传新员工或拉取名单。'}
                </div>
              </div>
            ) : (
              <div tabIndex={0} role="region" aria-label="人员比对列表" className="table-wrapper">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th style={{ width: 34 }}>
                        <input
                          type="checkbox"
                          aria-label="全选当前列表"
                          checked={
                            diffFilteredRows.length > 0 &&
                            diffFilteredRows.every((r) => selectedPins.includes(r.pin))
                          }
                          onChange={() => toggleSelectAll(diffFilteredRows)}
                        />
                      </th>
                      <th className="cell-nowrap" style={{ width: 72 }}>工号</th>
                      <th style={{ width: 225, minWidth: 200 }}>本地人员主库（基准）</th>
                      <th>存在差异的考勤机</th>
                      <th className="cell-nowrap" style={{ width: 126, textAlign: 'right' }}>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {diffFilteredRows.map((row) => {
                      const sum = summarizeRowAcrossDevices(row);
                      return (
                        <tr key={row.pin}>
                          <td>
                            <input
                              type="checkbox"
                              aria-label={`勾选工号 ${row.pin}`}
                              checked={selectedPins.includes(row.pin)}
                              onChange={() => toggleSelect(row.pin)}
                            />
                          </td>
                          <td className="mono cell-nowrap" style={{ fontWeight: 600 }}>{row.pin}</td>
                          <td>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                              <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                                <span style={{ fontWeight: 600 }}>
                                  {row.primaryName || <span style={{ color: 'var(--text-muted)' }}>（无姓名）</span>}
                                </span>
                                {!row.inMaster && <span className="badge badge-warn">未入主库</span>}
                                {row.workStatus === 'resigned' && <span className="badge badge-danger">离职</span>}
                                {row.department && <span className="badge badge-neutral">{row.department}</span>}
                              </div>
                              <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 11, color: 'var(--text-muted)' }}>
                                <span>{row.hasLocalFace ? '主库有人脸' : '无主库人脸'}</span>
                                <span>指纹×{row.masterFpCount || 0}</span>
                                {row.card && <span className="mono">卡:{row.card}</span>}
                              </div>
                            </div>
                          </td>
                          <td>
                            <div className="diff-device-chips">
                              {sum.diffDevices.map(({ dev, status, detail }) => {
                                const cls =
                                  status === 'resigned_present'
                                    ? 'badge-danger'
                                    : status === 'need_push'
                                      ? 'badge-info'
                                      : 'badge-warn';
                                const icon =
                                  status === 'resigned_present'
                                    ? '✕'
                                    : status === 'need_push'
                                      ? '↑'
                                      : status === 'need_pull'
                                        ? '↓'
                                        : '⇅';
                                return (
                                  <button
                                    key={dev.id}
                                    type="button"
                                    className={`badge ${cls} diff-chip-btn`}
                                    title={`${dev.name}：${detail || status}（点击查看并处理）`}
                                    onClick={() => setEmpSyncPin(row.pin)}
                                  >
                                    {icon} {shortDeviceName(dev.name)}
                                    {detail ? ` · ${detail.split('、')[0]}` : ''}
                                  </button>
                                );
                              })}
                            </div>
                          </td>
                          <td className="cell-nowrap" style={{ textAlign: 'right' }}>
                            <button
                              type="button"
                              className="btn btn-sm"
                              onClick={() => setEmpSyncPin(row.pin)}
                            >
                              查看处理
                            </button>
                            <button
                              type="button"
                              className="btn btn-sm btn-ghost"
                              style={{ marginLeft: 4 }}
                              title="编辑本地主库档案"
                              onClick={() => openEditUser(row)}
                            >
                              <Pencil size={13} />
                            </button>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      )}

      {/* ==================== TAB 3: 单机名单（按设备查看独立名单） ==================== */}
      {perspective === 'device' && activeDevice && activeStats && (
        <>
          <div className="card logs-control-card">
            <div className="logs-control-row" style={{ gap: 10 }}>
              <div className="toolbar-row" style={{ gap: 8 }}>
                <span className="logs-row-label">当前考勤机</span>
                <select
                  className="select"
                  aria-label="选择要查看的考勤机"
                  style={{ fontWeight: 600, minWidth: 280 }}
                  value={activeDevice.id}
                  onChange={(e) => {
                    setActiveDeviceId(e.target.value);
                    setSelectedPins([]);
                  }}
                >
                  {enabledDevices.map((d) => {
                    const st = perDeviceStats[d.id];
                    const conn = statuses[d.id]?.connected;
                    return (
                      <option key={d.id} value={d.id}>
                        {conn ? '● ' : '○ '}
                        {shortDeviceName(d.name)} · {d.ip}
                        {st?.hasSnapshot ? ` · ${st.present} 人` : ' · 未扫描'}
                      </option>
                    );
                  })}
                </select>
                <span className={`badge ${activeConnected ? 'badge-ok' : 'badge-neutral'}`}>
                  {activeConnected ? '已连接' : '未连接'}
                </span>
              </div>

              <div className="toolbar-row" style={{ gap: 6, flexShrink: 0 }}>
                <span className="badge badge-neutral">
                  本机 {activeStats.present} 人 / 本地主库 {matrix.masterCount || 0} 人
                </span>
                {activeConnected ? (
                  <button
                    type="button"
                    className="btn btn-sm"
                    disabled={busy}
                    onClick={() => handleScanDevices([activeDevice.id])}
                  >
                    <RefreshCw size={13} className={busy ? 'spin' : ''} /> 刷新本机
                  </button>
                ) : (
                  <button type="button" className="btn btn-sm" onClick={onGoToDevices}>
                    去连接 <ArrowRight size={13} />
                  </button>
                )}
              </div>
            </div>

            <div className="logs-control-footer">
              <div className="toolbar-row" style={{ gap: 6 }}>
                <button
                  type="button"
                  className="btn btn-sm btn-primary"
                  disabled={busy || !activeConnected}
                  onClick={() =>
                    openMultiPullModal({
                      preselectDeviceIds: [activeDevice.id],
                      forceAll: selectedPins.length === 0 && activeStats.pull === 0,
                    })
                  }
                  title={activeConnected ? `从 ${activeDevice.name} 拉取人员资料、指纹和人脸到本地主库` : '请先在「设备总览」中连接该考勤机'}
                >
                  <ArrowDownToLine size={13} />
                  从本机拉取到本地
                  {selectedPins.length > 0 ? ` (${selectedPins.length})` : ''}
                </button>

                <button
                  type="button"
                  className="btn btn-sm"
                  disabled={busy || !activeConnected || localStats.active === 0}
                  onClick={() => openMultiPushModal({ preselectDeviceIds: [activeDevice.id] })}
                  title={activeConnected ? `将勾选的本地员工上传到 ${activeDevice.name}` : '请先在「设备总览」中连接该考勤机'}
                >
                  <ArrowUpFromLine size={13} />
                  上传本地到本机
                  {selectedPins.length > 0 ? ` (${selectedPins.length})` : ''}
                </button>

                <button
                  type="button"
                  className="btn btn-sm"
                  style={{ color: 'var(--danger)' }}
                  disabled={busy || !activeConnected || (selectedPins.length === 0 && activeStats.resigned === 0)}
                  onClick={() => openMultiDeleteModal({ preselectDeviceIds: [activeDevice.id] })}
                  title={activeConnected ? `从 ${activeDevice.name} 移除人员（保留本地主库）` : '请先在「设备总览」中连接该考勤机'}
                >
                  <Trash2 size={13} />
                  {selectedPins.length > 0
                    ? `从本机移除 (${selectedPins.length})`
                    : `清理离职残留 (${activeStats.resigned})`}
                </button>
              </div>

              <span style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>
                {activeDevice.name} (<span className="mono">{activeDevice.ip}</span>)
                {!activeConnected && ' · 未连接'}
              </span>
            </div>
          </div>

          {activeConnected && !activeStats.hasSnapshot && (
            <div className="notice-bar info" role="status">
              <span>
                尚未扫描 <strong>{shortDeviceName(activeDevice.name)}</strong> 的机上人员名单。请点击「立即扫描本机」读取名单（只读操作）。
              </span>
              <button type="button" className="btn btn-primary btn-sm" disabled={busy} onClick={() => handleScanDevices([activeDevice.id])}>
                <RefreshCw size={13} /> 立即扫描本机
              </button>
            </div>
          )}

          <div className="toolbar-row">
            <div className="search-box">
              <Search size={14} />
              <input
                className="input"
                placeholder="搜索工号 / 姓名 / 部门 / 卡号..."
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>

            <div className="segmented" role="group" aria-label="单机名单筛选">
              <button type="button" className={devFilter === 'present' ? 'active' : ''} onClick={() => setDevFilter('present')}>
                本机已有 ({activeStats.present})
              </button>
              <button type="button" className={devFilter === 'not_assigned' ? 'active' : ''} onClick={() => setDevFilter('not_assigned')}>
                未在本机 ({activeStats.notAssigned})
              </button>
              <button type="button" className={devFilter === 'pull' ? 'active' : ''} onClick={() => setDevFilter('pull')}>
                ↓ 待拉取 ({activeStats.pull})
              </button>
              <button type="button" className={devFilter === 'push' ? 'active' : ''} onClick={() => setDevFilter('push')}>
                ↑ 资料待更新 ({activeStats.push})
              </button>
              <button type="button" className={devFilter === 'resigned' ? 'active' : ''} onClick={() => setDevFilter('resigned')}>
                ✕ 离职残留 ({activeStats.resigned})
              </button>
              <button type="button" className={devFilter === 'all' ? 'active' : ''} onClick={() => setDevFilter('all')}>
                全部 ({activeDevAllRows.length})
              </button>
            </div>

            {showDeptColumn && (
              <select className="select" aria-label="按部门筛选" value={filterDept} onChange={(e) => setFilterDept(e.target.value)}>
                <option value="">全部部门</option>
                {(matrix.departments || []).map((dept) => (
                  <option key={dept} value={dept}>{dept}</option>
                ))}
              </select>
            )}

            <span style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--text-muted)' }}>
              显示 <strong style={{ color: 'var(--text-primary)' }}>{activeDevFilteredRows.length}</strong> 人
              {selectedPins.length > 0 && ` · 已勾选 ${selectedPins.length} 人`}
            </span>
          </div>

          <div className="table-card">
            {activeDevFilteredRows.length === 0 ? (
              <div className="empty-state">
                <Users size={34} />
                <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text-primary)' }}>
                  当前筛选下没有记录
                </div>
                <div style={{ fontSize: 12.5 }}>
                  可切换上方筛选标签，或点击「刷新本机」读取该考勤机的最新名单。
                </div>
              </div>
            ) : (
              <div tabIndex={0} role="region" aria-label="单机人员名单" className="table-wrapper">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th style={{ width: 34 }}>
                        <input
                          type="checkbox"
                          aria-label="全选当前列表"
                          checked={
                            activeDevFilteredRows.length > 0 &&
                            activeDevFilteredRows.every((r) => selectedPins.includes(r.pin))
                          }
                          onChange={() => toggleSelectAll(activeDevFilteredRows)}
                        />
                      </th>
                      <th className="cell-nowrap" style={{ width: 72 }}>工号</th>
                      <th style={{ minWidth: 220 }}>本地人员主库（基准）</th>
                      <th style={{ width: 155 }}>与本机状态</th>
                      <th style={{ minWidth: 220 }}>{shortDeviceName(activeDevice.name)} 机上现状</th>
                      <th className="cell-nowrap" style={{ width: 136, textAlign: 'right' }}>单人操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {activeDevFilteredRows.map((row) => {
                      const cell = row.devices?.[activeDevice.id];
                      const st = cell?.syncStatus || 'no_snapshot';
                      const nameDiff = row.inMaster && cell?.present && row.primaryName && cell.name !== row.primaryName;
                      const privDiff = row.inMaster && cell?.present && cell.privilege !== row.privilege;
                      const fpMoreLocal = row.inMaster && cell?.present && (row.masterFpCount || 0) > (cell.fpCount || 0);
                      const fpMoreDev = row.inMaster && cell?.present && (cell.fpCount || 0) > (row.masterFpCount || 0);
                      const faceMoreLocal = row.inMaster && cell?.present && row.hasLocalFace && !cell.hasFace;
                      const faceMoreDev = row.inMaster && cell?.present && !row.hasLocalFace && cell.hasFace;

                      return (
                        <tr key={row.pin}>
                          <td>
                            <input
                              type="checkbox"
                              aria-label={`勾选工号 ${row.pin}`}
                              checked={selectedPins.includes(row.pin)}
                              onChange={() => toggleSelect(row.pin)}
                            />
                          </td>
                          <td className="mono cell-nowrap" style={{ fontWeight: 600 }}>{row.pin}</td>

                          <td>
                            {row.inMaster ? (
                              <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                                <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                                  <span style={{ fontWeight: 600 }}>{row.primaryName || '（无姓名）'}</span>
                                  {row.workStatus === 'resigned' && <span className="badge badge-danger">离职</span>}
                                  {row.department && <span className="badge badge-neutral">{row.department}</span>}
                                  {row.privilege === 14 && <span className="badge badge-warn">管理员</span>}
                                </div>
                                <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 11.5, color: 'var(--text-muted)' }}>
                                  <span className={faceMoreLocal ? 'diff-field-changed' : ''}>
                                    {row.hasLocalFace ? '人脸: 有' : '人脸: 无'}
                                  </span>
                                  <span className={fpMoreLocal ? 'diff-field-changed' : ''}>
                                    指纹: {row.masterFpCount || 0} 枚
                                  </span>
                                  {row.card && <span className="mono">卡:{row.card}</span>}
                                </div>
                              </div>
                            ) : (
                              <span className="badge badge-warn">本地主库尚未收录</span>
                            )}
                          </td>

                          <td>
                            {st === 'synced' && (
                              <span className="badge badge-ok"><CheckCircle2 size={10} /> 已注册 · 一致</span>
                            )}
                            {st === 'not_assigned' && (
                              <span className="badge badge-neutral">未在此机</span>
                            )}
                            {st === 'no_snapshot' && <span className="badge badge-neutral">未扫描</span>}
                            {st === 'need_push' && (
                              <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                                <span className="badge badge-info" style={{ width: 'fit-content' }}>↑ 资料待更新</span>
                                <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
                                  {(cell?.diffDetails || []).join('、')}
                                </span>
                              </div>
                            )}
                            {st === 'need_pull' && (
                              <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                                <span className="badge badge-warn" style={{ width: 'fit-content' }}>↓ 待拉取</span>
                                <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
                                  {(cell?.diffDetails || []).join('、')}
                                </span>
                              </div>
                            )}
                            {st === 'both' && (
                              <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                                <span className="badge badge-warn" style={{ width: 'fit-content' }}>⇅ 双向有更新</span>
                                <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
                                  {(cell?.diffDetails || []).join('、')}
                                </span>
                              </div>
                            )}
                            {st === 'resigned_present' && <span className="badge badge-danger">✕ 离职残留</span>}
                          </td>

                          <td>
                            {cell?.present ? (
                              <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                                <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                                  <span style={{ fontWeight: 600 }} className={nameDiff ? 'diff-field-changed' : ''}>
                                    {cell.name || '（无姓名）'}
                                  </span>
                                  <span style={{ fontSize: 11.5, color: 'var(--text-muted)' }} className={privDiff ? 'diff-field-changed' : ''}>
                                    {privilegeLabel(cell.privilege)}
                                  </span>
                                </div>
                                <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 11.5, color: 'var(--text-muted)' }}>
                                  <span className={faceMoreDev ? 'diff-field-new' : ''}>
                                    {cell.hasFace ? '人脸: 有' : '人脸: 无'}
                                  </span>
                                  <span className={fpMoreDev ? 'diff-field-new' : ''}>
                                    指纹: {cell.fpCount || 0} 枚
                                  </span>
                                  {cell.card && <span className="mono">卡:{cell.card}</span>}
                                </div>
                              </div>
                            ) : (
                              <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>
                                {st === 'no_snapshot' ? '—' : '未在此机注册'}
                              </span>
                            )}
                          </td>

                          <td className="cell-nowrap" style={{ textAlign: 'right' }}>
                            <button
                              type="button"
                              className="btn btn-sm btn-ghost"
                              title="编辑本地主库 / 清理单项生物特征"
                              onClick={() => openEditUser(row, activeDevice.id)}
                            >
                              <Pencil size={13} />
                            </button>
                            {row.inMaster && row.workStatus !== 'resigned' && (
                              <button
                                type="button"
                                className="btn btn-sm btn-ghost"
                                style={{ color: 'var(--accent)' }}
                                disabled={busy || !activeConnected}
                                title={
                                  activeConnected
                                    ? cell?.present
                                      ? `将本地资料更新到 ${shortDeviceName(activeDevice.name)}`
                                      : `将此员工添加到 ${shortDeviceName(activeDevice.name)}`
                                    : '请先连接该考勤机'
                                }
                                onClick={() => openMultiPushModal({ pins: [row.pin], preselectDeviceIds: [activeDevice.id] })}
                              >
                                <ArrowUpFromLine size={13} />
                              </button>
                            )}
                            {cell?.present && (
                              <button
                                type="button"
                                className="btn btn-sm btn-ghost"
                                style={{ color: 'var(--warn)' }}
                                disabled={busy || !activeConnected}
                                title={activeConnected ? `从 ${shortDeviceName(activeDevice.name)} 拉取到本地主库` : '请先连接该考勤机'}
                                onClick={() => openMultiPullModal({ pins: [row.pin], preselectDeviceIds: [activeDevice.id] })}
                              >
                                <ArrowDownToLine size={13} />
                              </button>
                            )}
                            {cell?.present && (
                              <button
                                type="button"
                                className="btn btn-sm btn-ghost"
                                style={{ color: 'var(--danger)' }}
                                disabled={busy || !activeConnected}
                                title={activeConnected ? `从 ${shortDeviceName(activeDevice.name)} 移除` : '请先连接该考勤机'}
                                onClick={() => openMultiDeleteModal({ pins: [row.pin], preselectDeviceIds: [activeDevice.id] })}
                              >
                                <Trash2 size={13} />
                              </button>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      )}

      {/* ==================== Employee Terminal Distribution Modal ==================== */}
      <ModalDialog
        open={!!empSyncRow}
        onClose={() => setEmpSyncPin(null)}
        lightDismiss
        title={empSyncRow ? `${empSyncRow.primaryName || `工号 ${empSyncRow.pin}`} (#${empSyncRow.pin}) · 考勤机分布` : ''}
        width={680}
        footer={
          empSyncRow && (
            <>
              <button
                type="button"
                className="btn btn-sm"
                style={{ marginRight: 'auto' }}
                onClick={() => {
                  const row = empSyncRow;
                  setEmpSyncPin(null);
                  void openEditUser(row);
                }}
              >
                <Pencil size={13} /> 编辑本地档案
              </button>

              {empSyncRow.workStatus === 'resigned' && connectedDeviceIds.length > 0 && (
                <button
                  type="button"
                  className="btn btn-sm btn-danger"
                  disabled={busy}
                  onClick={() => {
                    const pin = empSyncRow.pin;
                    setEmpSyncPin(null);
                    openMultiDeleteModal({ pins: [pin] });
                  }}
                >
                  <Trash2 size={13} /> 选择考勤机移除…
                </button>
              )}

              {empSyncRow.inMaster && empSyncRow.workStatus !== 'resigned' && connectedDeviceIds.length > 0 && (
                <button
                  type="button"
                  className="btn btn-sm btn-primary"
                  disabled={busy}
                  onClick={() => {
                    const pin = empSyncRow.pin;
                    setEmpSyncPin(null);
                    openMultiPushModal({ pins: [pin] });
                  }}
                >
                  <ArrowUpFromLine size={13} /> 选择考勤机上传…
                </button>
              )}

              <button type="button" className="btn btn-sm" onClick={() => setEmpSyncPin(null)}>
                关闭
              </button>
            </>
          )
        }
      >
        {empSyncRow && (
          <>
            <div className="overview-stats-strip">
              <div className="overview-stat-card">
                <span className="overview-stat-label">本地主库状态</span>
                <span className="overview-stat-value" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  {empSyncRow.inMaster ? (
                    empSyncRow.workStatus === 'resigned' ? (
                      <span className="badge badge-danger">已离职</span>
                    ) : (
                      <span className="badge badge-ok">在职 · 已建档</span>
                    )
                  ) : (
                    <span className="badge badge-warn">仅在考勤机 · 未入主库</span>
                  )}
                </span>
              </div>
              <div className="overview-stat-card">
                <span className="overview-stat-label">本地生物资产</span>
                <span className="overview-stat-value">
                  {empSyncRow.hasLocalFace ? '有人脸' : '无人脸'} · 指纹 {empSyncRow.masterFpCount || 0} 枚
                </span>
              </div>
              <div className="overview-stat-card">
                <span className="overview-stat-label">部门 / 权限</span>
                <span className="overview-stat-value">
                  {empSyncRow.department || '未分部门'} · {privilegeLabel(empSyncRow.privilege)}
                </span>
              </div>
            </div>

            <div className="table-card" style={{ maxHeight: 360 }}>
              <div tabIndex={0} role="region" aria-label="员工各考勤机注册情况" className="table-wrapper">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>考勤机</th>
                      <th>机上现状</th>
                      <th style={{ width: 155 }}>状态</th>
                      <th className="cell-nowrap" style={{ width: 145, textAlign: 'right' }}>单机操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {enabledDevices.map((d) => {
                      const cell = empSyncRow.devices?.[d.id];
                      const st = cell?.syncStatus || 'no_snapshot';
                      const conn = !!statuses[d.id]?.connected;
                      return (
                        <tr key={d.id}>
                          <td className="cell-nowrap">
                            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                              <span className={`dev-dot ${conn ? 'connected' : ''}`} />
                              <div>
                                <div style={{ fontWeight: 600 }}>{shortDeviceName(d.name)}</div>
                                <div className="mono" style={{ fontSize: 11, color: 'var(--text-muted)' }}>
                                  {d.ip} · {conn ? '已连接' : '未连接'}
                                </div>
                              </div>
                            </div>
                          </td>
                          <td>
                            {cell?.present ? (
                              <div style={{ fontSize: 12 }}>
                                <div style={{ fontWeight: 550 }}>{cell.name || '（无姓名）'}</div>
                                <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>
                                  {cell.hasFace ? '有人脸' : '无人脸'} · 指纹 {cell.fpCount || 0} 枚
                                </div>
                              </div>
                            ) : (
                              <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>
                                {st === 'no_snapshot' ? '未扫描' : '未在此机注册'}
                              </span>
                            )}
                          </td>
                          <td>
                            {st === 'synced' && <span className="badge badge-ok">✓ 已注册 · 一致</span>}
                            {st === 'not_assigned' && <span className="badge badge-neutral">未在此机</span>}
                            {st === 'no_snapshot' && <span className="badge badge-neutral">未扫描</span>}
                            {st === 'resigned_present' && <span className="badge badge-danger">✕ 离职残留</span>}
                            {(st === 'need_push' || st === 'need_pull' || st === 'both') && (
                              <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                                <span className={`badge ${st === 'need_push' ? 'badge-info' : 'badge-warn'}`} style={{ width: 'fit-content' }}>
                                  {st === 'need_push' ? '↑ 资料待更新' : st === 'need_pull' ? '↓ 待拉取' : '⇅ 双向有更新'}
                                </span>
                                <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
                                  {(cell?.diffDetails || []).join('、')}
                                </span>
                              </div>
                            )}
                          </td>
                          <td className="cell-nowrap" style={{ textAlign: 'right' }}>
                            {empSyncRow.inMaster && empSyncRow.workStatus !== 'resigned' && (
                              <button
                                type="button"
                                className="btn btn-sm btn-ghost"
                                style={{ color: 'var(--accent)' }}
                                disabled={busy || !conn}
                                title={conn ? `上传到 ${shortDeviceName(d.name)}` : '该设备未连接'}
                                onClick={() => {
                                  const pin = empSyncRow.pin;
                                  setEmpSyncPin(null);
                                  openMultiPushModal({ pins: [pin], preselectDeviceIds: [d.id] });
                                }}
                              >
                                <ArrowUpFromLine size={13} />
                              </button>
                            )}
                            {cell?.present && (
                              <button
                                type="button"
                                className="btn btn-sm btn-ghost"
                                style={{ color: 'var(--warn)' }}
                                disabled={busy || !conn}
                                title={conn ? `从 ${shortDeviceName(d.name)} 拉取到本地主库` : '该设备未连接'}
                                onClick={() => {
                                  const pin = empSyncRow.pin;
                                  setEmpSyncPin(null);
                                  openMultiPullModal({ pins: [pin], preselectDeviceIds: [d.id] });
                                }}
                              >
                                <ArrowDownToLine size={13} />
                              </button>
                            )}
                            {cell?.present && (
                              <button
                                type="button"
                                className="btn btn-sm btn-ghost"
                                style={{ color: 'var(--danger)' }}
                                disabled={busy || !conn}
                                title={conn ? `从 ${shortDeviceName(d.name)} 移除` : '该设备未连接'}
                                onClick={() => {
                                  const pin = empSyncRow.pin;
                                  setEmpSyncPin(null);
                                  openMultiDeleteModal({ pins: [pin], preselectDeviceIds: [d.id] });
                                }}
                              >
                                <Trash2 size={13} />
                              </button>
                            )}
                            <button
                              type="button"
                              className="btn btn-sm btn-ghost"
                              title={`查看 ${shortDeviceName(d.name)} 单机名单`}
                              onClick={() => {
                                setEmpSyncPin(null);
                                switchPerspective('device', d.id);
                              }}
                            >
                              <ArrowRight size={13} />
                            </button>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          </>
        )}
      </ModalDialog>

      {/* ==================== Local-First Edit / New User Modal ==================== */}
      <ModalDialog
        open={!!editState}
        onClose={() => setEditState(null)}
        title={
          editState?.isNew
            ? '新增员工（保存到本地主库）'
            : `编辑本地主库档案 · 工号 ${editState?.pin || ''}`
        }
        width={620}
        footer={
          <>
            {editState && !editState.isNew && editState.inMaster && (
              <button
                type="button"
                className="btn btn-ghost"
                style={{ marginRight: 'auto', color: 'var(--danger)' }}
                disabled={busy}
                onClick={() => setDeleteMasterPin(editState.pin)}
              >
                <Trash2 size={13} /> 从本地主库移除
              </button>
            )}
            <button type="button" className="btn" onClick={() => setEditState(null)}>
              取消
            </button>
            {connectedDeviceIds.length > 0 && (
              <button
                type="button"
                className="btn"
                disabled={busy}
                onClick={() => saveEditToLocal(true)}
                title="先保存到本地主库，再手动选择目标考勤机上传"
              >
                <ArrowUpFromLine size={13} /> 保存并上传到考勤机…
              </button>
            )}
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy}
              onClick={() => saveEditToLocal(false)}
            >
              仅保存到本地主库
            </button>
          </>
        }
      >
        {editState && (
          <>
            <div style={{ fontSize: 12, color: 'var(--text-secondary)', background: 'var(--bg-subtle)', padding: '8px 10px', borderRadius: 8 }}>
              {editState.inMaster || editState.isNew
                ? '所有修改优先写入本地人员主库（fsync + `.bak` 滚动备份），不会自动改动任何考勤机；如需上传请点击「保存并上传到考勤机…」。'
                : '该工号目前仅在考勤机上、尚未入本地主库。直接保存将建档基础资料；如需同时保留机上指纹和人脸，建议先从该机拉取到本地。'}
            </div>

            <div className="form-grid">
              <div className="field">
                <label>工号 (PIN)</label>
                <input
                  className="input mono"
                  value={editState.pin}
                  disabled={!editState.isNew}
                  onChange={(e) => setEditState({ ...editState, pin: e.target.value })}
                  placeholder="例如：1008"
                />
              </div>
              <div className="field">
                <label>姓名</label>
                <input
                  className="input"
                  value={editState.name}
                  onChange={(e) => setEditState({ ...editState, name: e.target.value })}
                  placeholder="支持中文"
                />
              </div>
              <div className="field">
                <label>部门</label>
                <input
                  className="input"
                  list="master-dept-list"
                  value={editState.department}
                  onChange={(e) => setEditState({ ...editState, department: e.target.value })}
                  placeholder="例如：教研部 / 行政部"
                />
                <datalist id="master-dept-list">
                  {(matrix.departments || []).map((d) => (
                    <option key={d} value={d} />
                  ))}
                </datalist>
              </div>
              <div className="field">
                <label>在职状态</label>
                <select
                  className="select"
                  value={editState.workStatus}
                  onChange={(e) => setEditState({ ...editState, workStatus: e.target.value })}
                >
                  <option value="active">在职</option>
                  <option value="resigned">离职（考勤机存在时标红待清理）</option>
                </select>
              </div>
              <div className="field">
                <label>设备权限</label>
                <select
                  className="select"
                  value={editState.privilege}
                  onChange={(e) => setEditState({ ...editState, privilege: Number(e.target.value) })}
                >
                  <option value={0}>普通员工 (0)</option>
                  <option value={2}>登记员 (2)</option>
                  <option value={14}>超级管理员 (14)</option>
                </select>
              </div>
              <div className="field">
                <label>账号启用</label>
                <label className="check-item" style={{ height: 31 }}>
                  <input
                    type="checkbox"
                    checked={editState.enabled}
                    onChange={(e) => setEditState({ ...editState, enabled: e.target.checked })}
                  />
                  允许在考勤机打卡验证
                </label>
              </div>
              <div className="field">
                <label>卡号</label>
                <input
                  className="input mono"
                  value={editState.card}
                  onChange={(e) => setEditState({ ...editState, card: e.target.value })}
                  placeholder="留空不修改原卡号"
                />
                {!editState.isNew && (
                  <label className="check-item" style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>
                    <input
                      type="checkbox"
                      checked={editState.preserveCardIfEmpty}
                      onChange={(e) => setEditState({ ...editState, preserveCardIfEmpty: e.target.checked })}
                    />
                    留空时保留主库原卡号
                  </label>
                )}
              </div>
              <div className="field">
                <label>考勤机密码{editState.masterPasswordSet ? '（主库已设置）' : ''}</label>
                <input
                  type="password"
                  className="input mono"
                  value={editState.password}
                  onChange={(e) => setEditState({ ...editState, password: e.target.value })}
                  placeholder="留空不修改原密码"
                />
                {!editState.isNew && (
                  <label className="check-item" style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>
                    <input
                      type="checkbox"
                      checked={editState.preservePwdIfEmpty}
                      onChange={(e) => setEditState({ ...editState, preservePwdIfEmpty: e.target.checked })}
                    />
                    留空时保留主库原密码
                  </label>
                )}
              </div>
              <div className="field full">
                <label>备注</label>
                <input
                  className="input"
                  value={editState.note}
                  onChange={(e) => setEditState({ ...editState, note: e.target.value })}
                  placeholder="可选备注信息"
                />
              </div>
            </div>

            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 14,
                padding: 10,
                borderRadius: 10,
                background: 'var(--bg-subtle)',
                border: '1px solid var(--border)',
              }}
            >
              <div
                style={{
                  width: 68,
                  height: 68,
                  borderRadius: 8,
                  overflow: 'hidden',
                  background: 'var(--bg-card)',
                  border: '1px solid var(--border)',
                  display: 'grid',
                  placeItems: 'center',
                  flexShrink: 0,
                }}
              >
                {faceLoading ? (
                  <RefreshCw size={16} className="spin" />
                ) : editState.faceDataUrl ? (
                  <img src={editState.faceDataUrl} alt="人脸预览" style={{ width: '100%', height: '100%', objectFit: 'cover' }} />
                ) : (
                  <Camera size={20} style={{ color: 'var(--text-muted)' }} />
                )}
              </div>
              <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 5 }}>
                <div style={{ fontSize: 12.5, fontWeight: 600 }}>本地主库生物特征资产</div>
                <div style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>
                  人脸：{editState.faceSourceInfo || '尚未保存人脸照'} · 指纹：主库已保存 <strong>{editState.masterFpCount}</strong> 枚模板
                </div>
                <div>
                  <button type="button" className="btn btn-sm" onClick={handlePickFaceForEdit}>
                    <Upload size={12} /> 选择/更换本地人脸照
                  </button>
                </div>
              </div>
            </div>

            {!editState.isNew && editState.bioClearDeviceId && statuses[editState.bioClearDeviceId]?.connected && (() => {
              const targetDev = enabledDevices.find((d) => d.id === editState.bioClearDeviceId);
              if (!targetDev) return null;
              return (
                <div style={{ padding: '9px 11px', borderRadius: 9, border: '1px solid var(--border)', display: 'flex', flexDirection: 'column', gap: 6 }}>
                  <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--text-secondary)' }}>
                    清空 {shortDeviceName(targetDev.name)} 上的单项生物特征（不删账号、不改本地主库）
                  </div>
                  <div className="toolbar-row" style={{ gap: 6 }}>
                    <button
                      type="button"
                      className="btn btn-sm"
                      disabled={busy}
                      onClick={() => setClearBioConfirm({ pin: editState.pin, target: 'fingerprints', label: '指纹模板', device: targetDev })}
                    >
                      <Fingerprint size={12} /> 清空本机指纹
                    </button>
                    <button
                      type="button"
                      className="btn btn-sm"
                      disabled={busy}
                      onClick={() => setClearBioConfirm({ pin: editState.pin, target: 'face', label: '人脸数据', device: targetDev })}
                    >
                      <Camera size={12} /> 清空本机人脸
                    </button>
                    <button
                      type="button"
                      className="btn btn-sm"
                      disabled={busy}
                      onClick={() => setClearBioConfirm({ pin: editState.pin, target: 'password', label: '验证密码', device: targetDev })}
                    >
                      <KeyRound size={12} /> 清空本机密码
                    </button>
                  </div>
                </div>
              );
            })()}
          </>
        )}
      </ModalDialog>

      {/* ==================== Multi-Device Push Modal (Manual Device Selection + Select All) ==================== */}
      <ModalDialog
        open={!!pushModal}
        onClose={() => setPushModal(null)}
        title={pushModal ? `本地主库上传到考勤机（${pushCountPreview} 人）` : ''}
        width={560}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setPushModal(null)}>关闭</button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || !pushModal || pushModal.targetDeviceIds.length === 0}
              onClick={handleConfirmMultiPush}
            >
              <ArrowUpFromLine size={14} /> 确认上传到 {pushModal?.targetDeviceIds.length || 0} 台设备
            </button>
          </>
        }
      >
        {pushModal && (
          <>
            {pushModal.allowScopeToggle ? (
              <div className="field">
                <label>上传人员范围</label>
                <div className="check-group">
                  <label className="check-item">
                    <input
                      type="radio"
                      checked={pushModal.pushScope === 'pending'}
                      onChange={() => setPushModal({ ...pushModal, pushScope: 'pending' })}
                    />
                    仅资料待更新的员工（{pushModal.pendingPins.length} 人）
                  </label>
                  <label className="check-item">
                    <input
                      type="radio"
                      checked={pushModal.pushScope === 'all_active'}
                      onChange={() => setPushModal({ ...pushModal, pushScope: 'all_active' })}
                    />
                    全部本地在职员工（{pushModal.allActivePins.length} 人）
                  </label>
                </div>
              </div>
            ) : (
              <div style={{ fontSize: 12.5, color: 'var(--text-secondary)' }}>
                本次上传 <strong>{pushModal.pins.length}</strong> 名本地主库员工（工号：
                <span className="mono">{pushModal.pins.slice(0, 8).join(', ')}{pushModal.pins.length > 8 ? '…' : ''}</span>
                ），写入前自动备份并记入审计日志。
              </div>
            )}

            <div className="field">
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                <label>手动选择目标考勤机（已选 {pushModal.targetDeviceIds.length} / {connectedDeviceIds.length} 台已连接）</label>
                {connectedDeviceIds.length > 0 && (
                  <div style={{ display: 'inline-flex', gap: 4 }}>
                    <button
                      type="button"
                      className="btn btn-sm"
                      style={{ height: 22, padding: '0 8px', fontSize: 11.5 }}
                      disabled={pushModal.targetDeviceIds.length === connectedDeviceIds.length}
                      onClick={() => setPushModal({ ...pushModal, targetDeviceIds: [...connectedDeviceIds] })}
                    >
                      全选
                    </button>
                    {pushModal.targetDeviceIds.length > 0 && (
                      <button
                        type="button"
                        className="btn btn-sm btn-ghost"
                        style={{ height: 22, padding: '0 8px', fontSize: 11.5 }}
                        onClick={() => setPushModal({ ...pushModal, targetDeviceIds: [] })}
                      >
                        清空
                      </button>
                    )}
                  </div>
                )}
              </div>
              <div className="modal-device-list">
                {enabledDevices.map((d) => {
                  const conn = !!statuses[d.id]?.connected;
                  const checked = pushModal.targetDeviceIds.includes(d.id);
                  const st = perDeviceStats[d.id];
                  return (
                    <label key={d.id} className={`modal-device-row ${!conn ? 'disabled' : ''}`}>
                      <input
                        type="checkbox"
                        disabled={!conn}
                        checked={checked}
                        onChange={() =>
                          setPushModal({
                            ...pushModal,
                            targetDeviceIds: checked
                              ? pushModal.targetDeviceIds.filter((id) => id !== d.id)
                              : [...pushModal.targetDeviceIds, d.id],
                          })
                        }
                      />
                      <span className={`dev-dot ${conn ? 'connected' : ''}`} />
                      <span style={{ fontWeight: 550 }}>{shortDeviceName(d.name)}</span>
                      <span className="mono" style={{ fontSize: 11, color: 'var(--text-muted)' }}>{d.ip}</span>
                      <span style={{ marginLeft: 'auto' }}>
                        {!conn ? (
                          <span className="badge badge-neutral">未连接</span>
                        ) : st?.hasSnapshot ? (
                          <span className="badge badge-ok">已连接 · 机上 {st.present} 人</span>
                        ) : (
                          <span className="badge badge-ok">已连接</span>
                        )}
                      </span>
                    </label>
                  );
                })}
              </div>
            </div>

            <div className="field">
              <label>上传内容</label>
              <div className="check-group">
                <label className="check-item">
                  <input type="checkbox" checked={pushModal.syncInfo} onChange={(e) => setPushModal({ ...pushModal, syncInfo: e.target.checked })} />
                  基础资料（姓名/权限/启用/卡号）
                </label>
                <label className="check-item">
                  <input type="checkbox" checked={pushModal.syncFP} onChange={(e) => setPushModal({ ...pushModal, syncFP: e.target.checked })} />
                  主库指纹模板
                </label>
                <label className="check-item">
                  <input type="checkbox" checked={pushModal.syncFace} onChange={(e) => setPushModal({ ...pushModal, syncFace: e.target.checked })} />
                  主库人脸照
                </label>
              </div>
            </div>

            <div className="check-group" style={{ fontSize: 12, color: 'var(--text-muted)' }}>
              <label className="check-item">
                <input type="checkbox" checked={pushModal.preserveCard} onChange={(e) => setPushModal({ ...pushModal, preserveCard: e.target.checked })} />
                主库卡号为空时保留考勤机原卡号
              </label>
              <label className="check-item">
                <input type="checkbox" checked={pushModal.preservePwd} onChange={(e) => setPushModal({ ...pushModal, preservePwd: e.target.checked })} />
                主库密码为空时保留考勤机原密码
              </label>
            </div>

            {pushModal.result && (
              <div style={{ padding: 10, borderRadius: 8, background: 'var(--bg-subtle)', fontSize: 12, display: 'flex', flexDirection: 'column', gap: 4 }}>
                {(pushModal.result.results || []).map((r) => (
                  <div key={r.deviceId}>
                    <strong>{r.deviceName}</strong>：
                    {r.success ? (
                      <span style={{ color: 'var(--success)' }}>上传成功（{(r.items || []).length} 人）</span>
                    ) : (
                      <span style={{ color: 'var(--danger)' }}>部分/全部失败：{r.error || '请查看操作记录'}</span>
                    )}
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </ModalDialog>

      {/* ==================== Multi-Device Pull Modal (Manual Device Selection + Select All) ==================== */}
      <ModalDialog
        open={!!pullModal}
        onClose={() => setPullModal(null)}
        title="从考勤机拉取人员到本地主库"
        width={560}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setPullModal(null)}>关闭</button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || !pullModal || pullModal.sourceDeviceIds.length === 0}
              onClick={handleConfirmMultiPull}
            >
              <ArrowDownToLine size={14} /> 从 {pullModal?.sourceDeviceIds.length || 0} 台设备拉取到本地
            </button>
          </>
        }
      >
        {pullModal && (
          <>
            <div style={{ fontSize: 12.5, color: 'var(--text-secondary)', background: 'var(--success-soft)', padding: '8px 10px', borderRadius: 8 }}>
              <strong>只读操作</strong>：不会修改任何考勤机数据；拉取前自动备份当前本地主库，按工号合并到本地人员库。
            </div>

            <div className="field">
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                <label>手动选择来源考勤机（已选 {pullModal.sourceDeviceIds.length} / {connectedDeviceIds.length} 台已连接）</label>
                {connectedDeviceIds.length > 0 && (
                  <div style={{ display: 'inline-flex', gap: 4 }}>
                    <button
                      type="button"
                      className="btn btn-sm"
                      style={{ height: 22, padding: '0 8px', fontSize: 11.5 }}
                      disabled={pullModal.sourceDeviceIds.length === connectedDeviceIds.length}
                      onClick={() => setPullModal({ ...pullModal, sourceDeviceIds: [...connectedDeviceIds] })}
                    >
                      全选
                    </button>
                    {pullModal.sourceDeviceIds.length > 0 && (
                      <button
                        type="button"
                        className="btn btn-sm btn-ghost"
                        style={{ height: 22, padding: '0 8px', fontSize: 11.5 }}
                        onClick={() => setPullModal({ ...pullModal, sourceDeviceIds: [] })}
                      >
                        清空
                      </button>
                    )}
                  </div>
                )}
              </div>
              <div className="modal-device-list">
                {enabledDevices.map((d) => {
                  const conn = !!statuses[d.id]?.connected;
                  const checked = pullModal.sourceDeviceIds.includes(d.id);
                  const st = perDeviceStats[d.id];
                  return (
                    <label key={d.id} className={`modal-device-row ${!conn ? 'disabled' : ''}`}>
                      <input
                        type="checkbox"
                        disabled={!conn}
                        checked={checked}
                        onChange={() =>
                          setPullModal({
                            ...pullModal,
                            sourceDeviceIds: checked
                              ? pullModal.sourceDeviceIds.filter((id) => id !== d.id)
                              : [...pullModal.sourceDeviceIds, d.id],
                          })
                        }
                      />
                      <span className={`dev-dot ${conn ? 'connected' : ''}`} />
                      <span style={{ fontWeight: 550 }}>{shortDeviceName(d.name)}</span>
                      <span className="mono" style={{ fontSize: 11, color: 'var(--text-muted)' }}>{d.ip}</span>
                      <span style={{ marginLeft: 'auto' }}>
                        {!conn ? (
                          <span className="badge badge-neutral">未连接</span>
                        ) : st?.pull ? (
                          <span className="badge badge-warn">{st.pull} 人待拉取</span>
                        ) : st?.hasSnapshot ? (
                          <span className="badge badge-ok">已连接 · 机上 {st.present} 人</span>
                        ) : (
                          <span className="badge badge-ok">已连接</span>
                        )}
                      </span>
                    </label>
                  );
                })}
              </div>
            </div>

            <div className="field">
              <label>拉取范围</label>
              <div className="check-group">
                {pullModal.pins.length > 0 && (
                  <label className="check-item">
                    <input type="radio" checked={!pullModal.pullAll} onChange={() => setPullModal({ ...pullModal, pullAll: false })} />
                    仅拉取已勾选人员（{pullModal.pins.length} 人）
                  </label>
                )}
                <label className="check-item">
                  <input type="radio" checked={pullModal.pullAll} onChange={() => setPullModal({ ...pullModal, pullAll: true })} />
                  拉取所选考勤机上的全部员工
                </label>
              </div>
            </div>

            <div className="field">
              <label>拉取内容</label>
              <div className="check-group">
                <label className="check-item">
                  <input type="checkbox" checked={pullModal.includeInfo} onChange={(e) => setPullModal({ ...pullModal, includeInfo: e.target.checked })} />
                  基础资料
                </label>
                <label className="check-item">
                  <input type="checkbox" checked={pullModal.includeFP} onChange={(e) => setPullModal({ ...pullModal, includeFP: e.target.checked })} />
                  指纹模板 (按手指号合并)
                </label>
                <label className="check-item">
                  <input type="checkbox" checked={pullModal.includeFace} onChange={(e) => setPullModal({ ...pullModal, includeFace: e.target.checked })} />
                  人脸注册照
                </label>
              </div>
            </div>

            <label className="check-item" style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
              <input type="checkbox" checked={pullModal.overwriteInfo} onChange={(e) => setPullModal({ ...pullModal, overwriteInfo: e.target.checked })} />
              覆盖本地已有的姓名 / 权限 / 人脸（默认关闭：仅新增与补缺）
            </label>

            {pullModal.result && (
              <div style={{ padding: 10, borderRadius: 8, background: 'var(--bg-subtle)', fontSize: 12 }}>
                <strong>拉取完成</strong>：处理 {pullModal.result.totalProcessed} 人（新增 {pullModal.result.created} · 更新 {pullModal.result.updated} · 新指纹 {pullModal.result.fpsAbsorbed} 枚 · 人脸 {pullModal.result.facesAbsorbed} 张）
              </div>
            )}
          </>
        )}
      </ModalDialog>

      {/* ==================== Multi-Device Remove Confirm Modal ==================== */}
      <ModalDialog
        open={!!deleteDevModal}
        onClose={() => setDeleteDevModal(null)}
        title={deleteDevModal ? `从考勤机移除人员（${deleteDevModal.pins.length} 人）` : ''}
        width={520}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setDeleteDevModal(null)}>取消</button>
            <button
              type="button"
              className="btn btn-danger"
              disabled={busy || !deleteDevModal || deleteDevModal.targetDeviceIds.length === 0}
              onClick={handleConfirmMultiDelete}
            >
              确认从 {deleteDevModal?.targetDeviceIds.length || 0} 台设备移除
            </button>
          </>
        }
      >
        {deleteDevModal && (
          <>
            <div style={{ display: 'flex', gap: 10, alignItems: 'flex-start' }}>
              <AlertTriangle size={20} style={{ color: 'var(--danger)', flexShrink: 0, marginTop: 2 }} />
              <div style={{ fontSize: 12.5 }}>
                即将从所选考勤机移除 <strong>{deleteDevModal.pins.length}</strong> 名员工（工号：
                <span className="mono">{deleteDevModal.pins.slice(0, 8).join(', ')}{deleteDevModal.pins.length > 8 ? '…' : ''}</span>
                ）。操作前自动备份快照，且<strong>不会删除本地主库与历史考勤</strong>。
              </div>
            </div>

            <div className="field">
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                <label>手动选择要清理的考勤机（已选 {deleteDevModal.targetDeviceIds.length} / {connectedDeviceIds.length} 台已连接）</label>
                {connectedDeviceIds.length > 0 && (
                  <div style={{ display: 'inline-flex', gap: 4 }}>
                    <button
                      type="button"
                      className="btn btn-sm"
                      style={{ height: 22, padding: '0 8px', fontSize: 11.5 }}
                      disabled={deleteDevModal.targetDeviceIds.length === connectedDeviceIds.length}
                      onClick={() => setDeleteDevModal({ ...deleteDevModal, targetDeviceIds: [...connectedDeviceIds] })}
                    >
                      全选
                    </button>
                    {deleteDevModal.targetDeviceIds.length > 0 && (
                      <button
                        type="button"
                        className="btn btn-sm btn-ghost"
                        style={{ height: 22, padding: '0 8px', fontSize: 11.5 }}
                        onClick={() => setDeleteDevModal({ ...deleteDevModal, targetDeviceIds: [] })}
                      >
                        清空
                      </button>
                    )}
                  </div>
                )}
              </div>
              <div className="modal-device-list">
                {enabledDevices.map((d) => {
                  const conn = !!statuses[d.id]?.connected;
                  const checked = deleteDevModal.targetDeviceIds.includes(d.id);
                  const pinSet = new Set(deleteDevModal.pins);
                  const presentCnt = (matrix.rows || []).filter((r) => pinSet.has(r.pin) && r.devices?.[d.id]?.present).length;
                  return (
                    <label key={d.id} className={`modal-device-row ${!conn ? 'disabled' : ''}`}>
                      <input
                        type="checkbox"
                        disabled={!conn}
                        checked={checked}
                        onChange={() =>
                          setDeleteDevModal({
                            ...deleteDevModal,
                            targetDeviceIds: checked
                              ? deleteDevModal.targetDeviceIds.filter((id) => id !== d.id)
                              : [...deleteDevModal.targetDeviceIds, d.id],
                          })
                        }
                      />
                      <span className={`dev-dot ${conn ? 'connected' : ''}`} />
                      <span style={{ fontWeight: 550 }}>{shortDeviceName(d.name)}</span>
                      <span className="mono" style={{ fontSize: 11, color: 'var(--text-muted)' }}>{d.ip}</span>
                      <span style={{ marginLeft: 'auto' }}>
                        {!conn ? (
                          <span className="badge badge-neutral">未连接</span>
                        ) : presentCnt > 0 ? (
                          <span className="badge badge-danger">机上有 {presentCnt} 人</span>
                        ) : (
                          <span className="badge badge-neutral">机上无匹配</span>
                        )}
                      </span>
                    </label>
                  );
                })}
              </div>
            </div>
          </>
        )}
      </ModalDialog>

      {/* ==================== Delete From Local Master Confirm ==================== */}
      <ModalDialog
        open={!!deleteMasterPin}
        onClose={() => setDeleteMasterPin(null)}
        title="确认从本地主库移除"
        width={420}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setDeleteMasterPin(null)}>取消</button>
            <button type="button" className="btn btn-danger" disabled={busy} onClick={handleConfirmDeleteMaster}>
              确认移除
            </button>
          </>
        }
      >
        <div style={{ fontSize: 12.5 }}>
          确定从本地人员主库移除工号 <strong className="mono">{deleteMasterPin}</strong> 吗？
          此操作<strong>不会改动任何考勤机</strong>，移除前自动备份到 `backups/`。
        </div>
      </ModalDialog>

      {/* ==================== Fine-Grained Single-Device Biometric Clear Confirm ==================== */}
      <ModalDialog
        open={!!clearBioConfirm}
        onClose={() => setClearBioConfirm(null)}
        title={`确认清空${clearBioConfirm?.label || ''}`}
        width={440}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setClearBioConfirm(null)}>取消</button>
            <button type="button" className="btn btn-danger" disabled={busy} onClick={handleConfirmClearBio}>
              确认清空
            </button>
          </>
        }
      >
        {clearBioConfirm && (
          <div style={{ fontSize: 12.5 }}>
            即将清空工号 <strong className="mono">{clearBioConfirm.pin}</strong> 在{' '}
            <strong>{clearBioConfirm.device.name}</strong> 上的 <strong>{clearBioConfirm.label}</strong>
            （保留账号与本地主库备份）。
          </div>
        )}
      </ModalDialog>
    </>
  );
}
