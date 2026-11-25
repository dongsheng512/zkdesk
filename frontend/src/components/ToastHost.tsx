import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { AlertCircle, CheckCircle2, Info, X } from 'lucide-react';

export interface ToastMessage {
  id: number;
  type: 'info' | 'success' | 'error';
  text: string;
}

interface ToastHostProps {
  toasts: ToastMessage[];
  onDismiss: (id: number) => void;
}

const AUTO_DISMISS_MS: Record<ToastMessage['type'], number> = {
  info: 2000,
  success: 2200,
  error: 4500,
};

function hasOpenModalDialog(): boolean {
  return Boolean(document.querySelector('dialog[open]'));
}

function announceToast(text: string, setFallbackText: (v: string) => void) {
  const doc = document as Document & { ariaNotify?: (msg: string) => void };
  if (typeof doc.ariaNotify === 'function') {
    doc.ariaNotify(text);
  } else {
    setFallbackText('');
    requestAnimationFrame(() => setFallbackText(text));
  }
}

interface ToastItemProps {
  toast: ToastMessage;
  offsetTop: number;
  onMeasure: (id: number, height: number) => void;
  onDismiss: (id: number, wasFocused: boolean) => void;
  registerCloseButton: (id: number, el: HTMLButtonElement | null) => void;
  onFocusEnterToast: (previousActive: HTMLElement | null) => void;
}

function ToastItem({
  toast,
  offsetTop,
  onMeasure,
  onDismiss,
  registerCloseButton,
  onFocusEnterToast,
}: ToastItemProps) {
  const domId = `zk-toast-${toast.id}`;
  const elRef = useRef<HTMLDivElement | null>(null);
  const closeBtnRef = useRef<HTMLButtonElement | null>(null);
  const timerRef = useRef<number | null>(null);
  const hoveredRef = useRef(false);
  const focusedRef = useRef(false);
  const dismissedRef = useRef(false);

  const supportsPopover =
    typeof HTMLElement !== 'undefined' &&
    Object.prototype.hasOwnProperty.call(HTMLElement.prototype, 'popover');

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      window.clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  const hideNow = useCallback(() => {
    const el = elRef.current as (HTMLDivElement & { hidePopover?: () => void }) | null;
    if (!el || dismissedRef.current) return;
    if (supportsPopover && typeof el.hidePopover === 'function') {
      try {
        if (el.matches(':popover-open')) {
          el.hidePopover();
          return;
        }
      } catch {
        // Fallback below
      }
    }
    dismissedRef.current = true;
    onDismiss(toast.id, el.contains(document.activeElement));
  }, [onDismiss, supportsPopover, toast.id]);

  const scheduleAutoDismiss = useCallback(() => {
    clearTimer();
    const duration = AUTO_DISMISS_MS[toast.type];
    timerRef.current = window.setTimeout(() => {
      const el = elRef.current;
      if (hoveredRef.current || focusedRef.current || (el && el.contains(document.activeElement))) {
        return;
      }
      hideNow();
    }, duration);
  }, [clearTimer, hideNow, toast.type]);

  // Promote to Top Layer via showPopover() without stealing focus
  useLayoutEffect(() => {
    const el = elRef.current as (HTMLDivElement & { showPopover?: () => void }) | null;
    if (!el) return;

    if (supportsPopover && typeof el.showPopover === 'function') {
      try {
        if (!el.matches(':popover-open')) {
          el.showPopover();
        }
      } catch {
        el.classList.add(':popover-open');
      }
    } else {
      el.classList.add(':popover-open');
    }

    const measure = () => {
      onMeasure(toast.id, el.getBoundingClientRect().height);
    };
    measure();

    const ro = new ResizeObserver(measure);
    ro.observe(el);

    return () => {
      ro.disconnect();
      clearTimer();
    };
  }, [clearTimer, onMeasure, supportsPopover, toast.id]);

  useEffect(() => {
    scheduleAutoDismiss();
    return clearTimer;
  }, [clearTimer, scheduleAutoDismiss]);

  // Listen for native popover toggle (triggered by popoverTargetAction="hide" button or hidePopover)
  useEffect(() => {
    const el = elRef.current;
    if (!el) return;
    const onToggle = (ev: Event) => {
      const te = ev as Event & { newState?: string };
      if (te.newState === 'closed' && !dismissedRef.current) {
        dismissedRef.current = true;
        clearTimer();
        const wasFocused = focusedRef.current || el.contains(document.activeElement);
        // Allow brief exit transition before removing from DOM
        window.setTimeout(() => {
          onDismiss(toast.id, wasFocused);
        }, 160);
      }
    };
    el.addEventListener('toggle', onToggle);
    return () => el.removeEventListener('toggle', onToggle);
  }, [clearTimer, onDismiss, toast.id]);

  useEffect(() => {
    registerCloseButton(toast.id, closeBtnRef.current);
    return () => registerCloseButton(toast.id, null);
  }, [registerCloseButton, toast.id]);

  const Icon =
    toast.type === 'success' ? CheckCircle2 : toast.type === 'error' ? AlertCircle : Info;

  return (
    <div
      id={domId}
      ref={elRef}
      {...({ popover: 'manual' } as Record<string, string>)}
      className={`toast-popover toast-${toast.type}`}
      style={{ '--toast-offset': `${offsetTop}px` } as React.CSSProperties}
      onMouseEnter={() => {
        hoveredRef.current = true;
        clearTimer();
      }}
      onMouseLeave={() => {
        hoveredRef.current = false;
        if (!focusedRef.current) {
          scheduleAutoDismiss();
        }
      }}
      onFocusCapture={(e) => {
        if (!focusedRef.current) {
          const related = e.relatedTarget as HTMLElement | null;
          if (related && !elRef.current?.contains(related)) {
            onFocusEnterToast(related);
          }
        }
        focusedRef.current = true;
        clearTimer();
      }}
      onBlurCapture={(e) => {
        const next = e.relatedTarget as Node | null;
        if (!next || !elRef.current?.contains(next)) {
          focusedRef.current = false;
          if (!hoveredRef.current) {
            scheduleAutoDismiss();
          }
        }
      }}
    >
      <span className="toast-icon" aria-hidden="true">
        <Icon size={16} />
      </span>
      <span className="toast-text">{toast.text}</span>
      <button
        ref={closeBtnRef}
        type="button"
        className="toast-close-btn"
        aria-label="关闭通知"
        {...({ popoverTarget: domId, popoverTargetAction: 'hide' } as Record<string, string>)}
        onClick={() => {
          if (!supportsPopover) {
            hideNow();
          }
        }}
      >
        <X size={14} />
      </button>
    </div>
  );
}

