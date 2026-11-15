import { t, tr, useLocale, locale, systemError } from './i18n';
import { createPortal } from 'react-dom';
import { useEffect, useRef, useState } from 'react';
import { api, errorMessage, type Session } from './api';
import { WorkspaceDialog } from './WorkspaceDialog';
import './recordings.css';
import { CallIcon } from './CallIcon';

type Segment = { id: string; speaker: string; source: string; startMs: number; endMs: number; text: string; language: string };
type Revision = { id: string; markdown: string; reviewed: boolean; created: number; author: string };
export type Recording = { id: string; room: string; occurrence: string; state: string; processing: string; minutesRequested: boolean; started: number; ended: number; deadline: number; window: number; pendingExtension?: boolean; warningSeconds: number; stopReason: string; partial: boolean; mixBytes: number; transcript?: Segment[]; revisions?: Revision[] };
type State = { externalProcessing: boolean; enabled: boolean; ready: boolean; minutesReady: boolean; run: Recording | null; serverTime: number };
type History = { recordings: Recording[]; canManage: boolean; canDelete: boolean };
const processingLabel: Record<string, string> = { not_requested: 'Recording only', queued: 'Minutes queued / waiting to retry', transcribing: 'Transcribing recording…', generating: 'Generating minutes…', cancelling: 'Cancelling minutes generation…', cancelled: 'Minutes generation cancelled', ready: 'Minutes ready', failed: 'Minutes generation failed', no_speech: 'No speech detected' };
const activeProcessing = ['queued', 'transcribing', 'generating'];
const runPath = (room: string, id: string) => `/api/rooms/${encodeURIComponent(room)}/recordings/${encodeURIComponent(id)}`;
const timecode = (ms: number) => `${Math.floor(ms / 60000).toString().padStart(2, '0')}:${Math.floor(ms / 1000 % 60).toString().padStart(2, '0')}`;

