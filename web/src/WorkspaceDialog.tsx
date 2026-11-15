import { tr, useLocale, locale, systemError } from './i18n';
import { useEffect, useId, useRef, type ReactNode } from 'react';
import { CallIcon } from './CallIcon';

export function WorkspaceDialog({ title, description, busy = false, onClose, children }: {
  title: string; description?: string; busy?: boolean; onClose: () => void; children: ReactNode;
}) {
  useLocale();
  const dialog = useRef<HTMLDialogElement>(null);
  const backdropDown = useRef(false);
  const titleID = useId(), descriptionID = useId();
  useEffect(() => {
    const element = dialog.current!;
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    element.showModal();
    element.querySelector<HTMLElement>('[data-autofocus]')?.focus();
    return () => {
      element.close(); document.body.style.overflow = previousOverflow;
      if (trigger?.isConnected) trigger.focus({ preventScroll: true });
    };
  }, []);
  function outside(event: { clientX: number; clientY: number }) {
    const rect = dialog.current!.getBoundingClientRect();
    return event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom;
  }
  return <dialog ref={dialog} className="workspace-dialog" aria-labelledby={titleID} aria-describedby={description ? descriptionID : undefined}
    onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}
    onPointerDown={event => { backdropDown.current = outside(event); }}
    onClick={event => { if (backdropDown.current && outside(event) && !busy) onClose(); backdropDown.current = false; }}
    onKeyDown={event => {
      if (event.key !== 'Tab') return;
      const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button, input, select, textarea, a[href], [tabindex]'))
        .filter(element => element.tabIndex >= 0 && !element.matches(':disabled') && element.getClientRects().length > 0);
      const first = controls[0], last = controls[controls.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
    }}>
    <div className="workspace-dialog-heading"><h2 id={titleID}>{title}</h2><button type="button" className="quiet workspace-dialog-close" aria-label={tr("Close dialog")} title={tr("Close dialog")} disabled={busy} onClick={onClose}><CallIcon name="close" /></button></div>
    <div className="workspace-dialog-body">{description && <p id={descriptionID}>{description}</p>}{children}</div>
  </dialog>;
}
