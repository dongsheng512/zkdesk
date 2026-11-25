import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Camera,
  ClipboardList,
  Clock,
  Loader2,
  MonitorSmartphone,
  Users,
} from 'lucide-react';
import appIconUrl from './assets/zkdesk-icon.png';
import { Events } from '@wailsio/runtime';
import {
  DeviceConfig,
  DeviceStatus,
  FacePhotoItem,
  OperationLog,
  ProgressEvent,
  PullLogsResponse,
  RuntimeHealth,
  UserMatrixResponse,
} from '../bindings/zk_mac/internal/models';
import * as ZK from '../bindings/zk_mac/internal/service/zkservice';
import { DevicesView } from './views/DevicesView';
import { UsersView } from './views/UsersView';
import { FacesView } from './views/FacesView';
import { LogsView } from './views/LogsView';
import { AuditView } from './views/AuditView';
import { ToastHost, ToastMessage } from './components/ToastHost';
import { ProgressModal } from './components/ProgressModal';

const INLINE_SINGLE_DEVICE_TASKS = new Set(['connect', 'probe', 'sync_time']);

type TabKey = 'devices' | 'users' | 'faces' | 'logs' | 'audit';

const emptyMatrix: UserMatrixResponse = {
  devices: [],
  rows: [],
  departments: [],
  totalUsers: 0,
  masterCount: 0,
  diffUsers: 0,
  pendingPushCount: 0,
  pendingPullCount: 0,
  resignedInDev: 0,
  lastUpdated: '',
  masterUpdatedAt: '',
  masterFilePath: '',
};

const emptyLogs: PullLogsResponse = {
  records: [],
  dailySummary: [],
  total: 0,
  addedNew: 0,
  archiveTotal: 0,
  summaries: [],
  pulledAt: '',
};

