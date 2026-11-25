import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  ArrowRight,
  Calendar,
  CheckCircle2,
  ChevronDown,
  Clock,
  Database,
  Download,
  FileArchive,
  FileSpreadsheet,
  FileText,
  Filter,
  Info,
  RefreshCw,
  RotateCcw,
  Search,
  Trash2,
  Upload,
} from 'lucide-react';
import {
  ArchiveOverviewResponse,
  AttendanceRecord,
  DailySummaryRow,
  DeviceConfig,
  DeviceStatus,
  PruneArchivePreview,
  PullLogsResponse,
} from '../../bindings/zk_mac/internal/models';
import * as ZK from '../../bindings/zk_mac/internal/service/zkservice';
import { ModalDialog } from '../components/ModalDialog';

interface LogsViewProps {
  devices: DeviceConfig[];
  statuses: Record<string, DeviceStatus | undefined>;
  logsData: PullLogsResponse;
  busy: boolean;
  onLogsChange: (data: PullLogsResponse) => void;
  onAuditChanged: () => void;
  onGoToDevices: () => void;
  onNotify: (type: 'info' | 'success' | 'error', text: string) => void;
  setBusy: (v: boolean) => void;
}

type DatePreset = 'today' | '7d' | 'month' | 'prevMonth';
type ExportFormat = 'xlsx' | 'abt' | 'csv' | 'summary_csv' | 'txt';

interface PullModalState {
  deviceIds: string[];
  startDate: string;
  endDate: string;
}

function formatDate(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return `${y}-${m}-${day}`;
}

function getPresetRange(preset: DatePreset): [string, string] {
  const now = new Date();
  if (preset === 'today') {
    const s = formatDate(now);
    return [s, s];
  }
  if (preset === '7d') {
    const from = new Date(now);
    from.setDate(now.getDate() - 6);
    return [formatDate(from), formatDate(now)];
  }
  if (preset === 'month') {
    const from = new Date(now.getFullYear(), now.getMonth(), 1);
    return [formatDate(from), formatDate(now)];
  }
  const from = new Date(now.getFullYear(), now.getMonth() - 1, 1);
  const to = new Date(now.getFullYear(), now.getMonth(), 0);
  return [formatDate(from), formatDate(to)];
}

function matchPreset(start: string, end: string): DatePreset | null {
  for (const key of ['today', '7d', 'month', 'prevMonth'] as const) {
    const [s, e] = getPresetRange(key);
    if (start === s && end === e) return key;
  }
  return null;
}

function shortDeviceName(name: string): string {
  return name.replace(/\s*\([^)]*\)\s*$/, '').trim() || name;
}

function getCutoffPresetDate(preset: 'thisMonth' | '1m' | '3m' | '6m'): string {
  const now = new Date();
  if (preset === 'thisMonth') {
    return formatDate(new Date(now.getFullYear(), now.getMonth(), 1));
  }
  const d = new Date(now);
  if (preset === '1m') d.setMonth(d.getMonth() - 1);
  else if (preset === '3m') d.setMonth(d.getMonth() - 3);
  else d.setMonth(d.getMonth() - 6);
  return formatDate(d);
}

