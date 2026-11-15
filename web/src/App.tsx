import { useLanguageAccount } from './LanguageSettings';
import { tr, useLocale, locale, systemError } from './i18n';
import React, { useEffect, useRef, useState } from 'react';
import { LiveKitRoom } from '@livekit/components-react';
import { DisconnectReason } from 'livekit-client';
import '@livekit/components-styles';
import { Organizer, type Account } from './Organizer';
import { Conference } from './Conference';
import { LoadingScreen } from './LoadingScreen';
import { UserSettingsPage } from './UserSettingsPage';
import { DeviceCheckDialog } from './DeviceCheckDialog';
import './entry.css';
import './paper.css';
import { AppHeader, AppFooter, AccountActions } from './AppShell';
import { accessFromHash, parseRoomAccess } from './roomAccess';
import { api, errorMessage, type Session } from './api';

export default function App() {
 const languageAccount = useLanguageAccount();
  useLocale();
  const [settingsPage, setSettingsPage] = useState(location.pathname === '/settings');
  const [accountError, setAccountError] = useState('');
  const settingsTrigger = useRef<HTMLElement | null>(null);
  const settingsReturn = useRef(location.pathname === '/rooms' ? '/rooms' : '/');
  function navigate(path: string) { history.pushState(null, '', path); setSettingsPage(path === '/settings'); setOrganizer(path === '/rooms'); setError(''); setAccountError(''); }
  function openRooms() { navigate('/rooms'); }
  function goHome() { navigate('/'); }
  function openSettings() { if (settingsPage || session) return; settingsReturn.current = location.pathname; settingsTrigger.current = document.activeElement as HTMLElement; history.pushState(null, '', '/settings'); setSettingsPage(true); }
  function closeSettings() { navigate(settingsReturn.current); }
  useEffect(() => { const pop = () => { setSettingsPage(location.pathname === '/settings'); if (location.pathname !== '/settings') setOrganizer(location.pathname === '/rooms'); }; window.addEventListener('popstate', pop); return () => window.removeEventListener('popstate', pop); }, []);
  useEffect(() => { if (!settingsPage) settingsTrigger.current?.focus({ preventScroll: true }); }, [settingsPage]);
  const [session, setSession] = useState<Session | null>(null);
  useEffect(() => {
    if (!session || !settingsPage) return;
    history.replaceState(null, '', settingsReturn.current);
    setSettingsPage(false);
    setOrganizer(settingsReturn.current === '/rooms');
  }, [session, settingsPage]);
  const [organizer, setOrganizer] = useState(location.pathname === '/rooms');
  const [account, setAccount] = useState<Account | null>(null);
  const [name, setName] = useState('');
  const [incoming] = useState(() => {
    try {
      const access = accessFromHash(location.hash);
      const params = new URLSearchParams(location.hash.slice(1));
      const adminInvite = params.get('adminInvite')?.trim() || (!location.hash ? sessionStorage.getItem('checkpoint.adminInvite') || '' : '');
      if (access && adminInvite) throw new Error('Open one invitation at a time.');
      return { access, adminInvite, error: '' };
    } catch (error) { return { access: null, adminInvite: '', error: errorMessage(error) }; }
  });
  const [adminInvite, setAdminInvite] = useState(incoming.adminInvite);
  const [access, setAccess] = useState(incoming.access?.value || '');
  const [error, setError] = useState(incoming.error);
  const [busy, setBusy] = useState(false);
  const [connected, setConnected] = useState(true);
  const [healthURL, setHealthURL] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [devices, setDevices] = useState({ audioDeviceId: 'default', videoDeviceId: 'default', audioOutputId: 'default' });
  const [preview, setPreview] = useState(false);
  const [restoring, setRestoring] = useState(true);
  useEffect(() => {
    let active = true;
    history.replaceState(null, '', location.pathname);
    if (incoming.access || incoming.adminInvite || incoming.error) sessionStorage.removeItem('checkpoint.session');
    const secret = incoming.access || incoming.adminInvite || incoming.error ? null : sessionStorage.getItem('checkpoint.session');
    const loadAccount = api<Account>('/api/account').catch(() => null);
    const restoreSession = secret ? api<Session>('/api/session', secret, {}).then(next => ({ ...next, session: secret })).catch(error => {
      sessionStorage.removeItem('checkpoint.session'); if (active) setError(errorMessage(error)); return null;
    }) : Promise.resolve(null);
    void Promise.all([loadAccount, restoreSession]).then(([user, restored]) => {
      if (!active) return;
      setAccount(user); languageAccount.setUser(user);
      if (user) setName(user.username);
      setOrganizer(location.pathname === '/rooms' && !incoming.access && !incoming.error);
      if (restored) setSession(restored);
      setRestoring(false);
    });
    return () => { active = false; };
  }, []);
  useEffect(() => { if (adminInvite) sessionStorage.setItem('checkpoint.adminInvite', adminInvite); else sessionStorage.removeItem('checkpoint.adminInvite'); }, [adminInvite]);
  useEffect(() => { if (session) sessionStorage.setItem('checkpoint.session', session.session); }, [session]);
  useEffect(() => {
    if (!session?.waiting) return;
    let active = true;
    const timer = setInterval(() => { api<Session>('/api/session', session.session, {}).then(next => { if (active && !next.waiting) setSession({ ...next, session: session.session }); }).catch(e => { if (active) setError(errorMessage(e)); }); }, 2000);
    return () => { active = false; clearInterval(timer); };
  }, [session]);
  function clearSession() { sessionStorage.removeItem('checkpoint.session'); setSession(null); setError(''); setOrganizer(location.pathname === '/rooms'); }
  useEffect(() => { api<{ observerURL: string }>('/api/config').then(c => setHealthURL(c.observerURL)).catch(() => {}); }, []);
  async function join(e: React.FormEvent) {
    e.preventDefault(); setBusy(true); setError('');
    try { const credential = incoming.access && access.trim() === incoming.access.value ? incoming.access : parseRoomAccess(access); const result = await api<Session>('/api/join', '', { name: name.trim(), [credential.kind]: credential.value }); setSession(result); setPreview(false); setConnected(true); }
    catch (e) { setError(errorMessage(e)); } finally { setBusy(false); }
  }
  async function retry() {
    if (!session) return;
    setBusy(true); setError('');
    try { const next = await api<Session>('/api/session', session.session, {}); setSession({ ...next, session: session.session }); setAttempt(v => v + 1); setConnected(true); }
    catch (e) { setError(errorMessage(e)); } finally { setBusy(false); }
  }
  function cancelAdminInvite() { setAdminInvite(''); setError(''); setOrganizer(location.pathname === '/rooms'); }
  async function acceptAdminInvite() {
    setBusy(true); setError('');
    try { await api('/api/room-admin-invitations/accept', '', { invite: adminInvite }); setAdminInvite(''); openRooms(); }
    catch { setError('This invitation could not be accepted. It may have expired, been used or been revoked. Ask the owner for a new link.'); }
    finally { setBusy(false); }
  }
  const showSettings = settingsPage && !session && !restoring;
  const inCall = !!session && !session.waiting && connected;
  async function signOut() {
    if (session) return;
    setBusy(true); setError(''); setAccountError('');
    try {
      await api('/api/account/logout', '', {});
      clearSession(); setAccount(null); languageAccount.setUser(null); setName(''); goHome();
    } catch (cause) { setAccountError(errorMessage(cause)); if (inCall) setError(errorMessage(cause)); } finally { setBusy(false); }
  }
  const accountActions = <AccountActions locked={!!session || restoring} hideSignIn={organizer || !!session} account={account} busy={busy} onSettings={openSettings} onSignOut={signOut} onSignIn={openRooms} />;
  const header = <><AppHeader onHome={session ? closeSettings : goHome}>{accountActions}</AppHeader>{accountError && <p className="notice" role="alert">{systemError(accountError)}</p>}</>;
  const footer = <AppFooter healthURL={healthURL} />;
  return <><div hidden={showSettings} className={`app app-shell ${inCall ? 'in-call' : ''}`}>{!inCall && header}
    {restoring ? <LoadingScreen label={tr("Opening Checkpoint")} /> : !session && adminInvite && (account || !organizer) ? <main className="admin-invitation-screen">
      <div className="eyebrow">{tr("ROOM ADMIN INVITATION")}</div><h1>{tr("Become a room admin")}</h1>
      <p>{tr("The owner invited you to help run a room. You can host meetings, invite guests, admit participants and manage speakers.")}</p>
      <p className="hint">{tr("Closing the room, ending the meeting for everyone, removing people and appointing other admins stay with the owner.")}</p>
      {account ? <p className="invitation-account">{tr("Accepting as {{name}}", {name: account.username})}</p> : <p>{tr("Sign in to an existing account to accept this invitation.")}</p>}
      {error && <p role="alert">{systemError(error)}</p>}
      <div className="entry-actions"><button disabled={busy} onClick={account ? acceptAdminInvite : () => { openRooms(); }}>{account ? busy ? tr("Accepting…") : tr("Accept invitation") : tr("Sign in to accept invitation")}</button><button className="quiet" disabled={busy} onClick={cancelAdminInvite}>{tr("Cancel invitation")}</button></div>
    </main> : !session && organizer ? <Organizer initialUser={account} onAccountChange={user => { setAccount(user); languageAccount.setUser(user); if (user) setName(user.username); else setOrganizer(false); }} onBack={goHome} onJoin={next => { setSession(next); setConnected(true); }} /> : !session ? <main className="entry-home">
      <div className="entry-paths">
        <section className="entry-join-card">
          <h1>{tr("Join a room")}</h1><p>{tr("No account needed. Enter a room ID or paste an invite link.")}</p>
          {error && <p role="alert">{systemError(error)}</p>}
          <form className="entry-form" onSubmit={join}>
            <label>{tr("Your name")}<input value={name} maxLength={80} onChange={event => setName(event.target.value)} autoComplete="name" required /></label>
            <label>{tr("Room ID or link")}<input value={access} onChange={event => setAccess(event.target.value)} placeholder="XXXX-XXXX-XXXX-XXXX" autoComplete="off" autoCapitalize="characters" spellCheck={false} aria-describedby="room-access-help" required /></label>
            <p className="hint entry-invitation-help" id="room-access-help">{tr("Anyone with the ID or link can join. Your account password goes on the sign-in screen.")}</p>
            <button disabled={busy || !name.trim() || !access.trim()}>{busy ? tr("Connecting…") : tr("Join session →")}</button>
          </form>
          <div className="entry-actions"><button className="secondary" onClick={() => setPreview(true)} aria-haspopup="dialog">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><rect x="3" y="5" width="12" height="14" rx="3" /><path d="m15 9 6-3v12l-6-3" /></svg>{tr("Check devices before joining")}</button></div>
        </section>
        <a className="entry-rooms-link" href="/rooms" onClick={event => { if (!event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && event.button === 0) { event.preventDefault(); openRooms(); } }}><svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true"><rect x="3" y="3" width="7" height="7" rx="2" /><rect x="14" y="3" width="7" height="7" rx="2" /><rect x="3" y="14" width="7" height="7" rx="2" /><rect x="14" y="14" width="7" height="7" rx="2" /></svg><span>{tr("All rooms")}</span><span aria-hidden="true">→</span></a>
      </div>
      <div className="entry-footer"><p className="hint">{tr("Your microphone and camera start off. Turn them on when you are ready.")}</p></div>
      {preview && <DeviceCheckDialog name={name} devices={devices} onClose={() => setPreview(false)} onApply={choices => { setName(choices.username); setDevices(current => ({ ...current, audioDeviceId: choices.audioDeviceId, videoDeviceId: choices.videoDeviceId })); setPreview(false); }} />}

    </main> : <>
      {!inCall && <div className="session-heading"><strong>{session.roomName}</strong><span>{session.name}</span></div>}
      {error && <div className="notice" role="alert">{systemError(error)}</div>}
      {session.waiting ? <main className="welcome"><div className="eyebrow">{tr("WAITING ROOM")}</div><h1>{tr("Your host will let you in.")}</h1><p>{tr("No audio or video is sent while you wait.")}</p><button className="secondary" onClick={async () => { try { await api('/api/leave', session.session, {}); clearSession(); } catch (e) { setError(errorMessage(e)); } }}>{tr("Cancel request")}</button></main> : connected ? <LiveKitRoom key={attempt} serverUrl={session.serverURL} token={session.token} connect audio={false} video={false} options={{ adaptiveStream: true, dynacast: true, audioOutput: typeof HTMLMediaElement.prototype.setSinkId === 'function' ? { deviceId: devices.audioOutputId } : undefined, audioCaptureDefaults: { deviceId: devices.audioDeviceId }, videoCaptureDefaults: { deviceId: devices.videoDeviceId } }} onError={e => setError(e.message)} onDisconnected={reason => { if (reason === DisconnectReason.CLIENT_INITIATED) { setConnected(false); } else { setConnected(false); setError('Connection ended. You can rejoin when the service is available.'); } }} data-lk-theme="default">
        <Conference session={session} onLeave={clearSession} healthURL={healthURL} devices={devices} onDeviceChange={(kind, id) => setDevices(current => ({ ...current, [kind === 'audioinput' ? 'audioDeviceId' : kind === 'videoinput' ? 'videoDeviceId' : 'audioOutputId']: id }))} accountActions={accountActions} />
      </LiveKitRoom> : <div className="welcome"><h1>{tr("Connection interrupted")}</h1><p>{tr("Your session and role are saved. Microphone and camera will start off when you rejoin.")}</p><button onClick={retry} disabled={busy}>{tr("Rejoin session")}</button><button className="secondary" onClick={clearSession}>{tr("Leave")}</button></div>}
    </>}
    {!inCall && footer}
  </div>{showSettings && <UserSettingsPage header={header} footer={footer} account={account} loading={restoring} inCall={inCall} onBack={closeSettings} onPasswordChanged={() => { sessionStorage.removeItem('checkpoint.session'); setSession(null); setAccount(null); languageAccount.setUser(null); navigate('/rooms'); }} />}</>;
}

