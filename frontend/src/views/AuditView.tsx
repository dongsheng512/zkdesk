import { useMemo, useState } from 'react';
import {
  CheckCircle2,
  ClipboardList,
  FolderOpen,
  RefreshCw,
  Search,
  Trash2,
  XCircle,
} from 'lucide-react';
import { OperationLog } from '../../bindings/zk_mac/internal/models';
import * as ZK from '../../bindings/zk_mac/internal/service/zkservice';
import { ModalDialog } from '../components/ModalDialog';

interface AuditViewProps {
  logs: OperationLog[];
  onReload: () => Promise<void>;
  onNotify: (type: 'info' | 'success' | 'error', text: string) => void;
}

export function AuditView({ logs, onReload, onNotify }: AuditViewProps) {
  const [query, setQuery] = useState('');
  const [actionFilter, setActionFilter] = useState('');
  const [onlyFailed, setOnlyFailed] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);

  const actionOptions = useMemo(() => {
    const map = new Map<string, string>();
    for (const item of logs) {
      if (item.action && !map.has(item.action)) {
        map.set(item.action, item.actionLabel || item.action);
      }
    }
    return Array.from(map.entries());
  }, [logs]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return logs.filter((l) => {
      if (actionFilter && l.action !== actionFilter) return false;
      if (onlyFailed && l.success) return false;
      if (q) {
        const hay = `${l.actionLabel} ${l.target} ${l.summary} ${l.detail || ''} ${l.timestamp}`.toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  }, [logs, query, actionFilter, onlyFailed]);

  const handleClear = async () => {
    try {
      await ZK.ClearOperationLogs();
      await onReload();
      setConfirmClear(false);
      onNotify('info', '已清空本地操作记录（备份文件保留不变）');
    } catch (err: any) {
      onNotify('error', `清空失败：${err?.message || err}`);
    }
  };

  return (
    <>
      <div className="toolbar-row" style={{ justifyContent: 'space-between' }}>
        <div className="toolbar-row">
          <button type="button" className="btn" onClick={onReload}>
            <RefreshCw size={14} /> 刷新记录
          </button>
          <button
            type="button"
            className="btn"
            onClick={() => ZK.OpenBackupsDirectory().catch((e) => onNotify('error', String(e)))}
          >
            <FolderOpen size={14} /> 打开自动备份目录
          </button>
          {logs.length > 0 && (
            <button
              type="button"
              className="btn btn-ghost"
              style={{ color: 'var(--danger)' }}
              onClick={() => setConfirmClear(true)}
            >
              <Trash2 size={14} /> 清空日志
            </button>
          )}
        </div>

        <div className="toolbar-row">
          <select
            className="select"
            aria-label="按操作类型筛选"
            value={actionFilter}
            onChange={(e) => setActionFilter(e.target.value)}
          >
            <option value="">全部操作类型</option>
            {actionOptions.map(([val, lbl]) => (
              <option key={val} value={val}>
                {lbl}
              </option>
            ))}
          </select>
          <label className="check-item">
            <input
              type="checkbox"
              checked={onlyFailed}
              onChange={(e) => setOnlyFailed(e.target.checked)}
            />
            仅看失败
          </label>
          <div className="search-box">
            <Search size={14} />
            <input
              className="input"
              placeholder="搜索工号 / 设备 / 详情..."
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </div>
        </div>
      </div>

      <div className="table-card">
        {filtered.length === 0 ? (
          <div className="empty-state">
            <ClipboardList size={30} strokeWidth={1.5} />
            <div>{logs.length === 0 ? '暂无操作记录（所有写入、同步、校时、考勤拉取会自动记录于此）' : '无匹配的操作记录'}</div>
          </div>
        ) : (
          <div tabIndex={0} role="region" aria-label="历史操作审计日志" className="table-wrapper">
            <table className="data-table">
              <thead>
                <tr>
                  <th style={{ width: 165 }}>时间</th>
                  <th style={{ width: 110 }}>操作类型</th>
                  <th style={{ width: 170 }}>对象</th>
                  <th>结果摘要与明细</th>
                  <th style={{ width: 85 }}>状态</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((item) => (
                  <tr key={item.id}>
                    <td className="mono">{item.timestamp}</td>
                    <td>
                      <span className="badge badge-accent">{item.actionLabel || item.action}</span>
                    </td>
                    <td style={{ fontWeight: 550 }}>{item.target}</td>
                    <td>
                      <div>{item.summary}</div>
                      {item.detail && (
                        <div
                          style={{
                            fontSize: 11.5,
                            color: item.success ? 'var(--text-muted)' : 'var(--danger)',
                            marginTop: 2,
                          }}
                        >
                          {item.detail}
                        </div>
                      )}
                      {item.backupPath && (
                        <div className="mono" style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>
                          备份：{item.backupPath.split('/').pop()}
                        </div>
                      )}
                    </td>
                    <td>
                      {item.success ? (
                        <span className="badge badge-ok">
                          <CheckCircle2 size={11} /> 成功
                        </span>
                      ) : (
                        <span className="badge badge-danger">
                          <XCircle size={11} /> 失败
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <ModalDialog
        open={confirmClear}
        title="确认清空操作日志"
        onClose={() => setConfirmClear(false)}
        width={420}
        footer={
          <>
            <button type="button" className="btn" autoFocus onClick={() => setConfirmClear(false)}>
              取消
            </button>
            <button type="button" className="btn btn-danger" onClick={handleClear}>
              确认清空
            </button>
          </>
        }
      >
        <p style={{ margin: 0 }}>
          仅清空界面展示的操作日志记录，不会删除 `backups/` 目录下已保存的自动备份文件。
        </p>
      </ModalDialog>
    </>
  );
}
