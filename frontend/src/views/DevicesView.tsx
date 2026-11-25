import React, { useMemo, useState } from 'react';
import {
  Clock,
  Cpu,
  FolderOpen,
  LayoutGrid,
  List,
  Loader2,
  Plug,
  Plus,
  RefreshCw,
  Settings,
  Trash2,
  Unplug,
  WifiOff,
} from 'lucide-react';
import {
  DeviceConfig,
  DeviceStatus,
  RuntimeHealth,
} from '../../bindings/zk_mac/internal/models';
import * as ZK from '../../bindings/zk_mac/internal/service/zkservice';
import { ModalDialog } from '../components/ModalDialog';

interface DevicesViewProps {
  devices: DeviceConfig[];
  statuses: Record<string, DeviceStatus | undefined>;
  runtime: RuntimeHealth | null;
  busy: boolean;
  onDevicesChange: (list: DeviceConfig[]) => void;
  onStatusesChange: (map: Record<string, DeviceStatus | undefined>) => void;
  onRefreshRuntime: () => Promise<void>;
  onAuditChanged: () => void;
  onNotify: (type: 'info' | 'success' | 'error', text: string) => void;
  setBusy: (v: boolean) => void;
}

const emptyDevice = (): DeviceConfig => ({
  id: '',
  name: '',
  ip: '',
  port: 4370,
  password: 0,
  machineNumber: 1,
  sensorId: '',
  note: '',
  enabled: true,
});

function formatDrift(sec: number): { label: string; warn: boolean } {
  const abs = Math.abs(sec);
  if (abs <= 3) return { label: '时间准确', warn: false };
  const dir = sec > 0 ? '快' : '慢';
  if (abs < 60) return { label: `${dir} ${abs}s`, warn: true };
  const mins = Math.floor(abs / 60);
  const rem = abs % 60;
  return { label: `${dir} ${mins}分${rem}秒`, warn: true };
}

function formatFirmwareBrief(fw?: string): string {
  if (!fw) return '-';
  // e.g. "Ver 6.60 Apr 28 2025" -> "Ver 6.60 (2025)"
  const yr = fw.match(/\b(20\d{2})\b/);
  const ver = fw.match(/^Ver\s+[\d.]+/i);
  if (ver && yr) return `${ver[0]} (${yr[1]})`;
  return fw;
}

