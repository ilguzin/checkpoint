import { LanguageSettings } from './LanguageSettings';
import { t, tr, useLocale, locale, systemError } from './i18n';
import { useEffect, useState } from 'react';
import './service-pages.css';
type Entry = { name: string; version: string; licenses: { file: string; text: string }[] };
export default function LicensesPage() {
  useLocale();
  const [entries, setEntries] = useState<Entry[]>([]);
  const [error, setError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [filter, setFilter] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/licenses', { signal: controller.signal }).then(r => { if (!r.ok) throw new Error(); return r.json(); }).then(setEntries).catch(() => { if (!controller.signal.aborted) setError(true); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, []);
  const matches = entries.filter(e => e.name.toLowerCase().includes(filter.trim().toLowerCase()));
  return <div className="app licenses"><header className="service-header"><a className="brand" href="/">◈ Checkpoint</a><a className="service-back" href="/">{tr("Back to Checkpoint")}</a><>{document.documentElement.dataset.app === "observe" ? <LanguageSettings /> : <a href="/settings">{tr("User settings")}</a>}</></header>
    <main className="license-page"><div className="eyebrow">{tr("BUILT TOGETHER")}</div><h1>{tr("Open source licenses")}</h1><p>{tr("Checkpoint is licensed under Apache 2.0. We build on the work of these open source projects.")}</p>
      <div className="license-search"><label>{tr("Find a project")}<input value={filter} onChange={e => setFilter(e.target.value)} type="search" placeholder={tr("Search projects…")} /></label>{!loading && !error && <span className="hint" role="status">{t("projects", {count: matches.length})}</span>}</div>
      {error && <p role="alert">{tr("Licenses could not be loaded. Please try again.")}</p>}
      {loading && <p role="status">{tr("Loading licenses…")}</p>}
      {!loading && !error && matches.length === 0 && <div className="service-empty"><h2>{tr("No matching projects")}</h2><p>{tr("Try another name or clear your search.")}</p>{filter && <button className="secondary" onClick={() => setFilter('')}>{tr("Clear search")}</button>}</div>}
      <div className="license-list">{matches.map(entry => <details key={entry.name + entry.version}><summary><span>{entry.name}</span> <small>{entry.version}</small></summary>{entry.licenses.map(license => <section key={license.file}><h2>{license.file}</h2><pre>{license.text}</pre></section>)}</details>)}</div>
    </main>
  </div>;
}
