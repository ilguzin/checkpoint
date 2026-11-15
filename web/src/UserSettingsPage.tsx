import { useEffect, useRef, useState, type ReactNode, type FormEvent } from 'react';
import { LanguageSettings } from './LanguageSettings';
import { tr, useLocale, systemError } from './i18n';
import { api, errorMessage } from './api';
import type { Account } from './Organizer';
import { LoadingScreen } from './LoadingScreen';
import './user-settings.css';

export function UserSettingsPage({ header, footer, account, loading, inCall, onBack, onPasswordChanged }: {
  header: ReactNode; footer: ReactNode; account: Account | null; loading: boolean; inCall: boolean; onBack: () => void; onPasswordChanged: () => void;
}) {
  useLocale();
  const heading = useRef<HTMLHeadingElement>(null);
  const [current, setCurrent] = useState(''), [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false), [error, setError] = useState('');
  useEffect(() => { heading.current?.focus(); }, [loading]);
  async function changePassword(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('');
    try { await api('/api/account/password', '', { current, password }); setCurrent(''); setPassword(''); onPasswordChanged(); }
    catch (cause) { setError(errorMessage(cause)); }
    finally { setBusy(false); }
  }
  return <div className="app app-shell user-settings-page">{header}
    {loading ? <LoadingScreen /> : <main className="user-settings-main"><button className="quiet" onClick={onBack}>{inCall ? tr('Back to meeting') : tr('← Back')}</button><h1 ref={heading} tabIndex={-1}>{tr('User settings')}</h1>
      {inCall && <p className="notice">{tr('Your meeting continues. Your microphone and camera keep their current state.')}</p>}
      <section className="user-settings-card"><h2>{tr('Interface language')}</h2><LanguageSettings /></section>
      <section className="user-settings-card"><h2>{tr('Change password')}</h2>{account ? <>
        <p className="hint">{tr('Changing your password signs you out on every device and ends your current meeting connection.')}</p>
        {error && <p role="alert">{systemError(error)}</p>}
        <form onSubmit={changePassword}><label>{tr('Current password')}<input type="password" autoComplete="current-password" value={current} onChange={event => setCurrent(event.target.value)} required /></label><label>{tr('New password')}<input type="password" autoComplete="new-password" minLength={12} maxLength={72} value={password} onChange={event => setPassword(event.target.value)} required /></label><button disabled={busy} aria-busy={busy}>{tr('Change and sign out everywhere')}</button></form>
      </> : <p>{tr('Sign in to your account to change your password.')}</p>}</section>
    </main>}
    {footer}
  </div>;
}