export function ToastHost({ toasts, onDismiss }: ToastHostProps) {
  const [heights, setHeights] = useState<Record<number, number>>({});
  const [modalOpen, setModalOpen] = useState<boolean>(() => hasOpenModalDialog());
  const [liveFallback, setLiveFallback] = useState('');

  const announcedIdsRef = useRef<Set<number>>(new Set());
  const closeBtnMapRef = useRef<Map<number, HTMLButtonElement>>(new Map());
  const priorFocusRef = useRef<HTMLElement | null>(null);

  // Track whether a modal <dialog open> is currently active
  useEffect(() => {
    const syncModal = () => setModalOpen(hasOpenModalDialog());
    syncModal();
    const mo = new MutationObserver(syncModal);
    mo.observe(document.body, {
      subtree: true,
      attributes: true,
      attributeFilter: ['open'],
      childList: true,
    });
    return () => mo.disconnect();
  }, []);

  // Suppress non-error background toasts while a modal dialog is open; surface once dismissed
  const visibleToasts = toasts.filter((t) => !modalOpen || t.type === 'error');

  // Announce newly surfaced toasts via ariaNotify() (or hidden ARIA live fallback)
  useEffect(() => {
    for (const t of visibleToasts) {
      if (!announcedIdsRef.current.has(t.id)) {
        announcedIdsRef.current.add(t.id);
        announceToast(t.text, setLiveFallback);
      }
    }
  }, [visibleToasts]);

  const handleMeasure = useCallback((id: number, h: number) => {
    setHeights((prev) => (prev[id] === h ? prev : { ...prev, [id]: h }));
  }, []);

  const registerCloseButton = useCallback((id: number, el: HTMLButtonElement | null) => {
    if (el) {
      closeBtnMapRef.current.set(id, el);
    } else {
      closeBtnMapRef.current.delete(id);
    }
  }, []);

  const handleFocusEnterToast = useCallback((prev: HTMLElement | null) => {
    if (prev && document.contains(prev)) {
      priorFocusRef.current = prev;
    }
  }, []);

  const handleDismiss = useCallback(
    (id: number, wasFocused: boolean) => {
      if (wasFocused) {
        const remaining = visibleToasts.filter((t) => t.id !== id);
        if (remaining.length > 0) {
          const nextBtn = closeBtnMapRef.current.get(remaining[0].id);
          nextBtn?.focus();
        } else if (priorFocusRef.current && document.contains(priorFocusRef.current)) {
          priorFocusRef.current.focus();
          priorFocusRef.current = null;
        }
      }
      setHeights((prev) => {
        if (!(id in prev)) return prev;
        const copy = { ...prev };
        delete copy[id];
        return copy;
      });
      onDismiss(id);
    },
    [onDismiss, visibleToasts]
  );

  // Stack toasts vertically from top-right (newest at top, 10px gap)
  let accOffset = 0;
  const itemsWithOffset = visibleToasts.map((t) => {
    const offset = accOffset;
    accOffset += (heights[t.id] || 48) + 10;
    return { toast: t, offset };
  });

  return (
    <>
      <div role="region" aria-label="应用消息通知" className="toast-region">
        {itemsWithOffset.map(({ toast, offset }) => (
          <ToastItem
            key={toast.id}
            toast={toast}
            offsetTop={offset}
            onMeasure={handleMeasure}
            onDismiss={handleDismiss}
            registerCloseButton={registerCloseButton}
            onFocusEnterToast={handleFocusEnterToast}
          />
        ))}
      </div>

      {/* Fallback live region only used when native document.ariaNotify() is unavailable */}
      <p className="sr-only">状态消息通知</p>
      <div className="sr-only" aria-live="polite">
        {liveFallback}
      </div>
    </>
  );
}
