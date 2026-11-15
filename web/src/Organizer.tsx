import { useLanguageAccount } from './LanguageSettings';
import { tr, useLocale, locale, systemError } from './i18n';
import { RecordingHistory } from './Recordings';
import { useEffect, useState, type FormEvent } from 'react';
import { api, errorMessage, type Session } from './api';
import { CallIcon } from './CallIcon';
import { WorkspaceDialog } from './WorkspaceDialog';
import './workspace.css';
import { parseRoomAccess } from './roomAccess';

export type Account = { id: string; username: string; admin?: boolean; disabled?: boolean; uiLocale?: import("./i18n").Preference };
type Room = { id: string; name: string; mode: 'meeting' | 'webinar'; ownerId: string; accessRole: 'owner' | 'admin' | 'shared'; closed: boolean; deletionState?: string };
type RoomAdmin = { id: string; username: string };
type Props = {
  onJoin: (session: Session) => void;
  onBack: () => void;
  initialUser?: Account | null;
  onAccountChange?: (user: Account | null) => void;
};
function CopyIcon() {
  useLocale();
  return <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><rect x="8" y="8" width="12" height="13" rx="2" /><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3" /></svg>;
}
function PlusIcon() {
  useLocale();
  return <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>;
}
export function Organizer({ onJoin, onBack, initialUser, onAccountChange }: Props) {
  useLocale();
  const languageAccount = useLanguageAccount();
  const [user, setUser] = useState<Account | null>(initialUser ?? null);
  const [rooms, setRooms] = useState<Room[]>([]);
  const [loading, setLoading] = useState(!!initialUser);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [name, setName] = useState('');
  const [mode, setMode] = useState<Room['mode']>('meeting');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [creating, setCreating] = useState(false);
  const [joining, setJoining] = useState(false);
  const [roomAccess, setRoomAccess] = useState('');
  const [filter, setFilter] = useState<'all' | 'own' | 'shared'>('all');
  const [query, setQuery] = useState('');
  const visibleRooms = rooms.filter(room => (filter === 'all' || (filter === 'own' ? room.accessRole === 'owner' : room.accessRole !== 'owner')) && room.name.toLocaleLowerCase().includes(query.toLocaleLowerCase()));
  const [selected, setSelected] = useState<Room | null>(null);
  const [historyRoom,setHistoryRoom]=useState<Room|null>(null);
  const [deleteRoom,setDeleteRoom]=useState<Room|null>(null);
  const [copied, setCopied] = useState('');
  const [admins, setAdmins] = useState<RoomAdmin[]>([]);
  const [adminsLoading, setAdminsLoading] = useState(false);
  const [adminInvite, setAdminInvite] = useState('');
  const ownsSelected = !!selected && (selected.accessRole === 'owner' || selected.ownerId === user?.id);

  useEffect(() => { if (initialUser !== undefined) setUser(initialUser); }, [initialUser]);
  useEffect(() => {
    if (initialUser !== undefined) return;
    let active = true;
    api<Account>('/api/account').then(account => { if (active) setUser(account); }).catch(() => {});
    return () => { active = false; };
  }, []);
  useEffect(() => {
    if (!user) { setRooms([]); setLoading(false); return; }
    let active = true;
    setLoading(true);
    api<Room[]>('/api/rooms').then(data => { if (active) setRooms(data); })
      .catch(cause => { if (active) setError(errorMessage(cause)); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [user?.id]);
  useEffect(() => {
    setAdmins([]); setAdminInvite('');
    if (!selected || !ownsSelected) { setAdminsLoading(false); return; }
    let active = true;
    setAdminsLoading(true);
    api<RoomAdmin[]>(`/api/rooms/${encodeURIComponent(selected.id)}/admins`).then(data => { if (active) setAdmins(data); })
      .catch(cause => { if (active) setError(errorMessage(cause)); }).finally(() => { if (active) setAdminsLoading(false); });
    return () => { active = false; };
  }, [selected?.id, ownsSelected]);
  useEffect(() => { if (!copied) return; const timer = setTimeout(() => setCopied(''), 2500); return () => clearTimeout(timer); }, [copied]);

  function updateAccount(account: Account | null) {
    setUser(account); languageAccount.setUser(account); onAccountChange?.(account);
    if (!account) { setRooms([]); setSelected(null); setCreating(false); setAdmins([]); setAdminInvite(''); setPassword(''); setUsername(''); }
  }
  async function run(action: () => Promise<void>) {
    setBusy(true); setError('');
    try { await action(); } catch (cause) { setError(errorMessage(cause)); } finally { setBusy(false); }
  }
  async function signIn(event: FormEvent) {
    event.preventDefault();
    await run(async () => { const account = await api<Account>('/api/account/login', '', { username, password }); setPassword(''); setUsername(''); updateAccount(account); });
  }
  function openCreate() { setName(''); setMode('meeting'); setError(''); setCreating(true); }
  function closeDialog() { setCreating(false); setSelected(null); setError(''); setCopied(''); setAdminInvite(''); }
  async function createRoom(event: FormEvent) {
    event.preventDefault();
    await run(async () => {
      const room = await api<Room>('/api/rooms', '', { name: name.trim(), mode });
      setRooms(current => [room, ...current.filter(item => item.id !== room.id)]);
      setCreating(false); setName(''); setFilter('all'); setQuery('');
    });
  }
  async function copy(value: string, key: string) {
    setError('');
    try { await navigator.clipboard.writeText(value); setCopied(key); }
    catch { setError('Could not copy automatically. Select and copy the ID or link from the Invite dialog.'); }
  }

  if (!user) return <main className="workspace workspace-auth">
    <button className="quiet workspace-back" onClick={onBack}>{tr("← Back")}</button>
    <section className="workspace-login-card"><div className="workspace-login-mark" aria-hidden="true">◈</div><div className="eyebrow">{tr("YOUR CHECKPOINT")}</div><h1>{tr("Welcome back.")}</h1><p>{tr("Sign in to create a room and bring your team together.")}</p>
      {error && <p role="alert">{systemError(error)}</p>}
      <form onSubmit={signIn}><label>{tr("Username")}<input autoComplete="username" value={username} onChange={event => setUsername(event.target.value)} required /></label><label>{tr("Password")}<input type="password" autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} required /></label><button disabled={busy} aria-busy={busy}>{tr("Sign in")}</button></form>
      <p className="hint">{tr("Have a room ID? You can join without an account.")}</p><button className="quiet workspace-join-link" onClick={onBack}>{tr("Join a room instead")}</button>
    </section>
  </main>;

  return <main className="workspace">
    <div className="workspace-intro"><div><h1>{tr("Your rooms")}</h1><p>{tr("A place for every conversation. Pick a room or make a new one.")}</p></div><div className="workspace-actions"><button className="secondary" onClick={() => { setError(''); setRoomAccess(''); setJoining(true); }} aria-haspopup="dialog"><CallIcon name="leave" />{tr("Join by ID")}</button><button onClick={openCreate} disabled={busy} aria-haspopup="dialog"><PlusIcon />{tr("Create a room")}</button></div></div>
    <div className="workspace-filters"><div className="room-filter-tabs">{(['all','own','shared'] as const).map(value => <button className={filter === value ? 'secondary selected' : 'quiet'} aria-pressed={filter === value} key={value} onClick={() => setFilter(value)}>{value === 'all' ? tr('All rooms') : value === 'own' ? tr('My rooms') : tr('Shared with me')}{value === 'all' && <span className="room-total">{rooms.length}</span>}</button>)}</div><input type="search" aria-label={tr('Find a room')} placeholder={tr('Find a room')} value={query} onChange={event => setQuery(event.target.value)} /></div>
    {error && !creating && !selected && !joining && <p role="alert">{systemError(error)}</p>}
    {loading && <p className="hint" role="status">{tr("Loading your rooms…")}</p>}
    <div className="room-grid workspace-room-grid" aria-label={tr("Your rooms")}>
      {visibleRooms.map(room => <article className={`room-card room-card-${room.mode}`} key={room.id}>
        <div className="room-card-top"><span className="room-card-icon"><CallIcon name={room.mode === 'webinar' ? 'screen' : 'people'} /></span><span className="room-mode">{room.mode === 'webinar' ? tr("Webinar") : tr("Meeting")}</span><span className="room-access">{room.accessRole === 'admin' ? tr("Room admin") : room.accessRole === 'shared' ? tr("Shared with me") : tr("Owned by you")}</span>{room.closed && <span className="room-closed">{room.deletionState === 'deleting' ? tr("Deleting room…") : tr("Closed")}</span>}</div>
        <h2>{room.name}</h2><p className="room-card-description">{room.mode === 'webinar' ? tr("A stage for your ideas. A place for your audience.") : tr("Your team, in the same conversation.")}</p>
        <div className="room-code-row"><div><span>{tr("Room ID")}</span><code>{room.id}</code></div><button className="quiet room-copy" aria-label={tr("Copy room ID")} title={copied === room.id ? tr("Copied") : tr("Copy room ID")} onClick={() => copy(room.id, room.id)}>{copied === room.id ? <CallIcon name="check" /> : <CopyIcon />}</button></div>
        <div className="room-card-actions"><button disabled={busy || room.closed} onClick={() => run(async () => onJoin(await api<Session>(`/api/rooms/${encodeURIComponent(room.id)}/join`, '', {})))}>{tr("Join room")} <span aria-hidden="true">→</span></button><button className="secondary" disabled={room.closed} onClick={() => { setError(''); setCopied(''); setSelected(room); }}>{tr("Invite")}</button></div>
        <div className="room-history-actions"><button className="quiet" disabled={!!room.deletionState} onClick={()=>setHistoryRoom(room)}>{tr("History")}</button>{room.accessRole==='owner' && <button className="quiet danger-text" disabled={!!room.deletionState} onClick={()=>setDeleteRoom(room)}>{tr("Delete room")}</button>}</div>
      </article>)}
    </div>
    {!loading && visibleRooms.length === 0 && <p className="room-empty">{rooms.length ? tr('No matching rooms.') : tr('Create your first room to start a conversation.')}</p>}
    {joining && <WorkspaceDialog title={tr('Join by ID')} description={tr('Enter a room ID or paste an invitation link.')} busy={busy} onClose={() => { setJoining(false); setError(''); }}><form className="workspace-dialog-form" onSubmit={event => { event.preventDefault(); void run(async () => { const credential = parseRoomAccess(roomAccess); const next = await api<Session>('/api/join', '', { name: user.username, [credential.kind]: credential.value }); setJoining(false); onJoin(next); }); }}><label>{tr('Room ID or link')}<input data-autofocus autoComplete="off" autoCapitalize="characters" spellCheck={false} value={roomAccess} onChange={event => setRoomAccess(event.target.value)} placeholder="XXXX-XXXX-XXXX-XXXX" required /></label>{error && <p role="alert">{systemError(error)}</p>}<div className="workspace-dialog-actions"><button type="button" className="secondary" disabled={busy} onClick={() => { setJoining(false); setError(''); }}>{tr('Cancel')}</button><button disabled={busy || !roomAccess.trim()}>{tr('Join room')}</button></div></form></WorkspaceDialog>}
    <div className="workspace-copy-feedback" role="status">{copied && tr("Copied to clipboard")}</div>
    {historyRoom && <RecordingHistory room={historyRoom.id} title={historyRoom.name} onClose={()=>setHistoryRoom(null)} />}
    {deleteRoom && <WorkspaceDialog title={tr("Delete room and all its contents?")} description={`${deleteRoom.name} · ${deleteRoom.id}`} busy={busy} onClose={()=>setDeleteRoom(null)}><p>{tr("This permanently deletes all recordings, transcripts, minutes, invitations and room access. Any active meeting will end. Accounts and other rooms are kept.")}</p><p>{tr("Copies already downloaded cannot be removed. Server backups expire under the operator’s retention policy.")}</p>{error && <p role="alert">{systemError(error)}</p>}<div className="workspace-dialog-actions"><button autoFocus className="secondary" disabled={busy} onClick={()=>setDeleteRoom(null)}>{tr("Cancel")}</button><button className="danger" disabled={busy} onClick={()=>run(async()=>{await api(`/api/rooms/${encodeURIComponent(deleteRoom.id)}/delete`,'',{confirmed:true});setRooms(current=>current.map(room=>room.id===deleteRoom.id?{...room,closed:true,deletionState:'deleting'}:room));setDeleteRoom(null);})}>{tr("Delete room permanently")}</button></div></WorkspaceDialog>}
    {creating && <WorkspaceDialog title={tr("Create a room")} description={tr("Give it a name. We’ll create a room ID you can share.")} busy={busy} onClose={closeDialog}>
      <form className="workspace-dialog-form" onSubmit={createRoom}><label>{tr("Room name")}<input value={name} onChange={event => setName(event.target.value)} maxLength={100} data-autofocus required placeholder={tr("e.g. Product team")} /></label><fieldset className="room-mode-options"><legend>{tr('Mode')}</legend>{(['meeting','webinar'] as const).map(value => <label key={value}><input type="radio" name="room-mode" value={value} checked={mode === value} onChange={() => setMode(value)} /><CallIcon name={value === 'meeting' ? 'people' : 'screen'} /><span><strong>{value === 'meeting' ? tr('Meeting') : tr('Webinar')}</strong><small>{value === 'meeting' ? tr('Everyone can use their microphone, camera and screen.') : tr('You present. Guests watch and can ask to speak.')}</small></span></label>)}</fieldset>
        <p className="hint workspace-mode-hint">{tr("The room ID is assigned automatically. You will be the owner.")}</p>{error && <p role="alert">{systemError(error)}</p>}
        <div className="workspace-dialog-actions"><button type="button" className="secondary" disabled={busy} onClick={closeDialog}>{tr("Cancel")}</button><button disabled={busy || !name.trim()} aria-busy={busy}>{tr("Create room")}</button></div>
      </form>
    </WorkspaceDialog>}
    {selected && <WorkspaceDialog title={tr("Invite to {{name}}", {name: selected.name})} description={tr("Share the link or room ID. Anyone admitted can join and access all saved recordings and minutes, including earlier meetings.")} busy={busy} onClose={closeDialog}>
      <div className="workspace-share-fields"><label>{tr("Room ID")}<input readOnly value={selected.id} onFocus={event => event.target.select()} autoFocus /></label><button className="secondary" onClick={() => copy(selected.id, selected.id)}><CopyIcon />{tr("Copy room ID")}</button>
        <label>{tr("Room link")}<input readOnly value={`${location.origin}/#room=${encodeURIComponent(selected.id)}`} onFocus={event => event.target.select()} /></label><button onClick={() => copy(`${location.origin}/#room=${encodeURIComponent(selected.id)}`, 'link')}><CopyIcon />{tr("Copy link")}</button>
      </div><p className="workspace-share-feedback" role="status">{copied === 'link' ? tr("Room link copied") : copied === selected.id ? tr("Room ID copied") : tr("Guests choose their name before joining.")}</p>
      {ownsSelected && <section className="room-admins"><h3>{tr("Room admins")}</h3><p>{tr("Invite someone you trust to help host this room. They’ll sign in with their own account.")}</p>
        {adminsLoading ? <p className="hint" role="status">{tr("Loading room admins…")}</p> : admins.length === 0 ? <p className="hint">{tr("You’re the only person managing this room.")}</p> : <ul>{admins.map(admin => <li key={admin.id}><span>{admin.username}</span><button className="quiet danger-text" aria-label={tr("Remove {{name}} as room admin", {name: admin.username})} disabled={busy} onClick={() => {
          if (confirm(tr("Remove {{name}} as a room admin?", {name: admin.username}))) void run(async () => { await api(`/api/rooms/${encodeURIComponent(selected.id)}/admins/${encodeURIComponent(admin.id)}/remove`, '', {}); setAdmins(current => current.filter(item => item.id !== admin.id)); });
        }}>{tr("Remove")}</button></li>)}</ul>}
        <button className="secondary room-admin-create" disabled={busy} onClick={() => run(async () => { const result = await api<{ url: string }>(`/api/rooms/${encodeURIComponent(selected.id)}/admin-invitations`, '', {}); setAdminInvite(result.url); })}>{tr("Create admin invitation")}</button>
        {adminInvite && <div className="room-admin-invitation"><label>{tr("Admin invitation link")}<input readOnly value={adminInvite} onFocus={event => event.target.select()} /></label><button className="secondary" onClick={() => copy(adminInvite, 'admin-invite')}><CopyIcon />{tr("Copy admin invitation")}</button><p className="hint" role="status">{copied === 'admin-invite' ? tr("Admin invitation copied") : tr("One person can accept this invitation within 24 hours.")}</p></div>}
      </section>}
      {error && <p role="alert">{systemError(error)}</p>}
    </WorkspaceDialog>}
  </main>;
}