export function DevicesView({
  devices,
  statuses,
  runtime,
  busy,
  onDevicesChange,
  onStatusesChange,
  onRefreshRuntime,
  onAuditChanged,
  onNotify,
  setBusy,
}: DevicesViewProps) {
  const [editing, setEditing] = useState<DeviceConfig | null>(null);
  const [editingIsNew, setEditingIsNew] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<DeviceConfig | null>(null);

  const openCreateModal = () => {
    setEditingIsNew(true);
    setEditing(emptyDevice());
  };

  const openEditModal = (dev: DeviceConfig) => {
    setEditingIsNew(false);
    setEditing({ ...dev });
  };
  const [statusFilter, setStatusFilter] = useState<'all' | 'connected' | 'disconnected'>('all');
  const [viewMode, setViewMode] = useState<'grid' | 'list'>('list');
  const [pendingOp, setPendingOp] = useState<{
    deviceId: string;
    action: 'connect' | 'disconnect' | 'refresh' | 'syncTime';
  } | null>(null);

  const connectedCount = useMemo(
    () => devices.filter((d) => !!statuses[d.id]?.connected).length,
    [devices, statuses]
  );
  const disconnectedCount = devices.length - connectedCount;

  const filteredDevices = useMemo(() => {
    return devices.filter((d) => {
      const conn = !!statuses[d.id]?.connected;
      if (statusFilter === 'connected') return conn;
      if (statusFilter === 'disconnected') return !conn;
      return true;
    });
  }, [devices, statuses, statusFilter]);

  const handleConnectAll = async () => {
    setBusy(true);
    try {
      const map = (await ZK.ConnectAllDevices()) || {};
      onStatusesChange(map);
      const ok = Object.values(map).filter((s) => s?.connected).length;
      const total = devices.filter((d) => d.enabled).length;
      onNotify(ok === total ? 'success' : 'info', `已连接 ${ok} / ${total} 台考勤机（保持长连接）`);
    } catch (err: any) {
      onNotify('error', `连接失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const handleDisconnectAll = async () => {
    setBusy(true);
    try {
      const map = (await ZK.DisconnectAllDevices()) || {};
      onStatusesChange(map);
      onNotify('info', '已断开全部考勤机连接');
    } catch (err: any) {
      onNotify('error', `断开失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const handleSyncAllTimes = async () => {
    setBusy(true);
    try {
      const map = (await ZK.SyncAllDeviceTimes()) || {};
      onStatusesChange({ ...statuses, ...map });
      onAuditChanged();
      const okCnt = Object.values(map).filter((s) => s && !s.error).length;
      onNotify('success', `已同步 ${okCnt} 台考勤机时间为本机当前时间`);
    } catch (err: any) {
      onNotify('error', `批量校时失败：${err?.message || err}`);
    } finally {
      setBusy(false);
    }
  };

  const handleSyncTimeOne = async (dev: DeviceConfig) => {
    setBusy(true);
    setPendingOp({ deviceId: dev.id, action: 'syncTime' });
    try {
      const st = await ZK.SyncDeviceTime(dev.id);
      onStatusesChange({ ...statuses, [dev.id]: st });
      onAuditChanged();
      if (!st.error) {
        onNotify('success', `${dev.name} 时间已校准为 ${st.deviceTime}`);
      } else {
        onNotify('error', `${dev.name} 校时失败：${st.error}`);
      }
    } catch (err: any) {
      onNotify('error', `校时失败：${err?.message || err}`);
    } finally {
      setPendingOp(null);
      setBusy(false);
    }
  };

  const handleConnectOne = async (dev: DeviceConfig) => {
    setBusy(true);
    setPendingOp({ deviceId: dev.id, action: 'connect' });
    try {
      const st = await ZK.ConnectDevice(dev.id);
      onStatusesChange({ ...statuses, [dev.id]: st });
      if (st.connected) {
        onNotify('success', `已连接 ${dev.name}（${st.firmware}，后续操作将复用此连接）`);
      } else {
        onNotify('error', `${dev.name} 连接失败：${st.error}`);
      }
    } catch (err: any) {
      onNotify('error', `连接失败：${err?.message || err}`);
    } finally {
      setPendingOp(null);
      setBusy(false);
    }
  };

  const handleDisconnectOne = async (dev: DeviceConfig) => {
    setBusy(true);
    setPendingOp({ deviceId: dev.id, action: 'disconnect' });
    try {
      const st = await ZK.DisconnectDevice(dev.id);
      onStatusesChange({ ...statuses, [dev.id]: st });
      onNotify('info', `已断开 ${dev.name}`);
    } catch (err: any) {
      onNotify('error', `断开失败：${err?.message || err}`);
    } finally {
      setPendingOp(null);
      setBusy(false);
    }
  };

  const handleRefreshOne = async (dev: DeviceConfig) => {
    setBusy(true);
    setPendingOp({ deviceId: dev.id, action: 'refresh' });
    try {
      const st = await ZK.ProbeDevice(dev.id);
      onStatusesChange({ ...statuses, [dev.id]: st });
      if (st.online) {
        onNotify('success', `已刷新 ${dev.name} 实时状态`);
      } else {
        onNotify('error', `${dev.name} 刷新失败：${st.error}`);
      }
    } catch (err: any) {
      onNotify('error', `刷新失败：${err?.message || err}`);
    } finally {
      setPendingOp(null);
      setBusy(false);
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!editing) return;
    try {
      const next = (await ZK.SaveDevice(editing)) || [];
      onDevicesChange(next);
      const latestSt = await ZK.GetCachedStatuses().catch(() => null);
      if (latestSt) onStatusesChange(latestSt);
      setEditing(null);
      onNotify('success', `已保存设备配置：${editing.name || editing.id}`);
    } catch (err: any) {
      onNotify('error', `保存失败：${err?.message || err}`);
    }
  };

  const handleDelete = async () => {
    if (!confirmDelete) return;
    try {
      const next = (await ZK.DeleteDevice(confirmDelete.id)) || [];
      onDevicesChange(next);
      const latestSt = await ZK.GetCachedStatuses().catch(() => null);
      if (latestSt) onStatusesChange(latestSt);
      setConfirmDelete(null);
      onNotify('info', `已移除设备配置：${confirmDelete.name}`);
    } catch (err: any) {
      onNotify('error', `删除失败：${err?.message || err}`);
    }
  };

  return (
    <>
      <div className="toolbar-row" style={{ justifyContent: 'space-between' }}>
        <div className="toolbar-row">
          <button type="button" className="btn btn-primary" disabled={busy} onClick={handleConnectAll}>
            <Plug size={14} />
            全部连接
          </button>
          <button
            type="button"
            className="btn"
            disabled={busy || connectedCount === 0}
            onClick={handleDisconnectAll}
          >
            <Unplug size={14} />
            全部断开 {connectedCount > 0 ? `(${connectedCount})` : ''}
          </button>
          <button
            type="button"
            className="btn"
            disabled={busy || connectedCount === 0}
            title={connectedCount === 0 ? '请先手动连接至少一台考勤机' : '将当前已连接考勤机的时钟同步为本机时间'}
            onClick={handleSyncAllTimes}
          >
            <Clock size={14} />
            同步已连接时间 {connectedCount > 0 ? `(${connectedCount})` : ''}
          </button>
          <button type="button" className="btn" disabled={busy} onClick={openCreateModal}>
            <Plus size={14} />
            新增设备
          </button>
        </div>

        <div className="toolbar-row">
          <div className="segmented" role="group" aria-label="按连接状态筛选">
            <button type="button" className={statusFilter === 'all' ? 'active' : ''} onClick={() => setStatusFilter('all')}>
              全部 ({devices.length})
            </button>
            <button type="button" className={statusFilter === 'connected' ? 'active' : ''} onClick={() => setStatusFilter('connected')}>
              已连接 ({connectedCount})
            </button>
            <button type="button" className={statusFilter === 'disconnected' ? 'active' : ''} onClick={() => setStatusFilter('disconnected')}>
              未连接 ({disconnectedCount})
            </button>
          </div>

          <div className="segmented" role="group" aria-label="显示模式">
            <button
              type="button"
              className={viewMode === 'list' ? 'active' : ''}
              title="紧凑列表视图（默认，适合多台设备全览）"
              aria-label="列表视图"
              onClick={() => setViewMode('list')}
              style={{ display: 'inline-flex', alignItems: 'center', padding: '4px 8px' }}
            >
              <List size={13} />
            </button>
            <button
              type="button"
              className={viewMode === 'grid' ? 'active' : ''}
              title="卡片视图"
              aria-label="卡片视图"
              onClick={() => setViewMode('grid')}
              style={{ display: 'inline-flex', alignItems: 'center', padding: '4px 8px' }}
            >
              <LayoutGrid size={13} />
            </button>
          </div>

          <button
            type="button"
            className="btn btn-ghost btn-sm"
            disabled={busy}
            title="检查 Wine 64 位 SDK 引擎状态"
            onClick={onRefreshRuntime}
          >
            <Cpu size={14} />
          </button>
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            title="打开本地数据目录"
            onClick={() => ZK.OpenDataDirectory().catch((e) => onNotify('error', String(e)))}
          >
            <FolderOpen size={14} />
          </button>
        </div>
      </div>

      {runtime && !runtime.ready && (
        <div className="notice-bar error">
          <span>Wine 64 位 SDK 引擎尚未就绪：{runtime.error || '请确认已安装并放行 Wine Stable.app'}</span>
          <button type="button" className="btn btn-sm" onClick={onRefreshRuntime}>
            重试检查
          </button>
        </div>
      )}

      {viewMode === 'grid' ? (
        <div className="device-grid">
          {filteredDevices.map((dev) => {
            const st = statuses[dev.id];
            const isConnected = !!st?.connected;
            const hasData = !!st && (st.online || st.userCount > 0 || !!st.sn);
            const drift = st?.hasTimeDrift ? formatDrift(st.timeDriftSec || 0) : null;
            const protoLabel = st?.logMode ? (st.logMode === 'with_mask' ? 'WithMask' : 'Std') : '';
            const hwSummary = st?.platform
              ? `${st.platform}${st.faceVersion ? ` · Face ${st.faceVersion}` : ''}`
              : dev.note || '-';
            const hwTooltip = [
              st?.sn ? `序列号: ${st.sn}` : '',
              st?.firmware ? `固件: ${st.firmware}` : '',
              st?.platform ? `平台: ${st.platform}` : '',
              st?.faceVersion ? `人脸算法: ${st.faceVersion}` : '',
              protoLabel ? `考勤协议: ${protoLabel}` : '',
            ]
              .filter(Boolean)
              .join(' | ');

            return (
              <div
                key={dev.id}
                className={`device-card ${isConnected ? 'connected' : 'disconnected'}`}
              >
                <div className="device-card-head">
                  <div style={{ minWidth: 0 }}>
                    <h3 className="device-name">
                      <span className={`dev-dot ${isConnected ? 'connected' : ''}`} />
                      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {dev.name}
                      </span>
                    </h3>
                    <div className="device-ip">
                      <span>{dev.ip}:{dev.port}</span>
                      <span>·</span>
                      <span>机号 {dev.sensorId || dev.id}</span>
                      {protoLabel && (
                        <>
                          <span>·</span>
                          <span>{protoLabel}</span>
                        </>
                      )}
                    </div>
                  </div>

                  <div style={{ display: 'inline-flex', alignItems: 'center', gap: 4, flexShrink: 0 }}>
                    {isConnected ? (
                      <span className="badge badge-ok">
                        <Plug size={10} /> 已连接
                      </span>
                    ) : st?.error ? (
                      <span className="badge badge-danger" title={st.error}>
                        <WifiOff size={10} /> 失败
                      </span>
                    ) : (
                      <span className="badge badge-neutral">未连接</span>
                    )}
                    <button
                      type="button"
                      className="btn btn-sm btn-ghost"
                      style={{ padding: '0 6px' }}
                      title="编辑设备配置 / 移除设备"
                      aria-label={`编辑 ${dev.name}`}
                      onClick={() => openEditModal(dev)}
                    >
                      <Settings size={13} />
                    </button>
                  </div>
                </div>

                <div className="stat-grid" title={isConnected ? '实时统计' : '上次连接缓存统计'}>
                  <div className="stat-box">
                    <div className="stat-val">{hasData ? st.userCount : '-'}</div>
                    <div className="stat-lbl">员工</div>
                  </div>
                  <div className="stat-box">
                    <div className="stat-val">{hasData ? st.fpCount : '-'}</div>
                    <div className="stat-lbl">指纹</div>
                  </div>
                  <div className="stat-box">
                    <div className="stat-val">{hasData ? st.faceCount : '-'}</div>
                    <div className="stat-lbl">人脸</div>
                  </div>
                  <div className="stat-box">
                    <div className="stat-val">{hasData ? st.logCount : '-'}</div>
                    <div className="stat-lbl">记录</div>
                  </div>
                </div>

                <div className="meta-list">
                  <div className="meta-row">
                    <span className="meta-lbl">{isConnected ? '设备时间' : '上次时间'}</span>
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                      <span className="meta-val">
                        {hasData && st.deviceTime ? st.deviceTime.slice(5) : '-'}
                      </span>
                      {drift && (
                        <span
                          className={`badge ${
                            !isConnected ? 'badge-neutral' : drift.warn ? 'badge-warn' : 'badge-ok'
                          }`}
                          title={isConnected ? '实时时间偏差' : '上次连接时的历史偏差'}
                        >
                          {drift.label}
                        </span>
                      )}
                    </span>
                  </div>
                  <div className="meta-row" title={hwTooltip}>
                    <span className="meta-lbl">平台 / 算法</span>
                    <span className="meta-val">{hwSummary}</span>
                  </div>
                  <div className="meta-row" title={hwTooltip}>
                    <span className="meta-lbl">序列号 / 固件</span>
                    <span className="meta-val">
                      {st?.sn ? `${st.sn} · ${formatFirmwareBrief(st.firmware)}` : formatFirmwareBrief(st?.firmware)}
                    </span>
                  </div>
                  {st?.error && (
                    <div style={{ color: 'var(--danger)', fontSize: 11.5, marginTop: 2 }}>{st.error}</div>
                  )}
                </div>

                <div className="device-card-foot">
                  {isConnected ? (
                    <>
                      <button
                        type="button"
                        className="btn btn-sm"
                        style={{ color: 'var(--danger)', borderColor: 'var(--danger-soft)' }}
                        disabled={busy}
                        onClick={() => handleDisconnectOne(dev)}
                      >
                        {pendingOp?.deviceId === dev.id && pendingOp.action === 'disconnect' ? (
                          <>
                            <Loader2 size={12} className="spin" /> 断开中…
                          </>
                        ) : (
                          <>
                            <Unplug size={12} /> 断开
                          </>
                        )}
                      </button>
                      <button
                        type="button"
                        className="btn btn-sm"
                        disabled={busy}
                        onClick={() => handleRefreshOne(dev)}
                        title="复用当前连接刷新实时状态"
                      >
                        <RefreshCw
                          size={12}
                          className={pendingOp?.deviceId === dev.id && pendingOp.action === 'refresh' ? 'spin' : ''}
                        />{' '}
                        {pendingOp?.deviceId === dev.id && pendingOp.action === 'refresh' ? '刷新中…' : '刷新'}
                      </button>
                      <button
                        type="button"
                        className={`btn btn-sm ${drift?.warn ? 'btn-warn' : ''}`}
                        style={{ marginLeft: 'auto' }}
                        disabled={busy}
                        title="将此考勤机时间同步为本机当前时间"
                        onClick={() => handleSyncTimeOne(dev)}
                      >
                        {pendingOp?.deviceId === dev.id && pendingOp.action === 'syncTime' ? (
                          <>
                            <Loader2 size={12} className="spin" /> 校时中…
                          </>
                        ) : (
                          <>
                            <Clock size={12} /> {drift?.warn ? '立即校时' : '校时'}
                          </>
                        )}
                      </button>
                    </>
                  ) : (
                    <>
                      <button
                        type="button"
                        className="btn btn-sm btn-primary"
                        disabled={busy}
                        onClick={() => handleConnectOne(dev)}
                      >
                        {pendingOp?.deviceId === dev.id && pendingOp.action === 'connect' ? (
                          <>
                            <Loader2 size={12} className="spin" /> 连接中…
                          </>
                        ) : (
                          <>
                            <Plug size={12} /> 连接设备
                          </>
                        )}
                      </button>
                      <span style={{ marginLeft: 'auto', fontSize: 11.5, color: 'var(--text-muted)' }}>
                        连接后可刷新 / 校时
                      </span>
                    </>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      ) : (
        <div className="table-card">
          <div tabIndex={0} role="region" aria-label="考勤设备列表" className="table-wrapper">
            <table className="data-table">
              <thead>
                <tr>
                  <th className="cell-nowrap">考勤机</th>
                  <th className="cell-nowrap">状态</th>
                  <th className="cell-nowrap">IP / 机号</th>
                  <th className="cell-nowrap">员工 / 指纹 / 人脸 / 记录</th>
                  <th className="cell-nowrap">设备时间</th>
                  <th className="cell-nowrap" style={{ textAlign: 'right' }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {filteredDevices.map((dev) => {
                  const st = statuses[dev.id];
                  const isConnected = !!st?.connected;
                  const hasData = !!st && (st.online || st.userCount > 0 || !!st.sn);
                  const drift = st?.hasTimeDrift ? formatDrift(st.timeDriftSec || 0) : null;
                  const protoLabel = st?.logMode ? (st.logMode === 'with_mask' ? 'WithMask' : 'Std') : '';
                  return (
                    <tr key={dev.id}>
                      <td className="cell-nowrap">
                        <div style={{ display: 'flex', alignItems: 'center', gap: 7, fontWeight: 600 }}>
                          <span className={`dev-dot ${isConnected ? 'connected' : ''}`} />
                          <span>{dev.name}</span>
                        </div>
                        {st?.sn && (
                          <div className="mono" style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>
                            SN: {st.sn}
                          </div>
                        )}
                      </td>
                      <td className="cell-nowrap">
                        {isConnected ? (
                          <span className="badge badge-ok"><Plug size={10} /> 已连接</span>
                        ) : st?.error ? (
                          <span className="badge badge-danger" title={st.error}><WifiOff size={10} /> 失败</span>
                        ) : (
                          <span className="badge badge-neutral">未连接</span>
                        )}
                      </td>
                      <td className="mono cell-nowrap" style={{ fontSize: 12 }}>
                        <div>{dev.ip}:{dev.port}</div>
                        <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>
                          机号 {dev.sensorId || dev.id}{protoLabel ? ` · ${protoLabel}` : ''}
                        </div>
                      </td>
                      <td className="mono cell-nowrap" style={{ fontSize: 12 }}>
                        {hasData ? (
                          <span>
                            {st.userCount} / {st.fpCount} / {st.faceCount} / {st.logCount}
                          </span>
                        ) : (
                          <span style={{ color: 'var(--text-muted)' }}>-</span>
                        )}
                      </td>
                      <td className="cell-nowrap">
                        <div style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                          <span className="mono" style={{ fontSize: 12 }}>
                            {hasData && st.deviceTime ? st.deviceTime.slice(5) : '-'}
                          </span>
                          {drift && (
                            <span className={`badge ${!isConnected ? 'badge-neutral' : drift.warn ? 'badge-warn' : 'badge-ok'}`}>
                              {drift.label}
                            </span>
                          )}
                        </div>
                      </td>
                      <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                        <div style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
                          {isConnected ? (
                            <>
                              <button
                                type="button"
                                className="btn btn-sm"
                                style={{ color: 'var(--danger)', borderColor: 'var(--danger-soft)' }}
                                disabled={busy}
                                onClick={() => handleDisconnectOne(dev)}
                              >
                                {pendingOp?.deviceId === dev.id && pendingOp.action === 'disconnect' ? (
                                  <>
                                    <Loader2 size={12} className="spin" /> 断开中…
                                  </>
                                ) : (
                                  <>
                                    <Unplug size={12} /> 断开
                                  </>
                                )}
                              </button>
                              <button
                                type="button"
                                className="btn btn-sm"
                                disabled={busy}
                                title="刷新此设备实时状态"
                                onClick={() => handleRefreshOne(dev)}
                              >
                                <RefreshCw
                                  size={12}
                                  className={pendingOp?.deviceId === dev.id && pendingOp.action === 'refresh' ? 'spin' : ''}
                                />
                              </button>
                              <button
                                type="button"
                                className={`btn btn-sm ${drift?.warn ? 'btn-warn' : ''}`}
                                disabled={busy}
                                onClick={() => handleSyncTimeOne(dev)}
                              >
                                {pendingOp?.deviceId === dev.id && pendingOp.action === 'syncTime' ? (
                                  <>
                                    <Loader2 size={12} className="spin" /> 校时中…
                                  </>
                                ) : (
                                  <>
                                    <Clock size={12} /> 校时
                                  </>
                                )}
                              </button>
                            </>
                          ) : (
                            <button type="button" className="btn btn-sm btn-primary" disabled={busy} onClick={() => handleConnectOne(dev)}>
                              {pendingOp?.deviceId === dev.id && pendingOp.action === 'connect' ? (
                                <>
                                  <Loader2 size={12} className="spin" /> 连接中…
                                </>
                              ) : (
                                <>
                                  <Plug size={12} /> 连接
                                </>
                              )}
                            </button>
                          )}
                          <button
                            type="button"
                            className="btn btn-sm btn-ghost"
                            title="编辑设备配置 / 移除设备"
                            onClick={() => openEditModal(dev)}
                          >
                            <Settings size={13} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <ModalDialog
        open={!!editing}
        title={editingIsNew ? '新增考勤机' : '编辑考勤机配置'}
        onClose={() => setEditing(null)}
        lightDismiss
        footer={
          <>
            {editing && !editingIsNew && (
              <button
                type="button"
                className="btn btn-ghost"
                style={{ marginRight: 'auto', color: 'var(--danger)' }}
                onClick={() => {
                  const target = devices.find((d) => d.id === editing.id);
                  setEditing(null);
                  if (target) setConfirmDelete(target);
                }}
              >
                <Trash2 size={13} /> 移除设备配置
              </button>
            )}
            <button type="button" className="btn" onClick={() => setEditing(null)}>
              取消
            </button>
            <button type="submit" form="device-edit-form" className="btn btn-primary">
              保存配置
            </button>
          </>
        }
      >
        {editing && (
          <form id="device-edit-form" onSubmit={handleSave} className="form-grid">
            <div className="field">
              <label htmlFor="dev-id">
                设备 ID（唯一{!editingIsNew ? '，编辑时不可修改' : ''}）
              </label>
              <input
                id="dev-id"
                className="input"
                required
                disabled={!editingIsNew}
                value={editing.id}
                onChange={(e) => setEditing({ ...editing, id: e.target.value })}
                placeholder="如 230"
                autoFocus={editingIsNew}
              />
            </div>
            <div className="field">
              <label htmlFor="dev-name">显示名称</label>
              <input
                id="dev-name"
                className="input"
                required
                value={editing.name}
                onChange={(e) => setEditing({ ...editing, name: e.target.value })}
                placeholder="如 考勤机 230 (xFace600)"
              />
            </div>
            <div className="field">
              <label htmlFor="dev-ip">IP 地址</label>
              <input
                id="dev-ip"
                className="input mono"
                required
                value={editing.ip}
                onChange={(e) => setEditing({ ...editing, ip: e.target.value })}
                placeholder="192.168.1.201"
              />
            </div>
            <div className="field">
              <label htmlFor="dev-port">端口</label>
              <input
                id="dev-port"
                type="number"
                className="input mono"
                value={editing.port}
                onChange={(e) => setEditing({ ...editing, port: Number(e.target.value) || 4370 })}
              />
            </div>
            <div className="field">
              <label htmlFor="dev-pwd">通讯密码（纯数字，无填 0）</label>
              <input
                id="dev-pwd"
                type="number"
                className="input mono"
                value={editing.password}
                onChange={(e) => setEditing({ ...editing, password: Number(e.target.value) || 0 })}
              />
            </div>
            <div className="field">
              <label htmlFor="dev-sensor">导出设备编号 (SensorID)</label>
              <input
                id="dev-sensor"
                className="input mono"
                value={editing.sensorId}
                onChange={(e) => setEditing({ ...editing, sensorId: e.target.value })}
                placeholder="如 30"
              />
            </div>
            <div className="field full">
              <label htmlFor="dev-note">备注</label>
              <input
                id="dev-note"
                className="input"
                value={editing.note}
                onChange={(e) => setEditing({ ...editing, note: e.target.value })}
              />
            </div>
            <div className="field full">
              <label className="check-item">
                <input
                  type="checkbox"
                  checked={editing.enabled}
                  onChange={(e) => setEditing({ ...editing, enabled: e.target.checked })}
                />
                启用该考勤机
              </label>
            </div>
          </form>
        )}
      </ModalDialog>

      <ModalDialog
        open={!!confirmDelete}
        title="确认移除设备配置"
        onClose={() => setConfirmDelete(null)}
        width={420}
        footer={
          <>
            <button type="button" className="btn" autoFocus onClick={() => setConfirmDelete(null)}>
              取消
            </button>
            <button type="button" className="btn btn-danger" onClick={handleDelete}>
              确认移除
            </button>
          </>
        }
      >
        <p style={{ margin: 0 }}>
          确定要从本地配置移除 <strong>{confirmDelete?.name}</strong>（{confirmDelete?.ip}）吗？此操作仅移除本地配置，不会影响考勤机内部数据。
        </p>
      </ModalDialog>
    </>
  );
}
