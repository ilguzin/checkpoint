import i18next from 'i18next';
import { initReactI18next, useTranslation } from 'react-i18next';
import en from './locales/en.json';
import ru from './locales/ru.json';
import source from './locales/source.json';

export type Preference = 'auto' | 'en' | 'ru';
export const validPreference = (value: unknown): Preference => value === 'en' || value === 'ru' ? value : 'auto';
export function resolveLocale(preference: Preference, languages: readonly string[]): 'en' | 'ru' {
  if (preference !== 'auto') return preference;
  for (const value of languages) { const base = value.toLowerCase().split('-')[0]; if (base === 'en' || base === 'ru') return base; }
  return 'en';
}
const storageKey = 'checkpoint.uiLocale';
export function guestPreference(): Preference { try { return validPreference(localStorage.getItem(storageKey)); } catch { return 'auto'; } }
let preference = guestPreference();
const browserLanguages = () => navigator.languages?.length ? navigator.languages : [navigator.language || 'en'];
void i18next.use(initReactI18next).init({
  resources: { en: { translation: en }, ru: { translation: ru } },
  lng: resolveLocale(preference, browserLanguages()), fallbackLng: 'en',
  initAsync: false, interpolation: { escapeValue: false }, returnNull: false, react: { useSuspense: false },
});
export const locale = () => i18next.resolvedLanguage === 'ru' ? 'ru' : 'en';
export function useLocale() { return useTranslation(); }
export function tr(english: string, values?: Record<string, unknown>): string {
  const key = (source as Record<string, string>)[english];
  return key ? String(i18next.t(key, { ...values, defaultValue: english })) : english;
}
export const t = (key: string, values?: Record<string, unknown>) => String(i18next.t(key, { ...values, defaultValue: '…' }));
function updateDocument() {
  document.documentElement.lang = locale();
  document.title = document.documentElement.dataset.app === 'observe' ? 'Checkpoint · ' + tr('Service health') : location.pathname === '/licenses' ? 'Checkpoint · ' + tr('Open source licenses') : 'Checkpoint';
}
i18next.on('languageChanged', updateDocument);
updateDocument();
export function applyPreference(value: Preference, guest = false) {
  preference = value;
  if (guest) { try { localStorage.setItem(storageKey, value); } catch { /* Private browsing still permits a session-only choice. */ } }
  void i18next.changeLanguage(resolveLocale(value, browserLanguages()));
}
window.addEventListener('languagechange', () => { if (preference === 'auto') applyPreference('auto'); });
export function systemError(message: string) {
  if (!message) return '';
  if ((source as Record<string, string>)[message]) return tr(message);
  return tr('Something went wrong. Please try again.');
}
export default i18next;