export default function App() {
  const [tab, setTab] = useState<TabKey>('devices');
  const [busy, setBusy] = useState(false);

  const [runtime, setRuntime] = useState<RuntimeHealth | null>(null);
  const [devices, setDevices] = useState<DeviceConfig[]>([]);
  const [statuses, setStatuses] = useState<Record<string, DeviceStatus | undefined>>({});
  const [matrix, setMatrix] = useState<UserMatrixResponse>(emptyMatrix);
  const [gallery, setGallery] = useState<FacePhotoItem[]>([]);
  const [logsData, setLogsData] = useState<PullLogsResponse>(emptyLogs);
  const [auditLogs, setAuditLogs] = useState<OperationLog[]>([]);

  const [progress, setProgress] = useState<ProgressEvent | null>(null);
  const [toasts, setToasts] = useState<ToastMessage[]>([]);
  const toastSeqRef = useRef(0);

  const notify = useCallback((type: 'info' | 'success' | 'error', text: string) => {
    const id = ++toastSeqRef.current;
    setToasts((prev) => [{ id, type, text }, ...prev].slice(0, 4));
  }, []);

  const dismissToast = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const refreshRuntime = useCallback(async () => {
    try {
      const h = await ZK.CheckRuntime();
      setRuntime(h);
    } catch (err: any) {
      setRuntime({
        ready: false,
        winePath: '',
        wineVersion: '',
        dataDir: '',
        error: String(err?.message || err),
      });
    }
  }, []);

  const reloadGallery = useCallback(async () => {
    try {
      const g = await ZK.GetFaceGallery();
      setGallery(g || []);
    } catch {
      // ignore initial empty gallery
    }
  }, []);

  const reloadAudit = useCallback(async () => {
    try {
      const list = await ZK.GetOperationLogs();
      setAuditLogs(list || []);
    } catch {
      // ignore
    }
  }, []);

  useEffect(() => {
    let mounted = true;
    (async () => {
      try {
        const [devs, cachedSt, cachedMatrix, cachedLogs, cachedAudit] = await Promise.all([
          ZK.GetDevices(),
          ZK.GetCachedStatuses(),
          ZK.GetUserMatrix(),
          ZK.GetLastLogs(),
          ZK.GetOperationLogs(),
        ]);
        if (!mounted) return;
        setDevices(devs || []);
        setStatuses(cachedSt || {});
        setMatrix(cachedMatrix || emptyMatrix);
        setLogsData(cachedLogs || emptyLogs);
        setAuditLogs(cachedAudit || []);
        await reloadGallery();
      } catch (err: any) {
        if (mounted) notify('error', `初始化加载失败：${err?.message || err}`);
      }
    })();

    refreshRuntime();

    const offProgress = Events.On('zk:progress', (ev: any) => {
      const data: ProgressEvent = ev?.data ?? ev;
      if (!data) return;
      if (data.done) {
        setProgress(null);
      } else if (INLINE_SINGLE_DEVICE_TASKS.has(data.task) && (data.total || 0) <= 1) {
        // Single-device connect / probe / sync_time renders inline spinner on the device button itself
        setProgress(null);
      } else {
        setProgress(data);
      }
    });

    const offReset = Events.On('zk:connections-reset', (ev: any) => {
      const data = ev?.data ?? ev;
      if (data?.statuses && typeof data.statuses === 'object') {
        setStatuses(data.statuses);
      } else {
        setStatuses((prev) => {
          const next: Record<string, DeviceStatus | undefined> = {};
          for (const [k, v] of Object.entries(prev)) {
            if (v) next[k] = { ...v, connected: false };
          }
          return next;
        });
      }
      notify('error', 'SDK 后台进程已重置，考勤机连接已断开，请重新连接');
    });

    return () => {
      mounted = false;
      if (typeof offProgress === 'function') offProgress();
      if (typeof offReset === 'function') offReset();
    };
  }, [notify, refreshRuntime, reloadGallery]);

  const switchTab = (next: TabKey) => {
    setTab(next);
    if (next === 'faces') {
      reloadGallery();
    } else if (next === 'audit') {
      reloadAudit();
    }
  };

  const tabMeta: Record<TabKey, { title: string; sub: string }> = {
    devices: {
      title: '考勤设备总览',
      sub: '手动连接考勤机、监测在线状态与设备时间偏差',
    },
    users: {
      title: '人员管理',
      sub: matrix.lastUpdated
        ? `以本地人员主库为基准 · 各考勤机独立名单，按需手动拉取与上传 · 最近扫描：${matrix.lastUpdated}`
        : '以本地人员主库为基准，各考勤机独立名单，按需手动选择考勤机拉取与上传',
    },
    faces: {
      title: '人脸照片库',
      sub: '从考勤机下载可见光原图，或上传本地照片跨多机注册',
    },
    logs: {
      title: '考勤数据拉取与汇总',
      sub: '本地累计库增量归档 · 每日首末打卡汇总 + 双表 Excel 导出',
    },
    audit: {
      title: '操作记录与备份',
      sub: '持久化记录所有同步、写入、校时、删除与拉取历史（最多保留 500 条）',
    },
  };

  const connectedCount = Object.values(statuses).filter((s) => s?.connected).length;

  return (
    <div className="app-shell">
      <aside className="sidebar" aria-label="主导航">
        <div className="titlebar-drag" />
        <div className="app-brand">
          <img
            src={appIconUrl}
            alt="ZKDesk"
            className="brand-icon"
            style={{ width: 30, height: 30, objectFit: 'cover', background: 'transparent' }}
          />
          <div>
            <div className="brand-title">ZKDesk</div>
            <div className="brand-sub">中控考勤管理台</div>
          </div>
        </div>

        <nav className="nav-list">
          <button
            type="button"
            className={`nav-btn ${tab === 'devices' ? 'active' : ''}`}
            onClick={() => switchTab('devices')}
          >
            <MonitorSmartphone size={16} />
            设备总览
            <span className="nav-badge" title="当前已连接设备数 / 启用设备总数">
              {connectedCount}/{devices.filter((d) => d.enabled).length}
            </span>
          </button>

          <button
            type="button"
            className={`nav-btn ${tab === 'users' ? 'active' : ''}`}
            onClick={() => switchTab('users')}
          >
            <Users size={16} />
            人员管理
            {matrix.diffUsers > 0 ? (
              <span className="nav-badge warn" title="存在未入本地库、已注册资料过期或离职残留的员工数">
                {matrix.diffUsers} 差异
              </span>
            ) : (
              <span className="nav-badge">{matrix.masterCount || matrix.totalUsers || 0}</span>
            )}
          </button>

          <button
            type="button"
            className={`nav-btn ${tab === 'faces' ? 'active' : ''}`}
            onClick={() => switchTab('faces')}
          >
            <Camera size={16} />
            人脸照片
            <span className="nav-badge">{gallery.length}</span>
          </button>

          <button
            type="button"
            className={`nav-btn ${tab === 'logs' ? 'active' : ''}`}
            onClick={() => switchTab('logs')}
          >
            <Clock size={16} />
            考勤拉取
            <span className="nav-badge" title="本地累计考勤库条数">
              {logsData.archiveTotal || logsData.total || 0}
            </span>
          </button>

          <button
            type="button"
            className={`nav-btn ${tab === 'audit' ? 'active' : ''}`}
            onClick={() => switchTab('audit')}
          >
            <ClipboardList size={16} />
            操作记录
            <span className="nav-badge">{auditLogs.length}</span>
          </button>
        </nav>

        <div className="sidebar-footer">
          <div className="runtime-pill" title={runtime?.error || runtime?.winePath || '正在检测 Wine 引擎'}>
            <span
              className={`status-dot ${
                runtime === null ? 'idle' : runtime.ready ? 'ok' : 'err'
              }`}
            />
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {runtime === null
                ? '正在预热 SDK 引擎...'
                : runtime.ready
                ? `SDK 就绪 (${runtime.wineVersion || 'Wine'})`
                : 'SDK 未就绪'}
            </span>
          </div>
        </div>
      </aside>

      <main className="main-area">
        <header className="topbar">
          <div className="topbar-left">
            <h1 className="topbar-title">{tabMeta[tab].title}</h1>
            <span className="topbar-sub">{tabMeta[tab].sub}</span>
          </div>
          <div className="topbar-actions">
            {busy && (
              <span className="badge badge-accent">
                <Loader2 size={12} className="spin" /> 正在与考勤机通讯...
              </span>
            )}
          </div>
        </header>

        <div className="content-body scroll-area">
          {tab === 'devices' && (
            <DevicesView
              devices={devices}
              statuses={statuses}
              runtime={runtime}
              busy={busy}
              onDevicesChange={setDevices}
              onStatusesChange={setStatuses}
              onRefreshRuntime={refreshRuntime}
              onAuditChanged={reloadAudit}
              onNotify={notify}
              setBusy={setBusy}
            />
          )}

          {tab === 'users' && (
            <UsersView
              devices={devices}
              statuses={statuses}
              matrix={matrix}
              busy={busy}
              onMatrixChange={(m) => {
                setMatrix(m);
                reloadGallery();
                ZK.GetLastLogs().then((l) => l && setLogsData(l)).catch(() => {});
              }}
              onAuditChanged={reloadAudit}
              onGoToDevices={() => setTab('devices')}
              onNotify={notify}
              setBusy={setBusy}
            />
          )}

          {tab === 'faces' && (
            <FacesView
              devices={devices}
              statuses={statuses}
              gallery={gallery}
              busy={busy}
              onReloadGallery={reloadGallery}
              onAuditChanged={reloadAudit}
              onGoToDevices={() => setTab('devices')}
              onNotify={notify}
              setBusy={setBusy}
            />
          )}

          {tab === 'logs' && (
            <LogsView
              devices={devices}
              statuses={statuses}
              logsData={logsData}
              busy={busy}
              onLogsChange={setLogsData}
              onAuditChanged={reloadAudit}
              onGoToDevices={() => setTab('devices')}
              onNotify={notify}
              setBusy={setBusy}
            />
          )}

          {tab === 'audit' && (
            <AuditView logs={auditLogs} onReload={reloadAudit} onNotify={notify} />
          )}
        </div>

        <ProgressModal progress={progress} />
        <ToastHost toasts={toasts} onDismiss={dismissToast} />
      </main>
    </div>
  );
}
