import type { ReactNode } from 'react';
import type { Account } from './Organizer';
import { CallIcon } from './CallIcon';
import { tr } from './i18n';
import './app-shell.css';

export function AccountActions({ account, busy, hideSignIn, locked, onSettings, onSignOut, onSignIn }: {
  account: Account | null; busy: boolean; hideSignIn: boolean; locked: boolean; onSettings: () => void; onSignOut: () => void; onSignIn: () => void;
}) {
  return <div className="account-actions">
    {account ? <span className="account-name" title={account.username}>{account.username}</span> : !locked && !hideSignIn && <button className="quiet account-sign-in" onClick={onSignIn}>{tr('Sign in')}</button>}
    {!locked && <button className="quiet account-action" title={tr('User settings')} aria-label={tr('User settings')} onClick={onSettings}><CallIcon name="settings" /></button>}
    {account && !locked && <button className="quiet account-action" title={tr('Sign out')} aria-label={tr('Sign out')} disabled={busy} onClick={onSignOut}><CallIcon name="leave" /></button>}
  </div>;
}

export function AppHeader({ children, onHome }: { children: ReactNode; onHome: () => void }) {
  return <header className="app-header"><a className="brand" href="/" onClick={event => { if (!event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && event.button === 0) { event.preventDefault(); onHome(); } }}><span className="app-brand-mark" aria-hidden="true"><CallIcon name="check" /></span>Checkpoint</a>{children}</header>;
}

export function AppFooter({ healthURL }: { healthURL: string }) {
  return <footer className="app-footer">{healthURL && <a href={healthURL} target="_blank" rel="noreferrer">{tr('Service health ↗')}</a>}<a href="/licenses">{tr('Open source licenses')}</a></footer>;
}
