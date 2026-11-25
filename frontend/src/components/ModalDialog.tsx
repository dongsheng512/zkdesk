import React, { useEffect, useId, useRef } from 'react';
import { X } from 'lucide-react';

interface ModalDialogProps {
  open: boolean;
  title: string;
  onClose: () => void;
  lightDismiss?: boolean;
  children: React.ReactNode;
  footer?: React.ReactNode;
  width?: number;
}

/**
 * Native <dialog> modal following modern-web-guidance:
 * - Uses showModal() for built-in top layer, inert background, focus containment, and Esc handling.
 * - Never adds redundant role="dialog" or aria-modal.
 * - Resets dialog.returnValue on close to avoid stale values.
 * - Supports safe pointer-bounds light-dismiss fallback when closedby is unsupported.
 */
export function ModalDialog({
  open,
  title,
  onClose,
  lightDismiss = false,
  children,
  footer,
  width,
}: ModalDialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const pressedOutsideRef = useRef(false);

  useEffect(() => {
    const dlg = ref.current;
    if (!dlg) return;
    if (open && !dlg.open) {
      dlg.showModal();
    } else if (!open && dlg.open) {
      dlg.close();
    }
  }, [open]);

  const isOutside = (e: React.PointerEvent<HTMLDialogElement>) => {
    const dlg = ref.current;
    if (!dlg) return false;
    const r = dlg.getBoundingClientRect();
    return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
  };

  return (
    <dialog
      ref={ref}
      className="app-dialog"
      aria-labelledby={titleId}
      style={width ? { width: `min(${width}px, 94vw)` } : undefined}
      {...({ closedby: lightDismiss ? 'any' : 'closerequest' } as Record<string, string>)}
      onClose={() => {
        if (ref.current) {
          ref.current.returnValue = '';
        }
        onClose();
      }}
      onPointerDown={(e) => {
        if (lightDismiss && !('closedBy' in HTMLDialogElement.prototype)) {
          pressedOutsideRef.current = isOutside(e);
        }
      }}
      onPointerUp={(e) => {
        if (lightDismiss && !('closedBy' in HTMLDialogElement.prototype)) {
          if (pressedOutsideRef.current && isOutside(e)) {
            ref.current?.close();
          }
          pressedOutsideRef.current = false;
        }
      }}
    >
      <div className="dialog-header">
        <h2 id={titleId} className="dialog-title">
          {title}
        </h2>
        <button
          type="button"
          className="btn btn-ghost btn-sm"
          aria-label="关闭窗口"
          onClick={() => ref.current?.close()}
        >
          <X size={15} />
        </button>
      </div>
      <div className="dialog-body">{children}</div>
      {footer && <div className="dialog-footer">{footer}</div>}
    </dialog>
  );
}
