import { tr, useLocale, locale, systemError } from './i18n';
/** Keep the boot markup in index.html in sync: it appears before JavaScript loads. */
export function LoadingScreen({ label = 'Loading Checkpoint' }: { label?: string }) {
  useLocale();
  return <main className="startup-loader" role="status" aria-label={tr(label)}>
    <div className="startup-content" aria-hidden="true">
      <div className="startup-mark"><span className="startup-ring" /><span className="startup-ring startup-ring-delayed" /><span className="startup-core" /></div>
      <div className="startup-name">Checkpoint</div>
      <div className="startup-tagline">Sync up. Move forward.</div>
      <div className="startup-progress"><span /></div>
    </div>
  </main>;
}
