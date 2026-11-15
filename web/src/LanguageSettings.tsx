import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { api } from './api';
import { applyPreference, guestPreference, tr, useLocale, validPreference, type Preference } from './i18n';
type User = { id: string; uiLocale?: Preference };
const Context = createContext<{ user: User | null; setUser: (user: User | null) => void }>({ user: null, setUser: () => {} });
export function LanguageProvider({ children }: { children: ReactNode }) {
  const [user, saveUser] = useState<User | null>(null);
  function setUser(next: User | null) { saveUser(next); applyPreference(next ? validPreference(next.uiLocale) : guestPreference()); }
  return <Context.Provider value={{ user, setUser }}>{children}</Context.Provider>;
}
export const useLanguageAccount = () => useContext(Context);
export function LanguageSettings() {
  useLocale();
  const { user, setUser } = useLanguageAccount();
  const [choice, setChoice] = useState<Preference>(() => user ? validPreference(user.uiLocale) : guestPreference());
  const [busy, setBusy] = useState(false), [error, setError] = useState('');
  useEffect(() => { setChoice(user ? validPreference(user.uiLocale) : guestPreference()); }, [user?.id, user?.uiLocale]);
  async function change(value: Preference) {
    setChoice(value); applyPreference(value, !user); setError('');
    if (!user) return;
    setBusy(true);
    try { const updated = await api<User>('/api/account/preferences', '', { uiLocale: value }); setUser(updated); }
    catch { setError('Language changed on this device, but could not be saved to your account. Try again.'); }
    finally { setBusy(false); }
  }
  return <div className="language-settings"><label>{tr('Interface language')}<select aria-label={tr('Interface language')} value={choice} disabled={busy} onChange={event => void change(validPreference(event.target.value))}><option value="auto">{tr('Automatic')}</option><option value="en">English</option><option value="ru">Русский</option></select></label>{error && <p role="alert">{tr(error)}</p>}</div>;
}