export function RecordingControls({ session }: { session: Session }) {
  useLocale();
  const [state, setState] = useState<State | null>(null);
  const [choice, setChoice] = useState(false);
  const [details, setDetails] = useState(false);
  const [history, setHistory] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const key = useRef('');
  const popover = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  async function refresh() { setState(await api<State>('/api/recording', session.session)); }
  useEffect(() => { let active = true; const poll = () => api<State>('/api/recording', session.session).then(value => { if (active) setState(value); }).catch(() => { if (active) setState(null); }); void poll(); const timer = setInterval(poll, 2000); return () => { active = false; clearInterval(timer); }; }, [session.session]);
  useEffect(() => {
    if (!choice) return;
    const close = (event: KeyboardEvent) => { if (event.key === 'Escape') { setChoice(false); trigger.current?.focus(); } };
    const outside = (event: PointerEvent) => { if (event.target instanceof Node && !popover.current?.contains(event.target) && !trigger.current?.contains(event.target)) setChoice(false); };
    window.addEventListener('keydown', close); window.addEventListener('pointerdown', outside);
    popover.current?.querySelector<HTMLButtonElement>('button')?.focus();
    return () => { window.removeEventListener('keydown', close); window.removeEventListener('pointerdown', outside); };
  }, [choice]);
  async function act(action: () => Promise<unknown>) { setBusy(true); setError(''); try { await action(); await refresh(); } catch (e) { setError(errorMessage(e)); } finally { setBusy(false); } }
  const run = state?.run;
  const recording = run?.state === 'recording';
  const label = recording ? (run.minutesRequested ? tr("Recording · Minutes will be generated") : tr("Recording only")) : run?.state === 'starting' ? tr("Starting recording…") : run?.state === 'finalizing' ? tr("Saving recording…") : tr("Recording is off");
  const startReason = !state ? tr("Recording status unavailable") : !state.enabled ? tr("Recording is disabled on this server.") : !state.ready ? tr("Recorder unavailable") : tr("Start recording");
  const remaining = run ? Math.max(0, run.deadline - (state?.serverTime || 0)) : 0;
  const warning = recording && remaining <= run.warningSeconds;
  return <div className="recording-controls">
    <button className={`dock-button recording-indicator ${recording ? 'is-recording' : ''}`} title={`${tr("Room history")} · ${state ? label : tr("Recording status unavailable")}`} aria-label={tr("Room history")} aria-description={state ? label : tr("Recording status unavailable")} aria-haspopup="dialog" aria-expanded={history} onMouseEnter={() => setDetails(true)} onMouseLeave={() => setDetails(false)} onFocus={() => setDetails(true)} onBlur={() => setDetails(false)} onClick={() => { setDetails(false); setChoice(false); setHistory(true); }} onKeyDown={event=>{if(event.key==='Escape')setDetails(false);}}>
      <span className="recording-history-icon" aria-hidden="true"><CallIcon name="history" />{recording && <span className="recording-dot" />}</span><span className="recording-label">{!state ? '…' : recording ? tr("Recording") : run?.state === 'starting' ? tr("Starting…") : run ? tr("Saving…") : tr("History")}</span>
    </button>
    {details && createPortal(<div className="recording-tooltip" role="tooltip">{state ? label : tr("Recording status unavailable")}</div>, document.body)}
    {session.moderator && <>
      {run ? <button className="dock-button recording-action recording-stop" disabled={busy || run.state === 'finalizing'} title={tr("Stop recording")} aria-label={tr("Stop recording")} aria-pressed={recording} onClick={() => act(() => api(`${runPath(session.room, run.id)}/stop`, session.session, {}))}><svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><circle cx="12" cy="12" r="6" fill="currentColor" /></svg><span className="recording-action-label">{tr("Stop recording")}</span></button> : <button ref={trigger} className="dock-button recording-action recording-start" title={startReason} aria-label={tr("Start recording")} aria-pressed={false} aria-expanded={choice} disabled={busy || !state?.enabled || !state.ready} onClick={() => { if (!choice) key.current = crypto.randomUUID(); setChoice(value => !value); }}><svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" strokeWidth="1.7" /><circle cx="12" cy="12" r="4" fill="currentColor" /></svg><span className="recording-action-label">{tr("Start recording")}</span></button>}
      {choice && createPortal(<div ref={popover} className="recording-popover" role="dialog" aria-label={tr("Recording options")}>
        <strong>{tr("Create minutes when recording ends?")}</strong><p>{state?.externalProcessing ? tr("The configured processing uses external services for audio or transcript.") : tr("Audio and minutes are processed locally on this server.")}</p><p>{tr("Everyone with room access can view saved recordings and minutes.")}</p>
        <button className="secondary" disabled={busy} onClick={() => setChoice(false)}>{tr("Cancel")}</button>
        <button disabled={busy || !state?.minutesReady} onClick={() => act(async () => { await api('/api/recording/start', session.session, { key: key.current, minutes: true }); setChoice(false); })}>{tr("Yes, record & create minutes")}</button>
        {!state?.minutesReady && <p>{tr("Minutes generation is unavailable on this server. You can still save a recording.")}</p>}
        <button className="secondary" disabled={busy} onClick={() => act(async () => { await api('/api/recording/start', session.session, { key: key.current, minutes: false }); setChoice(false); })}>{tr("No, record only")}</button>
      </div>, document.body)}
    </>}
    {warning && session.moderator && createPortal(<div className="recording-warning" role="alert"><span>{tr("Continue recording? Stops in {{time}}.", {time: timecode(remaining * 1000)})}</span><button disabled={busy || run.pendingExtension} onClick={() => act(() => api(`${runPath(session.room, run.id)}/extend`, session.session, { window: run.window }))}>{run.pendingExtension ? tr("Confirming extension…") : tr("Continue for 2 hours")}</button></div>, document.body)}
    {error && createPortal(<div className="recording-error" role="alert">{systemError(error)}<button className="quiet" aria-label={tr("Dismiss recording error")} onClick={() => setError('')}>×</button></div>, document.body)}
    {history && createPortal(<RecordingHistory room={session.room} title={session.roomName} secret={session.session} onClose={() => setHistory(false)} />, document.body)}
  </div>;
}

export function RecordingHistory({ room, title, secret = '', onClose }: { room: string; title: string; secret?: string; onClose: () => void }) {
  useLocale();
  const [history, setHistory] = useState<History | null>(null);
  const [selected, setSelected] = useState<Recording | null>(null);
  const [cancel, setCancel] = useState<Recording | null>(null);
  const [deleting,setDeleting]=useState<Recording|null>(null);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');
  const [editBase,setEditBase]=useState('');
  const [reviewed, setReviewed] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const player = useRef<HTMLAudioElement>(null);
  const selectedID = useRef('');
  async function refresh() {
    const result = await api<History>(`/api/rooms/${encodeURIComponent(room)}/history`, secret); setHistory(result);
    if (selectedID.current) { const id = selectedID.current; const detail = await api<Recording>(runPath(room, id), secret); if (selectedID.current === id) setSelected(detail); }
  }
  useEffect(() => { let active = true; const poll = async () => { try { if (active) await refresh(); } catch (e) { if (active) setError(errorMessage(e)); } }; void poll(); const timer = setInterval(poll, 3000); return () => { active = false; clearInterval(timer); }; }, [room, secret]);
  async function act(id: string, action: string, body: unknown = {}) { setBusy(true); setError(''); try { await api(`${runPath(room, id)}/${action}`, secret, body); setCancel(null); setDeleting(null); setEditing(false); if(action==='delete'){selectedID.current='';setSelected(null);} await refresh(); } catch (e) { setError(errorMessage(e)); } finally { setBusy(false); } }
  async function select(run: Recording) { selectedID.current = run.id; setSelected(run); setEditing(false); try { const detail = await api<Recording>(runPath(room, run.id), secret); if (selectedID.current === run.id) setSelected(detail); } catch (e) { setError(errorMessage(e)); } }
  function exportMinutes(revision: Revision) { const url = URL.createObjectURL(new Blob([revision.markdown], { type: 'text/markdown;charset=utf-8' })); const a = document.createElement('a'); a.href = url; a.download = 'checkpoint-minutes.md'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); }
  return <WorkspaceDialog title={tr("{{name}} · History", {name: title})} description={tr("Everyone admitted to this room can access its saved history, including earlier meetings.")} onClose={onClose} busy={busy}>
    <div className="recording-history">
      {error && <p role="alert">{systemError(error)}</p>}
      {!history ? <p role="status">{tr("Loading room history…")}</p> : history.recordings.length === 0 ? <p>{tr("No recordings yet.")}</p> : <ul className="recording-list">{history.recordings.map(run => <li key={run.id} className={selected?.id === run.id ? 'selected' : ''}>
        <button className="quiet recording-select" onClick={() => select(run)}><strong>{run.started ? new Date(run.started * 1000).toLocaleString(locale()) : tr("Starting recording…")}</strong><span>{run.state === 'saved' ? tr("Saved recording") : run.state === 'partial' ? tr("Partial recording") : tr(run.state)} · {tr(processingLabel[run.processing] || run.processing)}</span></button>
        {history.canManage && activeProcessing.includes(run.processing) && <button className="quiet danger-text" disabled={busy} onClick={() => setCancel(run)}>{tr("Cancel minutes generation")}</button>}
        {history.canManage && ['saved', 'partial'].includes(run.state) && ![...activeProcessing, 'cancelling'].includes(run.processing) && <button className="secondary" disabled={busy} onClick={() => act(run.id, 'generate')}>{['ready', 'cancelled', 'failed', 'no_speech'].includes(run.processing) ? tr("Generate minutes again") : tr("Create minutes")}</button>}
        {history.canDelete && !['deleting','deleted'].includes(run.state) && <button className="quiet danger-text" disabled={busy} onClick={()=>setDeleting(run)}>{tr("Delete recording")}</button>}
      </li>)}</ul>}
      {selected && <section className="recording-detail" aria-label={tr("Recording details")}>
        {selected.mixBytes > 0 && <audio ref={player} key={selected.id} controls preload="metadata" src={`${runPath(room, selected.id)}/audio`}>{tr("Audio playback is unavailable in this browser.")}</audio>}
        {selected.partial && <p className="notice">{tr("Partial recording: interruptions or gaps were detected.")}</p>}
        {selected.transcript && <details><summary>{t("segments", {count: selected.transcript.length})}</summary><ol className="transcript-list">{selected.transcript.map(segment => <li key={segment.id}><button className="quiet" onClick={() => { if (player.current) player.current.currentTime = segment.startMs / 1000; }}>{timecode(segment.startMs)}</button><div><strong>{segment.speaker}</strong><p>{segment.text}</p><small>{segment.id} · {segment.language}</small></div></li>)}</ol>{selected.transcript.length === 0 && <p>{tr("No speech detected.")}</p>}</details>}
        {selected.revisions?.map((revision, index) => <details key={revision.id} open={index === 0}><summary>{index === 0 ? tr("Minutes") : tr("Previous revision")} · {revision.reviewed ? tr("Reviewed") : tr("AI-generated · Not reviewed")}</summary><pre className="minutes-document">{revision.markdown}</pre><div className="recording-actions"><button className="secondary" onClick={() => exportMinutes(revision)}>{tr("Export Markdown")}</button>{index === 0 && history?.canManage && <button className="secondary" onClick={() => { setDraft(revision.markdown); setEditBase(revision.id); setReviewed(revision.reviewed); setEditing(true); }}>{tr("Edit / review")}</button>}</div></details>)}
      </section>}
      {cancel && <div className="recording-confirm" role="alertdialog" aria-labelledby="cancel-minutes-title"><h3 id="cancel-minutes-title">{tr("Cancel minutes generation?")}</h3><p>{new Date(cancel.started * 1000).toLocaleString(locale())}</p><p>{tr("The recording and any completed transcript will be kept. You can generate minutes again later.")}</p><button className="secondary" autoFocus disabled={busy} onClick={() => setCancel(null)}>{tr("Keep processing")}</button><button className="danger" disabled={busy} onClick={() => act(cancel.id, 'cancel', { confirmed: true })}>{tr("Cancel generation")}</button></div>}
      {deleting && <div className="recording-confirm" role="alertdialog" aria-labelledby="delete-recording-title"><h3 id="delete-recording-title">{tr("Delete recording and its minutes?")}</h3><p>{new Date(deleting.started*1000).toLocaleString(locale())}</p><p>{tr("The audio, transcript and every minutes revision will be permanently removed.")}</p><button className="secondary" autoFocus disabled={busy} onClick={()=>setDeleting(null)}>{tr("Keep recording")}</button><button className="danger" disabled={busy} onClick={()=>act(deleting.id,'delete',{confirmed:true})}>{tr("Delete permanently")}</button></div>}
      {editing && selected && <form className="minutes-editor" onSubmit={event => { event.preventDefault(); void act(selected.id, 'edit', { body: draft, reviewed, base: editBase }); }}><label>{tr("Minutes")}<textarea value={draft} onChange={event => setDraft(event.target.value)} rows={16} maxLength={128000} /></label><label><input type="checkbox" checked={reviewed} onChange={event => setReviewed(event.target.checked)} />{tr("Reviewed by me")}</label><button type="button" className="secondary" onClick={() => setEditing(false)}>{tr("Cancel")}</button><button disabled={busy}>{tr("Save new revision")}</button></form>}
    </div>
  </WorkspaceDialog>;
}
