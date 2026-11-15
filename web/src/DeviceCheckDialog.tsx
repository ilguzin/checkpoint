import { tr, useLocale, locale, systemError } from './i18n';
import { useCallback, useEffect, useRef, useState } from 'react';
import { PreJoin } from '@livekit/components-react';

type DeviceChoices = { username: string; audioDeviceId: string; videoDeviceId: string };
type Props = {
  name: string;
  devices: { audioDeviceId: string; videoDeviceId: string };
  onApply: (choices: DeviceChoices) => void;
  onClose: () => void;
};

/** Mount only while open: unmounting PreJoin releases every local preview track. */
export function DeviceCheckDialog({ name, devices, onApply, onClose }: Props) {
  useLocale();
  const dialog = useRef<HTMLDialogElement>(null);
  const startedOnBackdrop = useRef(false);
  const [error, setError] = useState('');
  // PreJoin's preview-track effect depends on this callback; keep it stable.
  const onDeviceError = useCallback((cause: Error) => setError(cause.message), []);
  useEffect(() => {
    const element = dialog.current!;
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    element.showModal();
    return () => {
      element.close();
      document.body.style.overflow = previousOverflow;
      if (trigger?.isConnected) trigger.focus({ preventScroll: true });
    };
  }, []);
  function isBackdrop(event: { clientX: number; clientY: number }) {
    const rect = dialog.current!.getBoundingClientRect();
    return event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom;
  }
  return <dialog ref={dialog} className="device-check-dialog" aria-labelledby="device-check-title" aria-describedby="device-check-description"
    onKeyDown={event => {
      if (event.key !== 'Tab' || event.altKey || event.ctrlKey || event.metaKey) return;
      // Native modal dialogs make the page inert, but Tab can still move to browser chrome.
      // Keep keyboard navigation cycling through the currently visible device controls.
      const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button, input, select, textarea, a[href], [tabindex]'))
        .filter(element => element.tabIndex >= 0 && !element.matches(':disabled') && element.getClientRects().length > 0 && getComputedStyle(element).visibility !== 'hidden');
      const first = controls[0], last = controls[controls.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
    }}
    onCancel={event => { event.preventDefault(); onClose(); }}
    onClose={onClose}
    onPointerDown={event => { startedOnBackdrop.current = isBackdrop(event); }}
    onClick={event => { if (startedOnBackdrop.current && isBackdrop(event)) onClose(); startedOnBackdrop.current = false; }}>
    <div className="device-dialog-heading">
      <div><div className="eyebrow">{tr("BEFORE YOU JOIN")}</div><h2 id="device-check-title">{tr("Check your devices")}</h2></div>
      <button type="button" className="quiet device-dialog-close" aria-label={tr("Close device check")} title={tr("Close device check")} onClick={onClose} autoFocus>
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18" /></svg>
      </button>
    </div>
    <div className="device-dialog-body">
      <p id="device-check-description">{tr("Turn on your microphone or camera to try them here. This preview is only visible to you.")}</p>
      {error && <p role="alert">{systemError(error)}</p>}
      <div className="device-preview" data-lk-theme="default">
        <label className="device-preview-name-label" htmlFor="username">{tr("Your preview name")}</label>
        <PreJoin defaults={{ username: name || tr("Guest"), ...devices, audioEnabled: false, videoEnabled: false }}
          micLabel={tr("Microphone")} camLabel={tr("Camera")} persistUserChoices={false} userLabel={tr("Your name")} joinLabel={tr("Use these devices")}
          onValidate={choices => choices.username.trim().length > 0 && choices.username.trim().length <= 80}
          onSubmit={choices => onApply({ username: choices.username.trim(), audioDeviceId: choices.audioDeviceId, videoDeviceId: choices.videoDeviceId })}
          onError={onDeviceError} />
      </div>
      <p className="hint device-preview-note">{tr("Your microphone and camera will still be off when you join the meeting.")}</p>
    </div>
  </dialog>;
}