export function LogsView({
  devices,
  statuses,
  logsData,
  busy,
  onLogsChange,
  onAuditChanged,
  onGoToDevices,
  onNotify,
  setBusy,
}: LogsViewProps) {
  const exportPopoverId = useId();
  const exportPopoverRef = useRef<HTMLDivElement | null>(null);
  const querySeqRef = useRef(0);
  const skipNextAutoQueryRef = useRef(false);

  const enabledDevices = useMemo(() => devices.filter((d) => d.enabled), [devices]);
  const connectedDeviceIds = useMemo(
    () => enabledDevices.filter((d) => statuses[d.id]?.connected).map((d) => d.id),
    [enabledDevices, statuses]
  );

  // Local archive query criteria (completely decoupled from hardware connection state)
  const [filterDeviceId, setFilterDeviceId] = useState<string>(''); // '' = all devices (incl. imported ABT sensors)
  const [startDate, setStartDate] = useState<string>(() => getPresetRange('month')[0]);
  const [endDate, setEndDate] = useState<string>(() => getPresetRange('month')[1]);
  const [querying, setQuerying] = useState(false);

  const activePreset = useMemo<DatePreset | null>(
    () => matchPreset(startDate, endDate),
    [startDate, endDate]
  );

  // Default view: raw punch stream
  const [viewMode, setViewMode] = useState<'summary' | 'raw'>('raw');
  const [query, setQuery] = useState('');
  const [filterDept, setFilterDept] = useState('');
  const [filterVerify, setFilterVerify] = useState('');
  const [onlyAbnormal, setOnlyAbnormal] = useState(false);

  // Modals: Hardware Pull Confirm, Local Archive Overview, Local Archive Prune, Daily Punch Detail
  const [pullModal, setPullModal] = useState<PullModalState | null>(null);
  const [archiveModalOpen, setArchiveModalOpen] = useState(false);
  const [archiveOverview, setArchiveOverview] = useState<ArchiveOverviewResponse | null>(null);
  const [loadingOverview, setLoadingOverview] = useState(false);
  const [pruneModal, setPruneModal] = useState<{ beforeDate: string; deviceId: string } | null>(null);
  const [prunePreview, setPrunePreview] = useState<PruneArchivePreview | null>(null);
  const [pruning, setPruning] = useState(false);
  const [detailRow, setDetailRow] = useState<DailySummaryRow | null>(null);

  const loadArchiveOverview = useCallback(async () => {
    setLoadingOverview(true);
    try {
      const ov = await ZK.GetAttendanceArchiveOverview();
      setArchiveOverview(ov);
    } catch (err: any) {
      onNotify('error', `读取本地累计库概览失败：${err?.message || err}`);
    } finally {
      setLoadingOverview(false);
    }
  }, [onNotify]);

  const openArchiveOverview = () => {
    setArchiveModalOpen(true);
    void loadArchiveOverview();
  };

  // Live preview of how many local records would be removed before confirming prune
  useEffect(() => {
    if (!pruneModal || pruneModal.beforeDate.length !== 10) {
      setPrunePreview(null);
      return;
    }
    let cancelled = false;
    ZK.PreviewPruneAttendanceArchive({
      beforeDate: pruneModal.beforeDate,
      deviceId: pruneModal.deviceId,
    })
      .then((res) => {
        if (!cancelled) setPrunePreview(res);
      })
      .catch(() => {
        if (!cancelled) setPrunePreview(null);
      });
    return () => {
      cancelled = true;
    };
  }, [pruneModal]);

  const openPruneModal = () => {
    setArchiveModalOpen(false);
    setPruneModal({
      beforeDate: getCutoffPresetDate('thisMonth'),
      deviceId: '',
    });
  };

  const handleConfirmPrune = async () => {
    if (!pruneModal || !prunePreview || prunePreview.matchCount === 0) return;
    setPruning(true);
    try {
      const res = await ZK.PruneAttendanceArchive({
        beforeDate: pruneModal.beforeDate,
        deviceId: pruneModal.deviceId,
      });
      setPruneModal(null);
      onAuditChanged();
      await runLocalArchiveQuery(startDate, endDate, filterDeviceId);
      void loadArchiveOverview();
      onNotify(
        'success',
        `已清理本地 ${res.removed} 条历史考勤（剩余 ${res.remainCount} 条，已自动备份至 backups/，未改动考勤机）`
      );
    } catch (err: any) {
      onNotify('error', `清理本地考勤库失败：${err?.message || err}`);
    } finally {
      setPruning(false);
    }
  };

  const runLocalArchiveQuery = useCallback(
    async (sDate: string, eDate: string, devId: string) => {
      if (!sDate || !eDate || sDate > eDate) return;
      const seq = ++querySeqRef.current;
      const ids = devId ? [devId] : [];
      setQuerying(true);
      try {
        const res = await ZK.QueryArchivedLogs(sDate, eDate, ids);
        if (seq !== querySeqRef.current) return;
        onLogsChange(res);
      } catch (err: any) {
        if (seq !== querySeqRef.current) return;
        onNotify('error', `查询本地累计库失败：${err?.message || err}`);
      } finally {
        if (seq === querySeqRef.current) {
          setQuerying(false);
        }
      }
    },
    [onLogsChange, onNotify]
  );

  // Auto-query local cumulative archive whenever date range or local device filter changes
  useEffect(() => {
    if (skipNextAutoQueryRef.current) {
      skipNextAutoQueryRef.current = false;
      return;
    }
    void runLocalArchiveQuery(startDate, endDate, filterDeviceId);
  }, [startDate, endDate, filterDeviceId, runLocalArchiveQuery]);

  const deptOptions = useMemo(() => {
    const set = new Set<string>();
    for (const r of logsData.records || []) {
      if (r.department) set.add(r.department);
    }
    return Array.from(set).sort();
  }, [logsData.records]);

  const showDeptColumn = deptOptions.length > 0;

  const applyPreset = (preset: DatePreset) => {
    const [s, e] = getPresetRange(preset);
    setStartDate(s);
    setEndDate(e);
  };

  const clearSecondaryFilters = () => {
    setQuery('');
    setFilterDept('');
    setFilterVerify('');
    setOnlyAbnormal(false);
  };

  // ==================== Hardware Pull Modal (Pre-selects Connected Devices!) ====================
  const openPullModal = () => {
    if (connectedDeviceIds.length === 0) {
      onNotify('error', '当前没有已连接的考勤机，请先在「设备总览」中手动连接');
      return;
    }
    setPullModal({
      deviceIds: [...connectedDeviceIds],
      startDate,
      endDate,
    });
  };

  const handleConfirmPull = async () => {
    if (!pullModal) return;
    const targets = pullModal.deviceIds.filter((id) => connectedDeviceIds.includes(id));
    if (targets.length === 0) {
      onNotify('error', '请至少勾选一台已连接的考勤机');
      return;
    }
    if (!pullModal.startDate || !pullModal.endDate || pullModal.startDate > pullModal.endDate) {
      onNotify('error', '拉取开始日期不能晚于结束日期');
      return;
    }

    const req = {
      deviceIds: targets,
      startDate: pullModal.startDate,
      endDate: pullModal.endDate,
    };
    // Close confirmation modal before launching hardware pull so centered ProgressModal displays cleanly
    setPullModal(null);
    setBusy(true);
    try {
      const res = await ZK.PullAttendanceLogs(req);
      onAuditChanged();
      // Re-query current local view so newly pulled records seamlessly merge with all other devices' local archive!
      await runLocalArchiveQuery(startDate, endDate, filterDeviceId);

      const errDevs = (res.summaries || []).filter((s) => s.error);
      if (errDevs.length > 0) {
        onNotify(
          'info',
          `增量拉取完成：拉取 ${res.total} 条，新增入库 ${res.addedNew} 条（${errDevs.length} 台失败：${shortDeviceName(errDevs[0].deviceName)}）`
        );
      } else {
        onNotify(
          'success',
          `已从 ${targets.length} 台考勤机拉取并合并入库：新增 ${res.addedNew} 条，本地累计库共 ${res.archiveTotal} 条`
        );
      }
    } catch (err: any) {
      onNotify('error', `拉取失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const handleImportABT = async () => {
    setBusy(true);
    try {
      const res = await ZK.ImportABTLogsFromDialog();
      if (!res || res.total === 0) return;

      let minDay = '';
      let maxDay = '';
      for (const r of res.records || []) {
        const d = (r.timestamp || '').slice(0, 10);
        if (d.length === 10) {
          if (!minDay || d < minDay) minDay = d;
          if (!maxDay || d > maxDay) maxDay = d;
        }
      }
      querySeqRef.current++;
      if (minDay && maxDay && (minDay !== startDate || maxDay !== endDate)) {
        skipNextAutoQueryRef.current = true;
        setStartDate(minDay);
        setEndDate(maxDay);
      }
      onLogsChange(res);
      onAuditChanged();
      onNotify(
        'success',
        `已导入 ${res.total} 条打卡记录（新增入库 ${res.addedNew} 条，本地累计库共 ${res.archiveTotal} 条）`
      );
    } catch (err: any) {
      onNotify('error', `导入 .ABT 失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  // ==================== Secondary Filtering & WYSIWYG Query Export ====================
  const filteredRaw = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (logsData.records || []).filter((r) => {
      if (filterDept && r.department !== filterDept) return false;
      if (filterVerify && r.verifyLabel !== filterVerify) return false;
      if (q) {
        const hitPin = r.pin.toLowerCase().includes(q);
        const hitName = (r.name || '').toLowerCase().includes(q);
        const hitDept = (r.department || '').toLowerCase().includes(q);
        const hitTime = r.timestamp.toLowerCase().includes(q);
        const hitDev = (r.deviceName || '').toLowerCase().includes(q);
        if (!hitPin && !hitName && !hitDept && !hitTime && !hitDev) return false;
      }
      return true;
    });
  }, [logsData.records, query, filterDept, filterVerify]);

  const filteredSummary = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (logsData.dailySummary || []).filter((row) => {
      if (onlyAbnormal && !row.abnormal) return false;
      if (filterDept && row.department !== filterDept) return false;
      if (q) {
        const hit =
          row.pin.toLowerCase().includes(q) ||
          (row.name || '').toLowerCase().includes(q) ||
          (row.department || '').toLowerCase().includes(q) ||
          row.date.includes(q);
        if (!hit) return false;
      }
      return true;
    });
  }, [logsData.dailySummary, query, filterDept, onlyAbnormal]);

  const abnormalCount = useMemo(
    () => (logsData.dailySummary || []).filter((r) => r.abnormal).length,
    [logsData.dailySummary]
  );

  const queriedRecords = useMemo<AttendanceRecord[]>(() => {
    if (viewMode === 'raw') {
      return filteredRaw;
    }
    const allowedKeys = new Set(filteredSummary.map((row) => `${row.date}|${row.pin}`));
    if (allowedKeys.size === 0) return [];
    return (logsData.records || []).filter((r) =>
      allowedKeys.has(`${(r.timestamp || '').slice(0, 10)}|${r.pin}`)
    );
  }, [viewMode, filteredRaw, filteredSummary, logsData.records]);

  const queriedSummaryCount = useMemo(() => {
    if (viewMode === 'summary') return filteredSummary.length;
    const keys = new Set<string>();
    for (const r of filteredRaw) {
      if (r.timestamp.length >= 10) {
        keys.add(`${r.timestamp.slice(0, 10)}|${r.pin}`);
      }
    }
    return keys.size;
  }, [viewMode, filteredSummary.length, filteredRaw]);

  const hasSecondaryFilter = Boolean(
    query.trim() ||
      filterDept ||
      (viewMode === 'summary' && onlyAbnormal) ||
      (viewMode === 'raw' && filterVerify)
  );
  const isNarrowed = hasSecondaryFilter && queriedRecords.length < (logsData.records || []).length;

  const buildExportMeta = (overrideFullRange = false) => {
    const dateTag =
      startDate === endDate
        ? startDate.replace(/-/g, '')
        : `${startDate.replace(/-/g, '')}_${endDate.replace(/-/g, '')}`;
    const prefixParts = [`考勤查询_${dateTag}`];
    const scopeParts = [`${startDate}~${endDate}`];

    if (filterDeviceId) {
      const dev = enabledDevices.find((d) => d.id === filterDeviceId);
      if (dev) {
        const sName = shortDeviceName(dev.name);
        prefixParts.push(sName);
        scopeParts.push(sName);
      }
    }

    if (!overrideFullRange) {
      if (filterDept) {
        prefixParts.push(filterDept);
        scopeParts.push(`部门:${filterDept}`);
      }
      if (viewMode === 'summary' && onlyAbnormal) {
        prefixParts.push('仅异常');
        scopeParts.push('仅单次/异常');
      }
      if (viewMode === 'raw' && filterVerify) {
        prefixParts.push(filterVerify);
        scopeParts.push(`方式:${filterVerify}`);
      }
      if (query.trim()) {
        prefixParts.push(query.trim());
        scopeParts.push(`搜索:${query.trim()}`);
      }
    }

    return {
      filePrefix: prefixParts.join('_'),
      scopeNote: scopeParts.join(' · '),
    };
  };

  const handleExport = async (
    fmt: ExportFormat,
    options?: {
      records?: AttendanceRecord[];
      dailySummary?: DailySummaryRow[];
      filePrefix?: string;
      scopeNote?: string;
      fullRange?: boolean;
    }
  ) => {
    (exportPopoverRef.current as (HTMLDivElement & { hidePopover?: () => void }) | null)?.hidePopover?.();

    const targetRecords = options?.records ?? (options?.fullRange ? logsData.records || [] : queriedRecords);
    const targetSummary =
      options?.dailySummary ??
      (options?.fullRange ? logsData.dailySummary || [] : viewMode === 'summary' ? filteredSummary : []);

    if (fmt === 'summary_csv' ? targetSummary.length === 0 && targetRecords.length === 0 : targetRecords.length === 0) {
      onNotify('error', '当前查询结果为空，无法导出');
      return;
    }

    const meta = buildExportMeta(Boolean(options?.fullRange));
    try {
      const saved = await ZK.ExportQueriedLogs({
        format: fmt,
        records: targetRecords,
        dailySummary: targetSummary,
        filePrefix: options?.filePrefix || meta.filePrefix,
        scopeNote: options?.scopeNote || meta.scopeNote,
      });
      if (saved) {
        onAuditChanged();
        const label =
          fmt === 'xlsx'
            ? `双 Sheet Excel（每日首末汇总 + ${targetRecords.length} 条流水）`
            : fmt === 'abt'
              ? `ZKTime 兼容 .ABT（${targetRecords.length} 条流水）`
              : fmt === 'summary_csv'
                ? `每日汇总 CSV（${targetSummary.length || queriedSummaryCount} 条汇总）`
                : fmt === 'txt'
                  ? `ZKTime GBK TXT（${targetRecords.length} 条流水）`
                  : `原始流水 CSV（${targetRecords.length} 条流水）`;
        onNotify('success', `已导出查询结果 ${label}：${saved}`);
      }
    } catch (err: any) {
      onNotify('error', `导出失败：${err?.message || err}`);
    }
  };

  const detailRecords = useMemo(() => {
    if (!detailRow) return [];
    return (logsData.records || [])
      .filter((r) => r.pin === detailRow.pin && (r.timestamp || '').slice(0, 10) === detailRow.date)
      .sort((a, b) => a.timestamp.localeCompare(b.timestamp));
  }, [detailRow, logsData.records]);

  const invalidDateRange = Boolean(startDate && endDate && startDate > endDate);
  const pullModalPreset = pullModal ? matchPreset(pullModal.startDate, pullModal.endDate) : null;
  const pullModalInvalidDate = Boolean(
    pullModal && pullModal.startDate && pullModal.endDate && pullModal.startDate > pullModal.endDate
  );

  return (
    <>
      <div className="card logs-control-card">
        {/* Row 1: 数据采集入库（左） + 查询结果导出（右），彻底与本地筛选解耦 */}
        <div className="logs-control-row">
          <div className="toolbar-row" style={{ gap: 6 }}>
            {connectedDeviceIds.length === 0 ? (
              <button
                type="button"
                className="btn btn-sm"
                onClick={onGoToDevices}
                title="拉取最新打卡前需先在「设备总览」中手动连接考勤机；浏览与导出本地历史无需连接"
              >
                去设备总览连接 <ArrowRight size={13} />
              </button>
            ) : (
              <button
                type="button"
                className="btn btn-sm btn-primary"
                disabled={busy}
                onClick={openPullModal}
                title="弹窗手动选择要拉取的考勤机与日期范围，并自动去重合并到本地累计库"
              >
                <RefreshCw size={13} className={busy ? 'spin' : ''} />
                拉取考勤 ({connectedDeviceIds.length} 台在线)
              </button>
            )}

            <button
              type="button"
              className="btn btn-sm"
              disabled={busy}
              title="导入历史或其它校区的 ZKTime .ABT / .TXT 考勤文件并去重合并到本地库"
              onClick={handleImportABT}
            >
              <Upload size={13} /> 导入 .ABT
            </button>

            <button
              type="button"
              className="archive-pill-btn"
              onClick={openArchiveOverview}
              title="点击查看各考勤机在本地累计库中的条数与日期覆盖范围"
            >
              <Database size={12} />
              <span>本地累计库：{logsData.archiveTotal || 0} 条</span>
              <Info size={11} />
            </button>
          </div>

          <div className="toolbar-row" style={{ gap: 6, flexShrink: 0 }}>
            {isNarrowed && (
              <span
                className="badge badge-info"
                title="导出按钮将精确导出当前搜索/筛选后的查询结果"
              >
                <Filter size={10} /> 已筛选
              </span>
            )}

            <button
              type="button"
              className="btn btn-sm"
              disabled={queriedRecords.length === 0}
              title={`导出当前查询结果到双 Sheet Excel（${queriedSummaryCount} 条日汇总 + ${queriedRecords.length} 条流水）`}
              onClick={() => handleExport('xlsx')}
            >
              <FileSpreadsheet size={13} />
              导出 Excel
              {queriedRecords.length > 0 && (
                <span className="export-count-pill">{queriedRecords.length}</span>
              )}
            </button>

            <button
              type="button"
              className="btn btn-sm"
              disabled={queriedRecords.length === 0}
              onClick={() => handleExport('abt')}
              title={`导出当前查询的 ${queriedRecords.length} 条流水为原版 ZKTime 5.0 兼容 .ABT 格式`}
            >
              <FileArchive size={13} /> 导出 .ABT
            </button>

            <button
              type="button"
              className="btn btn-sm"
              disabled={queriedRecords.length === 0 && (logsData.records || []).length === 0}
              {...({ popoverTarget: exportPopoverId } as Record<string, string>)}
            >
              更多导出 <ChevronDown size={13} />
            </button>

            <div
              id={exportPopoverId}
              ref={exportPopoverRef}
              {...({ popover: 'auto' } as Record<string, string>)}
              className="export-popover"
            >
              <button
                type="button"
                className="export-popover-item"
                disabled={queriedRecords.length === 0}
                onClick={() => handleExport('summary_csv')}
              >
                <FileSpreadsheet size={14} />
                <span>导出每日汇总 CSV</span>
                <span className="export-popover-sub">{queriedSummaryCount} 条</span>
              </button>
              <button
                type="button"
                className="export-popover-item"
                disabled={queriedRecords.length === 0}
                onClick={() => handleExport('csv')}
              >
                <Download size={14} />
                <span>导出原始流水 CSV</span>
                <span className="export-popover-sub">{queriedRecords.length} 条</span>
              </button>
              <button
                type="button"
                className="export-popover-item"
                disabled={queriedRecords.length === 0}
                onClick={() => handleExport('txt')}
                title="GBK 编码 Tab 分隔明文，可直接导入老版 ZKTime 5.0"
              >
                <FileText size={14} />
                <span>导出 ZKTime TXT</span>
                <span className="export-popover-sub">GBK</span>
              </button>

              {isNarrowed && (logsData.records || []).length > 0 && (
                <>
                  <div className="export-popover-divider" />
                  <button
                    type="button"
                    className="export-popover-item"
                    onClick={() => handleExport('xlsx', { fullRange: true })}
                    title="忽略搜索/异常筛选，导出当前日期区间与设备范围的全部记录"
                  >
                    <FileSpreadsheet size={14} />
                    <span>导出本区间全部 Excel</span>
                    <span className="export-popover-sub">{logsData.total} 条</span>
                  </button>
                </>
              )}
            </div>
          </div>
        </div>

        {/* Row 2: 本地查询条件栏（日期 + 快捷区间 + 考勤机筛选，变更即时查本地库，适配 5~20+ 台设备） */}
        <div className="logs-control-row">
          <div className="toolbar-row" style={{ gap: 8 }}>
            <div className="date-range-group">
              <Calendar size={14} />
              <input
                type="date"
                className="mono"
                aria-label="开始日期"
                value={startDate}
                onChange={(e) => setStartDate(e.target.value)}
              />
              <span style={{ fontSize: 12 }}>至</span>
              <input
                type="date"
                className="mono"
                aria-label="结束日期"
                value={endDate}
                onChange={(e) => setEndDate(e.target.value)}
              />
            </div>

            <div className="segmented" role="group" aria-label="快捷日期区间">
              {(
                [
                  ['today', '今天'],
                  ['7d', '近 7 天'],
                  ['month', '本月'],
                  ['prevMonth', '上月'],
                ] as const
              ).map(([key, label]) => (
                <button
                  key={key}
                  type="button"
                  className={activePreset === key ? 'active' : ''}
                  onClick={() => applyPreset(key)}
                >
                  {label}
                </button>
              ))}
            </div>

            <select
              className="select"
              aria-label="按考勤机筛选本地记录"
              value={filterDeviceId}
              onChange={(e) => setFilterDeviceId(e.target.value)}
            >
              <option value="">全部考勤机 ({enabledDevices.length} 台合并)</option>
              {enabledDevices.map((d) => (
                <option key={d.id} value={d.id}>
                  {statuses[d.id]?.connected ? '● ' : '○ '}
                  {shortDeviceName(d.name)}
                </option>
              ))}
            </select>

            {invalidDateRange && (
              <span className="badge badge-danger">开始日期不能晚于结束日期</span>
            )}
            {querying && (
              <span style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>查询中...</span>
            )}
          </div>
        </div>

        {/* Row 3: 紧凑单行状态摘要（各设备当前区间命中数 + 更新时间） */}
        <div className="logs-control-footer">
          <div className="toolbar-row" style={{ gap: 6, minWidth: 0 }}>
            {logsData.summaries && logsData.summaries.length > 0 ? (
              logsData.summaries.map((s) => (
                <button
                  key={s.deviceId}
                  type="button"
                  className={`badge ${s.error ? 'badge-danger' : 'badge-ok'} diff-chip-btn`}
                  title={
                    s.error ||
                    `${s.deviceName} · 点击仅筛选此考勤机（再次点击恢复全部）`
                  }
                  onClick={() =>
                    setFilterDeviceId((cur) => (cur === s.deviceId ? '' : s.deviceId))
                  }
                >
                  {shortDeviceName(s.deviceName)}: {s.error ? `失败 (${s.error})` : `${s.count} 条`}
                </button>
              ))
            ) : (
              <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>
                当前查询区间暂无本地记录，可点击上方「拉取考勤」从已连接设备增量拉取并合并入库
              </span>
            )}
          </div>

          {logsData.pulledAt && (
            <span
              style={{ fontSize: 11.5, color: 'var(--text-muted)', flexShrink: 0 }}
              title={`本地累计库最近更新时间：${logsData.pulledAt}`}
            >
              库更新于 {logsData.pulledAt.slice(5)}
            </span>
          )}
        </div>
      </div>

      {/* 视图切换与二次筛选工具栏 */}
      <div className="toolbar-row">
        <div className="segmented" role="group" aria-label="视图模式">
          <button
            type="button"
            className={viewMode === 'raw' ? 'active' : ''}
            onClick={() => setViewMode('raw')}
          >
            原始打卡流水 ({logsData.total || 0})
          </button>
          <button
            type="button"
            className={viewMode === 'summary' ? 'active' : ''}
            onClick={() => setViewMode('summary')}
          >
            每日首末打卡 ({(logsData.dailySummary || []).length})
          </button>
        </div>

        <div className="search-box">
          <Search size={14} />
          <input
            className="input"
            placeholder="搜索工号 / 姓名 / 日期..."
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>

        {deptOptions.length > 0 && (
          <select
            className="select"
            aria-label="按部门筛选"
            value={filterDept}
            onChange={(e) => setFilterDept(e.target.value)}
          >
            <option value="">全部部门</option>
            {deptOptions.map((d) => (
              <option key={d} value={d}>
                {d}
              </option>
            ))}
          </select>
        )}

        {viewMode === 'summary' ? (
          <label className="check-item">
            <input
              type="checkbox"
              checked={onlyAbnormal}
              onChange={(e) => setOnlyAbnormal(e.target.checked)}
            />
            仅看单次打卡/异常 ({abnormalCount})
          </label>
        ) : (
          <select
            className="select"
            aria-label="按验证方式筛选"
            value={filterVerify}
            onChange={(e) => setFilterVerify(e.target.value)}
          >
            <option value="">全部验证方式</option>
            <option value="人脸">人脸</option>
            <option value="指纹">指纹</option>
            <option value="刷卡">刷卡</option>
            <option value="密码">密码</option>
          </select>
        )}

        {hasSecondaryFilter && (
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            onClick={clearSecondaryFilters}
            title="清空搜索与筛选条件"
          >
            <RotateCcw size={12} /> 重置筛选
          </button>
        )}

        <span style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--text-muted)' }}>
          {viewMode === 'summary' ? (
            <>
              显示 <strong style={{ color: 'var(--text-primary)' }}>{filteredSummary.length}</strong> 条日汇总
              <span style={{ marginLeft: 6 }}>（对应 {queriedRecords.length} 条流水，点击行可看明细）</span>
            </>
          ) : (
            <>
              显示 <strong style={{ color: 'var(--text-primary)' }}>{filteredRaw.length}</strong> / {logsData.total || 0} 条流水
            </>
          )}
        </span>
      </div>

      {/* 数据表格区域 */}
      <div className="table-card">
        {viewMode === 'summary' ? (
          filteredSummary.length === 0 ? (
            <div className="empty-state">
              <Calendar size={30} strokeWidth={1.5} />
              <div>
                {hasSecondaryFilter
                  ? '当前筛选条件下没有匹配的每日汇总记录'
                  : '当前日期区间暂无每日汇总数据，可调整上方日期/考勤机或点击「拉取考勤」'}
              </div>
            </div>
          ) : (
            <div tabIndex={0} role="region" aria-label="每日首末打卡汇总表" className="table-wrapper">
              <table className="data-table">
                <thead>
                  <tr>
                    <th className="cell-nowrap" style={{ width: 106, minWidth: 102 }}>日期</th>
                    <th className="cell-nowrap" style={{ width: 76 }}>工号</th>
                    <th className="cell-nowrap" style={{ width: 96 }}>姓名</th>
                    {showDeptColumn && <th className="cell-nowrap" style={{ width: 100 }}>部门</th>}
                    <th className="cell-nowrap" style={{ width: 92 }}>首次打卡</th>
                    <th className="cell-nowrap" style={{ width: 92 }}>末次打卡</th>
                    <th className="cell-nowrap" style={{ width: 64 }}>次数</th>
                    <th className="cell-nowrap" style={{ width: 92 }}>跨度时长</th>
                    <th className="cell-nowrap" style={{ width: 175 }}>状态提醒</th>
                    <th>当日全部打卡点</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredSummary.slice(0, 2000).map((row) => {
                    const times = row.allTimes || [];
                    const previewTimes = times.slice(0, 3).join(' · ');
                    const extraCount = times.length > 3 ? times.length - 3 : 0;
                    const fullTimesText = times.join(' · ');
                    return (
                      <tr
                        key={`${row.date}-${row.pin}`}
                        className={`row-clickable ${row.abnormal ? 'row-diff' : ''}`}
                        onClick={() => setDetailRow(row)}
                      >
                        <td className="mono cell-nowrap">{row.date}</td>
                        <td className="mono cell-nowrap">{row.pin}</td>
                        <td className="cell-nowrap" style={{ fontWeight: 550 }}>{row.name || '-'}</td>
                        {showDeptColumn && (
                          <td className="cell-nowrap">
                            {row.department || <span style={{ color: 'var(--text-muted)' }}>-</span>}
                          </td>
                        )}
                        <td className="mono cell-nowrap">{row.firstTime}</td>
                        <td className="mono cell-nowrap">
                          {row.lastTime || <span style={{ color: 'var(--danger)' }}>缺失</span>}
                        </td>
                        <td className="mono cell-nowrap">{row.punchCount}</td>
                        <td className="mono cell-nowrap">{row.workDuration}</td>
                        <td className="cell-nowrap">
                          {row.abnormal ? (
                            <span className="badge badge-warn">
                              <AlertTriangle size={11} /> {row.abnormalNote}
                            </span>
                          ) : (
                            <span className="badge badge-ok">
                              <CheckCircle2 size={11} /> 正常
                            </span>
                          )}
                        </td>
                        <td className="cell-nowrap">
                          <button
                            type="button"
                            className="punch-preview-btn"
                            onClick={(e) => {
                              e.stopPropagation();
                              setDetailRow(row);
                            }}
                          >
                            <span className="punch-preview-text" aria-hidden="true">
                              {previewTimes}
                            </span>
                            {extraCount > 0 && (
                              <span className="punch-more-pill" aria-hidden="true">
                                +{extraCount}
                              </span>
                            )}
                            <span className="sr-only">
                              查看 {row.name || row.pin} {row.date} 全部 {times.length} 次打卡：{fullTimesText}
                            </span>
                          </button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
              {filteredSummary.length > 2000 && (
                <div style={{ padding: 10, textAlign: 'center', color: 'var(--text-muted)', fontSize: 12 }}>
                  界面仅预览前 2000 条日汇总（导出 Excel / 每日汇总 CSV 会包含全部 {filteredSummary.length} 条）
                </div>
              )}
            </div>
          )
        ) : filteredRaw.length === 0 ? (
          <div className="empty-state">
            <Calendar size={30} strokeWidth={1.5} />
            <div>
              {hasSecondaryFilter
                ? '当前筛选条件下没有匹配的打卡流水'
                : '当前日期区间暂无考勤记录，可调整上方日期/考勤机或点击「拉取考勤」'}
            </div>
          </div>
        ) : (
          <div tabIndex={0} role="region" aria-label="考勤流水列表" className="table-wrapper">
            <table className="data-table">
              <thead>
                <tr>
                  <th className="cell-nowrap" style={{ width: 82 }}>工号</th>
                  <th className="cell-nowrap" style={{ width: 104 }}>姓名</th>
                  {showDeptColumn && <th className="cell-nowrap" style={{ width: 104 }}>部门</th>}
                  <th className="cell-nowrap" style={{ width: 175 }}>打卡时间</th>
                  <th>设备</th>
                  <th className="cell-nowrap" style={{ width: 96 }}>验证方式</th>
                  <th className="cell-nowrap" style={{ width: 96 }}>状态</th>
                </tr>
              </thead>
              <tbody>
                {filteredRaw.slice(0, 2000).map((r, idx) => (
                  <tr key={`${r.deviceId}-${r.pin}-${r.timestamp}-${idx}`}>
                    <td className="mono cell-nowrap">{r.pin}</td>
                    <td className="cell-nowrap">{r.name || <span style={{ color: 'var(--text-muted)' }}>-</span>}</td>
                    {showDeptColumn && (
                      <td className="cell-nowrap">
                        {r.department || <span style={{ color: 'var(--text-muted)' }}>-</span>}
                      </td>
                    )}
                    <td className="mono cell-nowrap">{r.timestamp}</td>
                    <td className="cell-nowrap">{r.deviceName}</td>
                    <td className="cell-nowrap">
                      <span
                        className={`badge ${
                          r.verifyMode === 15
                            ? 'badge-accent'
                            : r.verifyMode === 1
                              ? 'badge-ok'
                              : 'badge-neutral'
                        }`}
                      >
                        {r.verifyLabel}
                      </span>
                    </td>
                    <td className="cell-nowrap">{r.inOutLabel}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {filteredRaw.length > 2000 && (
              <div style={{ padding: 10, textAlign: 'center', color: 'var(--text-muted)', fontSize: 12 }}>
                界面仅预览前 2000 条（导出 Excel / .ABT / CSV / TXT 会包含全部 {filteredRaw.length} 条）
              </div>
            )}
          </div>
        )}
      </div>

      {/* 从考勤机增量拉取确认弹窗（默认预选已连接的考勤机） */}
      <ModalDialog
        open={Boolean(pullModal)}
        title="从考勤机增量拉取考勤"
        onClose={() => setPullModal(null)}
        lightDismiss
        width={540}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setPullModal(null)}>
              取消
            </button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || !pullModal || pullModal.deviceIds.length === 0 || pullModalInvalidDate}
              onClick={() => void handleConfirmPull()}
            >
              <RefreshCw size={13} />
              开始拉取并合并入库 ({pullModal?.deviceIds.length || 0} 台)
            </button>
          </>
        }
      >
        {pullModal && (
          <>
            <div
              style={{
                fontSize: 12.5,
                color: 'var(--text-secondary)',
                background: 'var(--success-soft)',
                padding: '8px 10px',
                borderRadius: 8,
              }}
            >
              <strong>只读拉取并自动去重合并</strong>：新记录会增量合并到本地累计库，不清空机器数据，也不会改变你当前页面的查询筛选范围。
            </div>

            <div className="field">
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                <label>
                  选择拉取的考勤机（已选 {pullModal.deviceIds.length} / {connectedDeviceIds.length} 台已连接）
                </label>
                {connectedDeviceIds.length > 0 && (
                  <div style={{ display: 'inline-flex', gap: 4 }}>
                    <button
                      type="button"
                      className="btn btn-sm"
                      style={{ height: 22, padding: '0 8px', fontSize: 11.5 }}
                      disabled={pullModal.deviceIds.length === connectedDeviceIds.length}
                      onClick={() =>
                        setPullModal({ ...pullModal, deviceIds: [...connectedDeviceIds] })
                      }
                    >
                      全选已连接
                    </button>
                    {pullModal.deviceIds.length > 0 && (
                      <button
                        type="button"
                        className="btn btn-sm btn-ghost"
                        style={{ height: 22, padding: '0 8px', fontSize: 11.5 }}
                        onClick={() => setPullModal({ ...pullModal, deviceIds: [] })}
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
                  const checked = pullModal.deviceIds.includes(d.id);
                  return (
                    <label key={d.id} className={`modal-device-row ${!conn ? 'disabled' : ''}`}>
                      <input
                        type="checkbox"
                        disabled={!conn}
                        checked={checked}
                        onChange={() =>
                          setPullModal({
                            ...pullModal,
                            deviceIds: checked
                              ? pullModal.deviceIds.filter((id) => id !== d.id)
                              : [...pullModal.deviceIds, d.id],
                          })
                        }
                      />
                      <span className={`dev-dot ${conn ? 'connected' : ''}`} />
                      <span style={{ fontWeight: 550 }}>{shortDeviceName(d.name)}</span>
                      <span className="mono" style={{ fontSize: 11, color: 'var(--text-muted)' }}>
                        {d.ip}
                      </span>
                      <span style={{ marginLeft: 'auto' }}>
                        {conn ? (
                          <span className="badge badge-ok">已连接</span>
                        ) : (
                          <span className="badge badge-neutral">未连接</span>
                        )}
                      </span>
                    </label>
                  );
                })}
              </div>
            </div>

            <div className="field">
              <label>拉取日期范围</label>
              <div className="toolbar-row" style={{ gap: 8 }}>
                <div className="date-range-group">
                  <Calendar size={14} />
                  <input
                    type="date"
                    className="mono"
                    aria-label="拉取开始日期"
                    value={pullModal.startDate}
                    onChange={(e) => setPullModal({ ...pullModal, startDate: e.target.value })}
                  />
                  <span style={{ fontSize: 12 }}>至</span>
                  <input
                    type="date"
                    className="mono"
                    aria-label="拉取结束日期"
                    value={pullModal.endDate}
                    onChange={(e) => setPullModal({ ...pullModal, endDate: e.target.value })}
                  />
                </div>

                <div className="segmented" role="group" aria-label="拉取快捷日期">
                  {(
                    [
                      ['today', '今天'],
                      ['7d', '近 7 天'],
                      ['month', '本月'],
                      ['prevMonth', '上月'],
                    ] as const
                  ).map(([key, label]) => (
                    <button
                      key={key}
                      type="button"
                      className={pullModalPreset === key ? 'active' : ''}
                      onClick={() => {
                        const [s, e] = getPresetRange(key);
                        setPullModal({ ...pullModal, startDate: s, endDate: e });
                      }}
                    >
                      {label}
                    </button>
                  ))}
                </div>
              </div>
              {pullModalInvalidDate && (
                <span style={{ fontSize: 12, color: 'var(--danger)' }}>开始日期不能晚于结束日期</span>
              )}
            </div>
          </>
        )}
      </ModalDialog>

      {/* 本地累计考勤库概览弹窗 */}
      <ModalDialog
        open={archiveModalOpen}
        title="本地累计考勤库概览"
        onClose={() => setArchiveModalOpen(false)}
        lightDismiss
        width={680}
        footer={
          <>
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              style={{ marginRight: 'auto', color: 'var(--danger)' }}
              disabled={!archiveOverview || archiveOverview.totalRecords === 0}
              onClick={openPruneModal}
              title="按截止日期清理电脑本地的历史打卡数据（绝不动考勤机数据）"
            >
              <Trash2 size={12} /> 清理本地历史数据…
            </button>
            <button type="button" className="btn btn-sm" onClick={() => void loadArchiveOverview()}>
              <RefreshCw size={12} className={loadingOverview ? 'spin' : ''} /> 刷新统计
            </button>
            <button type="button" className="btn btn-sm btn-primary" onClick={() => setArchiveModalOpen(false)}>
              关闭
            </button>
          </>
        }
      >
        {loadingOverview && !archiveOverview ? (
          <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-muted)', fontSize: 12.5 }}>
            正在统计本地累计考勤库...
          </div>
        ) : archiveOverview ? (
          <>
            <div className="overview-stats-strip">
              <div className="overview-stat-card">
                <span className="overview-stat-label">累计归档总数</span>
                <span className="overview-stat-value mono">{archiveOverview.totalRecords} 条</span>
              </div>
              <div className="overview-stat-card">
                <span className="overview-stat-label">本地日期覆盖</span>
                <span className="overview-stat-value mono">
                  {archiveOverview.earliestDate && archiveOverview.latestDate
                    ? `${archiveOverview.earliestDate} ~ ${archiveOverview.latestDate}`
                    : '暂无数据'}
                </span>
              </div>
              <div className="overview-stat-card">
                <span className="overview-stat-label">最近入库时间</span>
                <span className="overview-stat-value mono">{archiveOverview.updatedAt || '-'}</span>
              </div>
            </div>

            <div className="table-card" style={{ maxHeight: 320 }}>
              <div tabIndex={0} role="region" aria-label="各设备本地累计统计" className="table-wrapper">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>设备 / 来源</th>
                      <th className="cell-nowrap" style={{ width: 66 }}>机号</th>
                      <th className="cell-nowrap" style={{ width: 92 }}>累计条数</th>
                      <th className="cell-nowrap">日期覆盖范围</th>
                      <th className="cell-nowrap" style={{ width: 88 }}>快捷操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(archiveOverview.devices || []).map((st) => (
                      <tr key={st.deviceId}>
                        <td className="cell-nowrap">
                          <div style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                            <span style={{ fontWeight: 550 }}>{shortDeviceName(st.deviceName || st.deviceId)}</span>
                            {!st.configured && <span className="badge badge-neutral">外部导入</span>}
                          </div>
                        </td>
                        <td className="mono cell-nowrap">{st.sensorId ? `#${st.sensorId}` : '-'}</td>
                        <td className="mono cell-nowrap">
                          {st.count > 0 ? (
                            <span className="badge badge-ok">{st.count} 条</span>
                          ) : (
                            <span className="badge badge-neutral">0 条</span>
                          )}
                        </td>
                        <td className="mono cell-nowrap" style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                          {st.earliestDate && st.latestDate
                            ? `${st.earliestDate} ~ ${st.latestDate}`
                            : '尚未拉取'}
                        </td>
                        <td className="cell-nowrap">
                          {st.configured && st.count > 0 ? (
                            <button
                              type="button"
                              className="btn btn-sm btn-ghost"
                              onClick={() => {
                                setFilterDeviceId(st.deviceId);
                                if (st.earliestDate && st.latestDate) {
                                  if (endDate < st.earliestDate || startDate > st.latestDate) {
                                    setStartDate(st.earliestDate);
                                    setEndDate(st.latestDate);
                                  }
                                }
                                setArchiveModalOpen(false);
                              }}
                            >
                              仅查此机
                            </button>
                          ) : (
                            <span style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>-</span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          </>
        ) : null}
      </ModalDialog>

      {/* 清理本地考勤累计库弹窗（仅清理电脑本地数据，不动考勤机） */}
      <ModalDialog
        open={Boolean(pruneModal)}
        title="清理本地考勤累计库"
        onClose={() => setPruneModal(null)}
        width={540}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setPruneModal(null)}>
              取消
            </button>
            <button
              type="button"
              className="btn btn-danger"
              disabled={pruning || !prunePreview || prunePreview.matchCount === 0}
              onClick={() => void handleConfirmPrune()}
            >
              <Trash2 size={13} />
              {pruning
                ? '清理中…'
                : prunePreview && prunePreview.matchCount > 0
                  ? `确认清理本地 ${prunePreview.matchCount} 条`
                  : '无符合条件的记录'}
            </button>
          </>
        }
      >
        {pruneModal && (
          <>
            <div
              style={{
                fontSize: 12.5,
                color: 'var(--text-secondary)',
                background: 'var(--success-soft)',
                padding: '8px 10px',
                borderRadius: 8,
              }}
            >
              <strong>仅清理电脑本地数据</strong>：绝不修改或删除任何考勤机内的记录；执行前自动将要清理的数据备份到{' '}
              <code className="mono">backups/</code> 目录。
            </div>

            <div className="field">
              <label>清理此日期之前的数据（不含所选当天）</label>
              <div className="toolbar-row" style={{ gap: 8 }}>
                <input
                  type="date"
                  className="input mono"
                  aria-label="清理截止日期"
                  value={pruneModal.beforeDate}
                  onChange={(e) => setPruneModal({ ...pruneModal, beforeDate: e.target.value })}
                />
                <div className="segmented" role="group" aria-label="快捷保留期限">
                  {(
                    [
                      ['thisMonth', '仅保留本月'],
                      ['1m', '保留近1月'],
                      ['3m', '保留近3月'],
                      ['6m', '保留近6月'],
                    ] as const
                  ).map(([key, label]) => {
                    const target = getCutoffPresetDate(key);
                    return (
                      <button
                        key={key}
                        type="button"
                        className={pruneModal.beforeDate === target ? 'active' : ''}
                        onClick={() => setPruneModal({ ...pruneModal, beforeDate: target })}
                      >
                        {label}
                      </button>
                    );
                  })}
                </div>
              </div>
              <span style={{ fontSize: 11.5, color: 'var(--text-muted)' }}>
                将清理 <strong>{pruneModal.beforeDate || '-'}</strong> 00:00:00 之前的所有本地打卡，完整保留{' '}
                <strong>{pruneModal.beforeDate || '-'}</strong> 及之后的数据。
              </span>
            </div>

            <div className="field">
              <label>清理设备范围（可选）</label>
              <select
                className="select"
                aria-label="清理设备范围"
                value={pruneModal.deviceId}
                onChange={(e) => setPruneModal({ ...pruneModal, deviceId: e.target.value })}
              >
                <option value="">全部考勤机（含外部导入 .ABT）</option>
                {(archiveOverview?.devices || []).map((st) => (
                  <option key={st.deviceId} value={st.deviceId}>
                    仅清理「{shortDeviceName(st.deviceName || st.deviceId)}」（累计 {st.count} 条）
                  </option>
                ))}
              </select>
            </div>

            {prunePreview && (
              <div className="overview-stats-strip">
                <div className="overview-stat-card">
                  <span className="overview-stat-label">即将清理（本地）</span>
                  <span
                    className="overview-stat-value mono"
                    style={{ color: prunePreview.matchCount > 0 ? 'var(--danger)' : 'var(--text-muted)' }}
                  >
                    {prunePreview.matchCount} 条
                  </span>
                </div>
                <div className="overview-stat-card">
                  <span className="overview-stat-label">清理后本地保留</span>
                  <span className="overview-stat-value mono" style={{ color: 'var(--success)' }}>
                    {prunePreview.remainCount} 条
                  </span>
                </div>
                <div className="overview-stat-card">
                  <span className="overview-stat-label">涉及历史日期</span>
                  <span className="overview-stat-value mono" style={{ fontSize: 12 }}>
                    {prunePreview.earliestDate && prunePreview.latestDate
                      ? `${prunePreview.earliestDate} ~ ${prunePreview.latestDate}`
                      : '无命中记录'}
                  </span>
                </div>
              </div>
            )}
          </>
        )}
      </ModalDialog>

      {/* 单日打卡明细弹窗 */}
      <ModalDialog
        open={Boolean(detailRow)}
        title={
          detailRow
            ? `${detailRow.name || `工号 ${detailRow.pin}`} · ${detailRow.date} 打卡明细`
            : '当日打卡明细'
        }
        onClose={() => setDetailRow(null)}
        lightDismiss
        width={640}
        footer={
          <>
            {detailRow && detailRecords.length > 0 && (
              <button
                type="button"
                className="btn btn-sm"
                onClick={() =>
                  void handleExport('xlsx', {
                    records: detailRecords,
                    dailySummary: [detailRow],
                    filePrefix: `考勤明细_${detailRow.date}_${detailRow.pin}_${detailRow.name || ''}`,
                    scopeNote: `${detailRow.date} · ${detailRow.name || detailRow.pin}`,
                  })
                }
              >
                <FileSpreadsheet size={13} /> 导出当日 Excel ({detailRecords.length} 条)
              </button>
            )}
            <button type="button" className="btn btn-sm btn-primary" onClick={() => setDetailRow(null)}>
              关闭
            </button>
          </>
        }
      >
        {detailRow && (
          <>
            <div className="overview-stats-strip">
              <div className="overview-stat-card">
                <span className="overview-stat-label">员工 / 工号</span>
                <span className="overview-stat-value">
                  {detailRow.name || '-'}{' '}
                  <span className="mono" style={{ color: 'var(--text-muted)', fontSize: 12 }}>
                    #{detailRow.pin}
                  </span>
                </span>
              </div>
              <div className="overview-stat-card">
                <span className="overview-stat-label">首末打卡 / 跨度</span>
                <span className="overview-stat-value mono">
                  {detailRow.firstTime} ~ {detailRow.lastTime || '缺失'} ({detailRow.workDuration})
                </span>
              </div>
              <div className="overview-stat-card">
                <span className="overview-stat-label">当日共打卡</span>
                <span className="overview-stat-value" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <span className="mono">{detailRow.punchCount} 次</span>
                  {detailRow.abnormal ? (
                    <span className="badge badge-warn">{detailRow.abnormalNote}</span>
                  ) : (
                    <span className="badge badge-ok">正常</span>
                  )}
                </span>
              </div>
            </div>

            <div className="table-card" style={{ maxHeight: 340 }}>
              <div tabIndex={0} role="region" aria-label="当日打卡时间轴" className="table-wrapper">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th className="cell-nowrap" style={{ width: 52 }}>序号</th>
                      <th className="cell-nowrap" style={{ width: 165 }}>打卡时间</th>
                      <th>打卡设备</th>
                      <th className="cell-nowrap" style={{ width: 90 }}>验证方式</th>
                      <th className="cell-nowrap" style={{ width: 92 }}>考勤状态</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detailRecords.map((r, i) => (
                      <tr key={`${r.deviceId}-${r.timestamp}-${i}`}>
                        <td className="mono cell-nowrap" style={{ color: 'var(--text-muted)' }}>
                          {i + 1}
                        </td>
                        <td className="mono cell-nowrap" style={{ fontWeight: 550 }}>
                          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
                            <Clock size={12} style={{ color: 'var(--text-muted)' }} />
                            {r.timestamp}
                          </span>
                        </td>
                        <td className="cell-nowrap">{shortDeviceName(r.deviceName)}</td>
                        <td className="cell-nowrap">
                          <span
                            className={`badge ${
                              r.verifyMode === 15
                                ? 'badge-accent'
                                : r.verifyMode === 1
                                  ? 'badge-ok'
                                  : 'badge-neutral'
                            }`}
                          >
                            {r.verifyLabel}
                          </span>
                        </td>
                        <td className="cell-nowrap">{r.inOutLabel}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          </>
        )}
      </ModalDialog>
    </>
  );
}
