import { useEffect, useId, useRef } from 'react';
import { ProgressEvent } from '../../bindings/zk_mac/internal/models';

interface ProgressModalProps {
  progress: ProgressEvent | null;
}

const TASK_TITLES: Record<string, string> = {
  connect: '正在连接考勤机',
  probe: '正在刷新设备状态',
  sync_time: '正在同步设备时间',
  users: '正在扫描设备人员名单',
  pull_master: '正在拉取人员到本地库',
  push_master: '正在上传人员到考勤机',
  upsert: '正在写入员工信息',
  delete: '正在从考勤机移除人员',
  clear_bio: '正在清理生物特征',
  sync: '正在跨设备同步人员',
  faces: '正在下载人脸照片',
  face_batch: '正在批量上传人脸',
  face_upload: '正在上传人脸照片',
  face_delete: '正在删除设备人脸',
  logs: '正在拉取考勤打卡数据',
};

/**
 * Centered translucent loading HUD using native <dialog> (showModal).
 * - Only renders a determinate progress bar when total > 1 to avoid fake 100% bars on single-step RPCs.
 * - Prevents Esc dismissal during active hardware I/O.
 */
export function ProgressModal({ progress }: ProgressModalProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const open = Boolean(progress);

  useEffect(() => {
    const dlg = ref.current;
    if (!dlg) return;
    if (open && !dlg.open) {
      dlg.showModal();
    } else if (!open && dlg.open) {
      dlg.close();
    }
  }, [open]);

  const title = progress ? TASK_TITLES[progress.task] || '正在与考勤机通讯' : '正在处理';
  const showBar = Boolean(progress && progress.total > 1);
  const pct =
    progress && progress.total > 1
      ? Math.min(100, Math.max(4, Math.round((progress.current / progress.total) * 100)))
      : 0;

  return (
    <dialog
      ref={ref}
      className="progress-hud-dialog"
      aria-labelledby={titleId}
      aria-busy="true"
      onCancel={(e) => e.preventDefault()}
    >
      <div className="progress-hud-spinner" aria-hidden="true">
        <span className="progress-hud-ring" />
      </div>
      <div id={titleId} className="progress-hud-title">
        {title}
      </div>
      <div className="progress-hud-msg">{progress?.message || '请稍候...'}</div>
      {showBar && progress && (
        <div className="progress-hud-bar-wrap">
          <div className="progress-hud-bar-track" aria-hidden="true">
            <div className="progress-hud-bar-fill" style={{ width: `${pct}%` }} />
          </div>
          <span className="progress-hud-step mono">
            {progress.current} / {progress.total}
          </span>
        </div>
      )}
    </dialog>
  );
}
