import { LocalizedTile } from './LocalizedTile';
import { DeviceControl } from './DeviceControl';
import { tr, useLocale, locale, systemError } from './i18n';
import { RecordingControls } from './Recordings';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { TrackToggle, GridLayout, CarouselLayout, useSpeakingParticipants, RoomAudioRenderer, StartAudio, useConnectionState, useLocalParticipant, useRoomContext, useTracks } from '@livekit/components-react';
import { ConnectionState, RoomEvent, Track, type LocalTrackPublication } from 'livekit-client';
import { api, errorMessage, type Session } from './api';
import { MicrophoneIndicator } from './MicrophoneIndicator';
import { CallIcon } from './CallIcon';

import { Avatar, MessagePanel, groupMembers, type Meeting, type CallPanel } from './CallPanels';
export function Conference({ session, onLeave, healthURL, devices, onDeviceChange, accountActions }: {
  session: Session; onLeave: () => void; healthURL: string;
  devices: { audioDeviceId: string; videoDeviceId: string; audioOutputId: string };
  onDeviceChange: (kind: MediaDeviceKind, id: string) => void; accountActions: ReactNode;
}) {
  useLocale();
  const room = useRoomContext(); const state = useConnectionState(); const { localParticipant } = useLocalParticipant();
  const [meeting, setMeeting] = useState<Meeting | null>(null); const [publish, setPublish] = useState(false); const [sharing, setSharing] = useState(false);
  const [error, setError] = useState(''); const [busy, setBusy] = useState(false); const [panel, setPanel] = useState<CallPanel | null>(null);
  const [deviceMenu, setDeviceMenu] = useState<MediaDeviceKind | null>(null);
  const [outputMuted, setOutputMuted] = useState(false);
  const [drafts, setDrafts] = useState({ chat: '', questions: '' });
  const panelTrigger = useRef<HTMLElement | null>(null);
  const panelHeading = useRef<HTMLHeadingElement>(null); const [quality, setQuality] = useState('unknown');
  const tracks = useTracks([{ source: Track.Source.Camera, withPlaceholder: true }, { source: Track.Source.ScreenShare, withPlaceholder: false }]);
  const screens = tracks.filter(t => t.source === Track.Source.ScreenShare);
  const cameras = tracks.filter(t => t.source === Track.Source.Camera && (session.mode !== 'webinar' || meeting?.members.some(m => m.identity === t.participant.identity && m.canPublish)));
  const speakers = useSpeakingParticipants();
  const [focusedIdentity, setFocusedIdentity] = useState('');
  const candidate = speakers.find(speaker => cameras.some(track => track.participant.identity === speaker.identity))?.identity;
  useEffect(() => {
    if (!candidate || candidate === focusedIdentity) return;
    // Keep the stage stable during short interjections and pauses.
    const timer = setTimeout(() => setFocusedIdentity(candidate), 700);
    return () => clearTimeout(timer);
  }, [candidate, focusedIdentity]);
  const focused = cameras.find(track => track.participant.identity === focusedIdentity) || cameras.find(track => !track.participant.isLocal) || cameras[0];
  const others = cameras.filter(track => track.participant.identity !== focused?.participant.identity);
  const self = meeting?.members.find(m => m.identity === session.identity);
  async function refresh() { const data = await api<Meeting>('/api/meeting', session.session); setMeeting(data); }
  async function action(fn: () => Promise<void>) { setBusy(true); setError(''); try { await fn(); await refresh(); } catch (e) { setError(errorMessage(e)); try { await refresh(); } catch { /* Keep the last known view until the next successful poll. */ } } finally { setBusy(false); } }
  useEffect(() => {
    let active = true;
    const poll = () => api<Meeting>('/api/meeting', session.session).then(m => { if (active) setMeeting(m); }).catch(e => { if (active) setError(errorMessage(e)); });
    void poll(); const timer = setInterval(poll, 2000); return () => { active = false; clearInterval(timer); };
  }, [session.session]);
  useEffect(() => {
    const changed = () => { setPublish(!!localParticipant.permissions?.canPublish); setSharing(localParticipant.isScreenShareEnabled); setQuality(localParticipant.connectionQuality); };
    const unpublished = (pub: LocalTrackPublication) => { changed(); if (pub.source === Track.Source.ScreenShare) void api('/api/screen', session.session, { claim: false }).catch(() => {}); };
    room.on(RoomEvent.ParticipantPermissionsChanged, changed).on(RoomEvent.LocalTrackPublished, changed).on(RoomEvent.LocalTrackUnpublished, unpublished).on(RoomEvent.ConnectionQualityChanged, changed);
    changed(); return () => { room.off(RoomEvent.ParticipantPermissionsChanged, changed).off(RoomEvent.LocalTrackPublished, changed).off(RoomEvent.LocalTrackUnpublished, unpublished).off(RoomEvent.ConnectionQualityChanged, changed); };
  }, [room, localParticipant, session.session]);
  useEffect(() => {
    if (!sharing) return;
    const tick = setInterval(() => { void api('/api/screen', session.session, { claim: true }).catch(e => { setError(errorMessage(e)); void localParticipant.setScreenShareEnabled(false); }); }, 10000);
    return () => clearInterval(tick);
  }, [sharing, localParticipant, session.session]);
  async function shareScreen() {
    await action(async () => {
      if (sharing) { await localParticipant.setScreenShareEnabled(false); await api('/api/screen', session.session, { claim: false }); return; }
      // Request capture while the click still carries browser user activation.
      // Nothing is published until the server grants this participant the lease.
      const captured = await localParticipant.createScreenTracks({ audio: true });
      try {
        await api('/api/screen', session.session, { claim: true });
        const until = Date.now() + 5000;
        while (!localParticipant.permissions?.canPublishSources?.includes(3) && Date.now() < until) await new Promise(resolve => setTimeout(resolve, 50));
        if (!localParticipant.permissions?.canPublishSources?.includes(3)) throw new Error('Screen sharing permission did not arrive. Please try again.');
        for (const track of captured) await localParticipant.publishTrack(track);
      } catch (e) {
        for (const track of captured) { track.stop(); await localParticipant.unpublishTrack(track).catch(() => {}); }
        await api('/api/screen', session.session, { claim: false }).catch(() => {}); throw e;
      }
    });
  }
  async function leave() { await action(async () => { await api('/api/leave', session.session, {}); await room.disconnect(); onLeave(); }); }
  const canShare = typeof navigator.mediaDevices?.getDisplayMedia === 'function';
  const role = session.role === 'owner' ? tr("Owner") : session.moderator ? tr("Room admin") : publish ? (session.mode === 'meeting' ? tr("Participant") : tr("Presenter")) : tr("Viewer");
  function closePanel() { setPanel(null); panelTrigger.current?.focus({ preventScroll: true }); }
  function togglePanel(next: CallPanel) {
    if (panel === next) { closePanel(); return; }
    panelTrigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setPanel(next);
  }
  useEffect(() => {
    if (!panel) return;
    panelHeading.current?.focus({ preventScroll: true });
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') closePanel(); };
    window.addEventListener('keydown', escape); return () => window.removeEventListener('keydown', escape);
  }, [panel]);
  async function toggleOutput() {
    if (outputMuted) {
      try { await room.startAudio(); setOutputMuted(false); }
      catch (e) { setError(errorMessage(e)); }
    } else setOutputMuted(true);
  }
  const screenDisabled = busy || !canShare || (!!meeting?.screenOwner && meeting.screenOwner !== session.identity);
  return <div className="conference">
    <div className="session-controls" onClick={() => setDeviceMenu(null)}>
      <span className="app-brand-mark call-brand" aria-hidden="true"><CallIcon name="check" /></span>
      <strong className="call-title" title={session.roomName}>{session.roomName}</strong>
      <div className="call-connection" role="status" aria-label={tr(state)} title={state === ConnectionState.Reconnecting ? tr("Reconnecting… Your devices will keep their previous settings.") : tr(state)}>
        <span className={`dot ${state === ConnectionState.Connected ? 'good' : ''}`} /><span>{tr(state)}</span>
      </div>

      {accountActions}
    </div>
    {quality === 'poor' && <p className="call-notice notice">{tr("Your connection is weak. Try turning off video to prioritize audio.")}</p>}
    {error && <p className="call-notice" role="alert">{systemError(error)}</p>}
    {state === ConnectionState.Reconnecting && <p className="call-notice notice">{tr("Reconnecting… Your devices will keep their previous settings.")}</p>}
    <div className={`call-layout ${panel ? 'with-panel' : ''}`}>
      <main className={`media-stage ${screens.length ? 'has-screen' : ''}`} aria-label={session.mode === 'webinar' ? tr("Webinar stage") : tr("Meeting grid")}>
        {screens.map(t => <LocalizedTile className="screen-tile" key={t.participant.identity} trackRef={t} />)}
        {focused ? screens.length ? <GridLayout tracks={cameras} className="camera-strip"><LocalizedTile /></GridLayout> : <div className={`focus-layout ${others.length ? 'has-others' : ''}`}><LocalizedTile className="focused-tile" trackRef={focused} />{others.length > 0 && <CarouselLayout tracks={others} className="lk-carousel participant-filmstrip"><LocalizedTile /></CarouselLayout>}</div> : <div className="stage-empty"><span className="brand-mark">◈</span><h2>{session.mode === 'webinar' ? tr("The stage is ready.") : tr("You are here.")}</h2><p>{session.mode === 'webinar' ? tr("Your host will be with you shortly.") : tr("Invite your team to join the conversation.")}</p></div>}
      </main>
      {panel && <aside className={`call-panel panel-${panel}`} aria-labelledby="call-panel-title">
        <div className="call-panel-heading">
          <span className="panel-heading-icon"><CallIcon name={panel === 'settings' ? 'info' : panel} /></span>
          <h2 ref={panelHeading} tabIndex={-1} id="call-panel-title">{panel === 'people' ? tr("People") : panel === 'chat' ? tr("Chat") : panel === 'questions' ? tr("Questions") : tr("Meeting details")}</h2>
          {panel === 'people' && <span className="panel-count">{meeting?.members.length || 1}</span>}
          <button className="quiet panel-close" onClick={closePanel} aria-label={tr("Close panel")} title={tr("Close panel")}><CallIcon name="close" /></button>
        </div>
        {panel === 'settings' ? <div className="panel-scroll">
          <section className="detail-section meeting-summary"><span className="eyebrow">{tr(session.mode)}</span><h3>{session.roomName}</h3><div className="details-connection"><span className={`dot ${state === ConnectionState.Connected ? 'good' : ''}`} />{tr(state)}</div></section>
          <section className="detail-section">
            <h3>{tr("You in this meeting")}</h3>
            <div className="call-identity"><Avatar name={session.name} /><div className="identity-copy"><strong>{session.name}</strong><span>{role}</span></div></div>
            <p>{publish ? tr("You can speak and share your screen. Use the controls below to turn on your devices.") : tr("You are watching. A host can invite you to speak.")}</p>
          </section>
          <div className="mobile-call-actions">
            <button className="secondary small-screen-people" onClick={() => setPanel('people')}><CallIcon name="people" />{tr("People")}</button>
            <button className="secondary small-screen-chat" onClick={() => setPanel('chat')}><CallIcon name="chat" />{tr("Chat")}</button>
            {publish && <button className="secondary mobile-share" disabled={screenDisabled} onClick={shareScreen}><CallIcon name="screen" />{sharing ? tr("Stop sharing") : tr("Share screen")}</button>}
            <button className="secondary small-screen-hand" disabled={busy} onClick={() => action(async () => { await api('/api/hand', session.session, { raised: !self?.handAt }); })}><CallIcon name="hand" />{self?.handAt ? tr("Lower hand") : tr("Raise hand")}</button>
            {session.mode === 'webinar' && <button className="secondary mobile-questions" onClick={() => setPanel('questions')}><CallIcon name="questions" />{tr("Questions")}</button>}
          </div>
          {healthURL && <a className="call-health-link" href={healthURL} target="_blank" rel="noreferrer"><CallIcon name="info" /><span>{tr("Service health")}</span><span aria-hidden="true">↗</span></a>}
        </div> : panel === 'people' ? <div className="panel-scroll">
          {groupMembers(meeting?.members || []).map(group => <section className={`people-group people-group-${group.key}`} key={group.key} aria-label={group.label}><h3>{group.key === 'hands' && <CallIcon name="hand" />}{group.label}<span className="panel-count">{group.members.length}</span></h3>{group.key === 'hands' && <p className="queue-hint">{tr('In the order hands were raised')}</p>}<ul className="people-list">{group.members.map((member, index) => <li key={member.identity}>
            <Avatar name={member.name} />
            <div className="person-copy"><strong>{member.name}{member.identity === session.identity && <span className="self-label"> {tr("(you)")}</span>}</strong><span>{!member.admitted ? tr("Waiting to join") : member.role === 'owner' ? tr("Owner") : member.moderator ? tr("Room admin") : member.canPublish ? session.mode === 'meeting' ? tr("Participant") : tr("Presenter") : tr("Viewer")}</span></div>
            {member.handAt > 0 && <span className="raised-hand" title={tr("Hand raised")} aria-label={tr("Hand raised")}><CallIcon name="hand" /><span className="queue-position">{index + 1}</span></span>}
            {session.moderator && !member.moderator && <details className="member-actions"><summary aria-label={tr("Actions for {{name}}", {name: member.name})} title={tr("Actions for {{name}}", {name: member.name})}><CallIcon name="more" /></summary><div className="member-action-buttons">
              {!member.admitted ? <button className="secondary" disabled={busy} onClick={() => action(async () => { await api('/api/admit', session.session, { identity: member.identity }); })}>{tr("Admit")}</button> : <button className="secondary" disabled={busy} onClick={() => action(async () => { await api('/api/permissions', session.session, { identity: member.identity, canPublish: !member.canPublish }); })}>{member.canPublish ? tr("Move to audience") : tr("Invite to speak")}</button>}
              {session.role === 'owner' && <button className="quiet danger-text" disabled={busy} onClick={() => { if (confirm(tr("Remove {{name}}? This disconnects them and revokes any individual invitation they used. Anyone who still knows the room ID can join again unless you lock new guest joins.", {name: member.name}))) void action(async () => { await api('/api/kick', session.session, { identity: member.identity }); }); }}>{tr("Remove")}</button>}
            </div></details>}
          </li>)}</ul></section>)}
          {!meeting && <p className="hint" role="status">{tr("Loading participants…")}</p>}
          {session.role === 'owner' && <section className="moderation"><h3>{tr("Meeting access")}</h3>
            <label className="check-label"><span><strong>{tr("Lock new guest joins")}</strong><small>{tr("Keep the current group together.")}</small></span><input type="checkbox" role="switch" aria-label={tr("Lock new guest joins")} disabled={busy} checked={meeting?.locked || false} onChange={event => { const locked = event.target.checked; if (meeting) setMeeting({ ...meeting, locked }); void action(async () => { await api('/api/meeting/policy', session.session, { locked, waitingRoom: meeting?.waitingRoom || false }); }); }} /></label>
            <label className="check-label"><span><strong>{tr("Waiting room for new guests")}</strong><small>{tr("Let each new guest in yourself.")}</small></span><input type="checkbox" role="switch" aria-label={tr("Waiting room for new guests")} disabled={busy} checked={meeting?.waitingRoom || false} onChange={event => { const waitingRoom = event.target.checked; if (meeting) setMeeting({ ...meeting, waitingRoom }); void action(async () => { await api('/api/meeting/policy', session.session, { locked: meeting?.locked || false, waitingRoom }); }); }} /></label>
            {session.role === 'owner' && <button className="quiet danger-text end-meeting" disabled={busy} onClick={() => { if (confirm(tr("End this meeting for everyone? Chat and questions will be deleted. The room stays available for another meeting."))) void action(async () => { await api('/api/meeting/end', session.session, {}); await room.disconnect(); onLeave(); }); }}>{tr("End meeting for everyone")}</button>}
          </section>}
        </div> : <MessagePanel key={panel} kind={panel} identity={session.identity} messages={meeting?.messages.filter(message => message.kind === (panel === 'chat' ? 'chat' : 'question')) || []} loading={!meeting} moderator={session.moderator} busy={busy} draft={drafts[panel]} onDraft={value => setDrafts(current => ({ ...current, [panel]: value }))} onSend={() => {
          const kind = panel; const body = drafts[kind];
          void action(async () => { await api('/api/messages', session.session, { kind: kind === 'chat' ? 'chat' : 'question', body: body.trim() }); setDrafts(current => current[kind] === body ? { ...current, [kind]: '' } : current); });
        }} onAnswer={id => { void action(async () => { await api('/api/questions/answer', session.session, { id }); }); }} />}
      </aside>}

    </div>
    <div className="call-dock" aria-label={tr("Call controls")}><div className="dock-devices">
      {publish && <div className="lk-control-bar">{([{ source: Track.Source.Microphone, kind: 'audioinput', selected: devices.audioDeviceId, label: tr("Microphone") }, { source: Track.Source.Camera, kind: 'videoinput', selected: devices.videoDeviceId, label: tr("Camera") }] as const).map(device => <DeviceControl key={device.source} kind={device.kind} label={device.label} selected={device.selected} onSelect={id => onDeviceChange(device.kind, id)} onError={setError} open={deviceMenu === device.kind} onOpen={() => setDeviceMenu(device.kind)} onClose={() => setDeviceMenu(null)}><TrackToggle source={device.source} className={device.source === Track.Source.Camera ? 'camera-toggle' : undefined} aria-label={device.label} title={device.label} showIcon={device.source !== Track.Source.Microphone} onDeviceError={() => setError('Device unavailable. Check browser permissions and device settings.')}>{device.source === Track.Source.Microphone && <MicrophoneIndicator />}</TrackToggle></DeviceControl>)}</div>}
      <DeviceControl kind="audiooutput" label={tr("Audio output")} selected={devices.audioOutputId} onSelect={id => onDeviceChange('audiooutput', id)} onError={setError} open={deviceMenu === 'audiooutput'} onOpen={() => setDeviceMenu('audiooutput')} onClose={() => setDeviceMenu(null)}><button className="lk-button audio-output-toggle" data-lk-enabled={!outputMuted} aria-label={tr("Audio output")} aria-pressed={!outputMuted} title={outputMuted ? tr("Unmute audio output") : tr("Mute audio output")} onClick={toggleOutput}><CallIcon name={outputMuted ? 'audioOff' : 'audio'} /></button></DeviceControl>
      </div><div className="dock-actions">
      {publish && <button className={`dock-button dock-share ${sharing ? 'active' : ''}`} data-lk-source="screen_share" aria-label={sharing ? tr("Stop sharing") : tr("Share screen")} aria-pressed={sharing} disabled={screenDisabled} onClick={shareScreen} title={canShare ? (sharing ? tr("Stop sharing") : tr("Share screen")) : tr("Screen sharing is unavailable in this browser")}><CallIcon name="screen" /></button>}
      <button className={`dock-button dock-hand ${self?.handAt ? 'active' : ''}`} aria-label={self?.handAt ? tr("Lower hand") : tr("Raise hand")} title={self?.handAt ? tr("Lower hand") : tr("Raise hand")} aria-pressed={!!self?.handAt} disabled={busy} onClick={() => action(async () => { await api('/api/hand', session.session, { raised: !self?.handAt }); })}><CallIcon name="hand" /></button>
      <RecordingControls session={session} />
      <span className="dock-divider" />
      <button className={`dock-button dock-people ${panel === 'people' ? 'selected' : ''}`} aria-label={tr("People ({{count}})", {count: meeting?.members.length || 1})} title={tr("People")} aria-expanded={panel === 'people'} onClick={() => togglePanel('people')}><CallIcon name="people" /><span className="participant-count">{meeting?.members.length || 1}</span></button>
      <button className={`dock-button dock-chat ${panel === 'chat' ? 'selected' : ''}`} aria-label={tr("Chat")} title={tr("Chat")} aria-expanded={panel === 'chat'} onClick={() => togglePanel('chat')}><CallIcon name="chat" /></button>
      {session.mode === 'webinar' && <button className={`dock-button dock-questions ${panel === 'questions' ? 'selected' : ''}`} aria-label={tr("Questions")} title={tr("Questions")} aria-expanded={panel === 'questions'} onClick={() => togglePanel('questions')}><CallIcon name="questions" /></button>}
      <button className={`dock-button ${panel === 'settings' ? 'selected' : ''}`} aria-label={tr("Meeting details")} title={tr("Meeting details")} aria-expanded={panel === 'settings'} onClick={() => togglePanel('settings')}><CallIcon name="info" /></button>
      <button className="dock-button danger" aria-label={tr("Leave")} title={tr("Leave")} onClick={leave} disabled={busy}><CallIcon name="leave" /></button>
    </div></div><RoomAudioRenderer volume={outputMuted ? 0 : 1} /><StartAudio label={tr("Enable sound")} />
  </div>;
}
