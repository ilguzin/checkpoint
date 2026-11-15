import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useMediaDeviceSelect, useRoomContext } from '@livekit/components-react';
import { CallIcon } from './CallIcon';
import { errorMessage } from './api';
import { tr, useLocale } from './i18n';
import './device-controls.css';

export function DeviceControl({ kind, label, selected, onSelect, onError, open, onOpen, onClose, children }: {
  kind: MediaDeviceKind; label: string; selected: string; onSelect: (id: string) => void;
  onError: (message: string) => void; open: boolean; onOpen: () => void; onClose: () => void; children: ReactNode;
}) {
  useLocale();
  const room = useRoomContext();
  const handleError = useCallback((error: Error) => onError(errorMessage(error)), [onError]);
  const { devices, activeDeviceId } = useMediaDeviceSelect({ kind, requestPermissions: false, onError: handleError });
  const trigger = useRef<HTMLButtonElement>(null), menu = useRef<HTMLDivElement>(null);
  const id = useId();
  const [busy, setBusy] = useState(false);
  const [position, setPosition] = useState({ left: 8, bottom: 80 });
  const supported = kind !== 'audiooutput' || typeof HTMLMediaElement.prototype.setSinkId === 'function';
  const choices = [{ deviceId: 'default', label: tr('System default') }, ...devices.filter(device => device.deviceId && device.deviceId !== 'default')];
  useEffect(() => {
    // Preserve the system-default preference, but follow an SDK fallback when
    // an explicitly selected physical device is unplugged.
    if (!busy && selected !== 'default' && activeDeviceId !== selected && devices.some(device => device.deviceId === activeDeviceId)) onSelect(activeDeviceId);
  }, [activeDeviceId, selected, devices, busy, onSelect]);
  function close(focus = true) { onClose(); if (focus) trigger.current?.focus({ preventScroll: true }); }
  useLayoutEffect(() => {
    if (!open) return;
    function place() {
      const rect = trigger.current!.closest('.device-control')!.getBoundingClientRect();
      const width = Math.min(300, window.innerWidth - 16);
      setPosition({ left: Math.max(8, Math.min(rect.left, window.innerWidth - width - 8)), bottom: window.innerHeight - rect.top + 8 });
    }
    place(); window.addEventListener('resize', place);
    return () => window.removeEventListener('resize', place);
  }, [open]);
  useEffect(() => {
    if (!open) return;
    (menu.current?.querySelector<HTMLElement>('[aria-checked="true"]:not(:disabled)') || menu.current)?.focus();
    const outside = (event: PointerEvent) => {
      if (!menu.current?.contains(event.target as Node) && !trigger.current?.contains(event.target as Node)) onClose();
    };
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [open]);
  async function choose(value: string) {
    if (busy) return;
    setBusy(true);
    try {
      // Cameras usually have no literal "default" ID. An ideal constraint lets
      // the browser choose its default without requesting an unavailable ID.
      const changed = await room.switchActiveDevice(kind, value, value !== 'default');
      if (!changed) throw new Error('Device unavailable. Check browser permissions and device settings.');
      onSelect(value); close();
    } catch (error) { onError(errorMessage(error)); }
    finally { setBusy(false); }
  }
  return <div className="device-control">
    {children}
    <button ref={trigger} type="button" className="device-menu-trigger" aria-label={tr('Choose {{device}}', { device: label })} title={tr('Choose {{device}}', { device: label })} aria-haspopup="menu" aria-expanded={open} aria-controls={open ? id : undefined} onClick={() => open ? close() : onOpen()} onKeyDown={event => { if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); onOpen(); } }}>
      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="m4 6 4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" /></svg>
    </button>
    {open && createPortal(<div ref={menu} id={id} className="device-menu" role="menu" aria-label={tr('Choose {{device}}', { device: label })} aria-busy={busy} tabIndex={-1} style={{ left: position.left, bottom: position.bottom }} onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget as Node) && event.relatedTarget !== trigger.current) onClose(); }} onKeyDown={event => {
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(); return; }
      if (event.key === 'Tab') { event.preventDefault(); close(); return; }
      const items = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]:not(:disabled)'));
      const current = items.indexOf(document.activeElement as HTMLButtonElement);
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : event.key === 'ArrowDown' ? (current + 1) % items.length : event.key === 'ArrowUp' ? (current - 1 + items.length) % items.length : -1;
      if (next >= 0) { event.preventDefault(); items[next]?.focus(); }
    }}>
      <div className="device-menu-title">{label}</div>
      {choices.map((device, index) => <button type="button" role="menuitemradio" aria-checked={selected === device.deviceId} key={device.deviceId} disabled={busy || !supported} onClick={() => void choose(device.deviceId)}><span>{device.label || tr('Unnamed device {{number}}', { number: index })}</span>{selected === device.deviceId && <CallIcon name="check" />}</button>)}
      {!supported ? <p>{tr('Choose your audio output in the device sound settings.')}</p> : <p>{tr('Turn on a device to see its name if your browser hides it.')}</p>}
    </div>, document.body)}
  </div>;
}
