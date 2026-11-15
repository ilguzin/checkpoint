import { LanguageSettings } from './LanguageSettings';
import { t, tr, useLocale, locale, systemError } from './i18n';
import { useEffect, useState } from 'react';
import { api, errorMessage } from './api';
import './service-pages.css';

type Check = { state: string; checkedAt: string; source: string; latencyMs: number };
type Health = { core: Check; signaling: Check; media: Check; recording?: Check; processing?: Check; processingQueue?: number; now: string };
type Day = { date: string; samples: number; expected: number; coreAvailability: number | null; signalAvailability: number | null };
type History = { days: Day[]; incidents: { component: string; started: number; ended: number | null }[]; source: string };
type Quality = { source: string; core_ms: { samples: number; p95Ms: number | null }; signal_ms: { samples: number; p95Ms: number | null } };
const tabs = ['Overview', 'Connection quality', 'History', 'Connection check'] as const;
export default function HealthPage() {
  useLocale();
  const [snapshot, setSnapshot] = useState<Health | null>(null); const [updated, setUpdated] = useState(0); const [now, setNow] = useState(Date.now());
  const [tab, setTab] = useState<typeof tabs[number]>('Overview'); const [history, setHistory] = useState<History | null>(null); const [quality, setQuality] = useState<Quality | null>(null);
  const [error, setError] = useState(''); const [checking, setChecking] = useState(false); const [result, setResult] = useState<number[] | null>(null);
  const [selectedDay, setSelectedDay] = useState<Day | null>(null);
  const [mediaResult, setMediaResult] = useState<{ packets: number; lost: number; jitterMs: number } | null>(null);
  useEffect(() => {
    let active = true;
    const refresh = () => api<Health>('/api/health').then(data => { if (active) { setSnapshot(data); setUpdated(Date.now()); } }).catch(() => {});
    void refresh(); const poll = setInterval(refresh, 5000); const clock = setInterval(() => setNow(Date.now()), 1000);
    return () => { active = false; clearInterval(poll); clearInterval(clock); };
  }, []);
  useEffect(() => {
    let active = true; setError('');
    if (tab === 'History') api<History>('/api/history').then(value => { if (active) setHistory(value); }).catch(() => { if (active) setError('History is temporarily unavailable.'); });
    if (tab === 'Connection quality') api<Quality>('/api/quality').then(value => { if (active) setQuality(value); }).catch(() => { if (active) setError('Quality measurements are temporarily unavailable.'); });
    return () => { active = false; };
  }, [tab]);
  async function check() {
    setChecking(true); setError(''); setResult(null); setMediaResult(null);
    let room: import('livekit-client').Room | null = null;
    try {
      const times: number[] = [];
      for (let i = 0; i < 5; i++) { const start = performance.now(); await api('/api/check'); times.push(Math.round(performance.now() - start)); }
      setResult(times);
      const session = await api<{ token: string; serverURL: string }>('/api/probe', '', {});
      const { Room } = await import('livekit-client'); room = new Room();
      await room.connect(session.serverURL, session.token, { autoSubscribe: true });
      const until = Date.now() + 12000;
      while (Date.now() < until) {
        for (const participant of room.remoteParticipants.values()) for (const publication of participant.trackPublications.values()) {
          const stats = await publication.track?.getRTCStatsReport();
          if (stats) for (const entry of stats.values()) if (entry.type === 'inbound-rtp' && entry.kind === 'audio' && entry.packetsReceived >= 50) {
            setMediaResult({ packets: entry.packetsReceived, lost: Math.max(0, entry.packetsLost || 0), jitterMs: Math.round((entry.jitter || 0) * 1000) }); return;
          }
        }
        await new Promise(resolve => setTimeout(resolve, 250));
      }
      throw new Error('Signaling connected, but synthetic media did not arrive in time. Try another network.');
    } catch (e) { setError(errorMessage(e)); } finally { await room?.disconnect(); setChecking(false); }
  }

  const stale = !updated || now - updated > 15000;
  const state = (c?: Check) => stale ? 'unknown' : c?.state || 'unknown';
  const core = state(snapshot?.core), signaling = state(snapshot?.signaling);
  const headline = core === 'maintenance' ? tr("Scheduled service maintenance") : core === 'outage' ? tr("Joining sessions is unavailable") : signaling === 'outage' ? tr("The call service is unavailable") : core === 'operational' && signaling === 'operational' ? tr("Session services are responding") : tr("Waiting for fresh measurements");
  return <div className="app health"><header className="service-header"><a className="brand" href="/">◈ Checkpoint</a><span className="service-header-label">{tr("Service health")}</span><span className={`badge service-update ${core === 'operational' && signaling === 'operational' ? 'operational' : 'unknown'}`}>{stale ? tr("Waiting for an update") : tr("Updated just now")}</span><LanguageSettings /></header>
    <nav className="health-nav" aria-label={tr("Service health pages")}>{tabs.map(label => <button className={tab === label ? 'active' : 'secondary'} key={label} aria-current={tab === label ? 'page' : undefined} onClick={() => setTab(label)}>{tr(label)}</button>)}</nav>
    <main className="service-main">{error && <p role="alert">{systemError(error)}</p>}
      {tab === 'Overview' && <><div className="eyebrow">{tr("CHECKPOINT SERVICE HEALTH")}</div><h1>{headline}</h1><p>{core === 'outage' && signaling === 'operational' ? tr("Existing media connections may continue. Joining and session controls are affected.") : tr("A clear view of what is responding, what is affected, and what still needs checking.")}</p><div className="health-grid">{([
        [tr("Joining & session controls"), snapshot?.core, tr("Can the application respond to requests?")],
        [tr("Call service API"), snapshot?.signaling, tr("Can the media service process an authenticated request?")],
        [tr("Audio, video & screen sharing"), snapshot?.media, tr("A successful server check does not prove that media works from your network.")],
        [tr("Meeting recordings"), snapshot?.recording, tr("Optional audio recording service. Its availability does not affect calls.")],
        [tr("Meeting minutes"), snapshot?.processing, tr("Optional transcription and minutes processing. Queue: {{count}}.", {count: snapshot?.processingQueue ?? 0})],
      ] as [string, Check | undefined, string][]).map(([title, value, description]) => <article key={title}><span className={`badge ${state(value)}`}>{tr(state(value))}</span><h2>{title}</h2><p>{description}</p><small>{tr("Source:")} {tr(value?.source || "No measurement yet")}</small><small>{tr("Checked:")} {value?.checkedAt && !value.checkedAt.startsWith('0001') ? new Date(value.checkedAt).toLocaleTimeString(locale()) : tr("Not yet measured")}</small></article>)}</div>
      <section className="panel"><h2>{tr("Checking from your network?")}</h2><p>{tr("Run a connection check from this device. It starts only when you choose to run it.")}</p><button onClick={() => setTab('Connection check')}>{tr("Check my connection")}</button></section></>}
      {tab === 'Connection quality' && <><div className="eyebrow">{tr("MEASUREMENTS · LAST 24 HOURS")}</div><h1>{tr("Know what the numbers mean.")}</h1><p>{tr("These timings measure local application and call-service responses. They are not network latency, packet loss or audio quality for your participants.")}</p><div className="health-grid">{[[tr("Application response"), quality?.core_ms], [tr("Call service response"), quality?.signal_ms]].map(([label, raw]) => { const value = raw as Quality['core_ms'] | undefined; return <article key={String(label)}><h2>{String(label)}</h2><div className="metric">{value?.p95Ms == null ? tr("Not enough data") : tr("{{value}} ms", {value: value.p95Ms})}</div><p>{t("samples", {count: value?.samples || 0})}</p><small>{tr("Source: local server probes. At least 12 samples are needed.")}</small></article>; })}<article><h2>{tr("End-to-end media")}</h2><div className="metric">{tr("Unknown")}</div><p>{tr("No continuous media quality measurements are available yet.")}</p></article></div></>}
      {tab === 'History' && <><div className="eyebrow">{tr("OBSERVATION HISTORY · UTC")}</div><h1>{tr("See what happened.")}</h1><p>{tr("Availability is calculated from observed checks only. Gaps are unknown and shown as coverage. At least one minute of observations is required for a percentage.")}</p><div className="day-bars" aria-label={tr("30 days of observations")}>{history?.days.map(day => <button key={day.date} aria-pressed={selectedDay?.date === day.date} className={day.coreAvailability == null || day.signalAvailability == null ? 'unknown' : (day.coreAvailability ?? 100) < 100 || (day.signalAvailability ?? 100) < 100 ? 'outage' : 'operational'} aria-label={tr("{{date}}: {{count}} of {{expected}} checks", {date: day.date, count: day.samples, expected: day.expected})} onClick={() => setSelectedDay(day)} />)}</div>{selectedDay && <article><h2>{selectedDay.date}</h2><p>{tr("Coverage: {{samples}}/{{expected}} checks ({{percent}}%).", {samples: selectedDay.samples,expected: selectedDay.expected,percent:(100*selectedDay.samples/selectedDay.expected).toLocaleString(locale(),{maximumFractionDigits:1})})}</p><p>{tr("Joining:")} {selectedDay.coreAvailability == null ? tr("Not enough data") : tr("{{value}}% of observed checks", {value: selectedDay.coreAvailability.toLocaleString(locale(), {maximumFractionDigits:2})})}{tr(". Call service:")} {selectedDay.signalAvailability == null ? tr("Not enough data") : tr("{{value}}% of observed checks", {value: selectedDay.signalAvailability.toLocaleString(locale(), {maximumFractionDigits:2})})}.</p></article>}
      <h2 className="service-section-title">{tr("Detected incidents")}</h2>{history?.incidents.length === 0 && <p>{tr("No outages were recorded in the observed intervals. Unobserved time is not a guarantee of availability.")}</p>}<div className="incident-list">{history?.incidents.map(incident => <article key={`${incident.component}-${incident.started}`}><span className={`badge ${incident.ended ? 'operational' : 'outage'}`}>{incident.ended ? tr("Recovered") : tr("Open")}</span><h2>{incident.component === 'joining' ? tr("Joining sessions was affected") : tr("Call service was affected")}</h2><p>{tr("Detected")} {new Date(incident.started * 1000).toLocaleString(locale())}{incident.ended ? tr(" · Recovered {{time}}", {time: new Date(incident.ended * 1000).toLocaleString(locale())}) : ''}</p><small>{tr("Automatic local checks detected this change. A root cause has not been established.")}</small></article>)}</div></>}
      {tab === 'Connection check' && <><div className="eyebrow">{tr("THIS DEVICE · THIS NETWORK")}</div><h1>{tr("Check your way in.")}</h1><p>{tr("This check sends five small HTTPS requests, connects to an isolated test room and receives synthetic silent audio for a few seconds. It does not request your microphone or camera, record anything, or publish your result.")}</p><button aria-busy={checking} disabled={checking} onClick={check}>{checking ? tr("Checking…") : tr("Run connection check")}</button>{result && <article className="panel"><span className="badge operational">{tr("HTTPS reachable")}</span><h2>{tr("Your browser can reach this site.")}</h2><p>{tr("Five requests succeeded. Median round trip: {{value}} ms.", {value:[...result].sort((a,b)=>a-b)[2]})}</p><p>{tr("Source: this browser to this Observer site, measured just now.")}</p></article>}{mediaResult && <article className="panel"><span className="badge operational">{tr("Signaling and media received")}</span><h2>{tr("Your browser received test media.")}</h2><p>{tr("{{packets}} audio packets received · {{lost}} lost · {{jitter}} ms jitter.", {packets: mediaResult.packets, lost: mediaResult.lost, jitter: mediaResult.jitterMs})}</p><small>{tr("Measured in this browser, just now. A short synthetic test does not guarantee the quality of a long meeting or prove video and screen sharing.")}</small></article>}<p className="hint">{tr("No sound is played. The test room expires automatically. If joining a call fails, try another network and share the error with your organizer.")}</p></>}
      <footer><p className="hint">{tr("Checks run every five seconds on the same server as your meetings. Two consecutive failures/successes change an incident state. Results older than 15 seconds become Unknown. This site cannot independently detect a complete server or network outage.")}</p><p className="hint">{stale ? tr("No fresh data. Last known results are not a current health signal.") : tr("Last status update: {{time}}", {time: new Date(updated).toLocaleTimeString(locale())})}</p></footer>
    </main></div>;
}
