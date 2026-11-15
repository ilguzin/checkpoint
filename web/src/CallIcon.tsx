import type { ReactNode } from 'react';
type Name = 'history' | 'settings' | 'screen' | 'hand' | 'people' | 'chat' | 'questions' | 'more' | 'leave' | 'manage' | 'info' | 'audio' | 'audioOff' | 'close' | 'microphone' | 'camera' | 'send' | 'check' | 'down';
const shapes: Record<Name, ReactNode> = {
  history: <><path d="M3 10a9 9 0 1 1 1.8 8M3 4v6h6M12 7v5l3 2" /></>,
  settings: <><path d="M3 7h3m6 0h9M3 17h9m6 0h3" /><circle cx="9" cy="7" r="3" /><circle cx="15" cy="17" r="3" /></>,
  close: <path d="m6 6 12 12M18 6 6 18" />,
  microphone: <><rect x="9" y="2" width="6" height="13" rx="3" /><path d="M5 10v2a7 7 0 0 0 14 0v-2M12 19v3m-4 0h8" /></>,
  camera: <><rect x="2" y="5" width="14" height="14" rx="3" /><path d="m16 9 6-3v12l-6-3" /></>,
  send: <path d="m22 2-7 20-4-9-9-4Zm0 0L11 13" />,
  check: <path d="m5 12 4 4L19 6" />,
  down: <path d="M12 4v16m-6-6 6 6 6-6" />,
  screen: <><rect x="3" y="4" width="18" height="13" rx="2" /><path d="M8 21h8M12 17v4m-3-11 3-3 3 3m-3-3v7" /></>,
  hand: <path d="M8 12V6a1.5 1.5 0 0 1 3 0v5-7a1.5 1.5 0 0 1 3 0v7-5a1.5 1.5 0 0 1 3 0v6-3a1.5 1.5 0 0 1 3 0v6a7 7 0 0 1-13 3l-3-5a1.5 1.5 0 0 1 2.5-1.6L8 14" />,
  people: <><circle cx="9" cy="8" r="3" /><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 5a3 3 0 0 1 0 6m2 3a5 5 0 0 1 3 5v2" /></>,
  chat: <path d="M21 15a3 3 0 0 1-3 3H9l-6 4V6a3 3 0 0 1 3-3h12a3 3 0 0 1 3 3ZM7 8h10M7 12h7" />,
  questions: <><circle cx="12" cy="12" r="9" /><path d="M9.5 9a2.5 2.5 0 1 1 4.2 1.8c-1.2.8-1.7 1.2-1.7 2.7M12 17h.01" /></>,
  more: <><circle cx="5" cy="12" r="1" /><circle cx="12" cy="12" r="1" /><circle cx="19" cy="12" r="1" /></>,
  leave: <><path d="M9 4H5a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h4M10 12h11m-4-4 4 4-4 4" /></>,
  manage: <><path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6ZM9 12l2 2 4-4" /></>,
  audio: <><path d="m11 5-5 4H3v6h3l5 4Z" /><path d="M15 8a6 6 0 0 1 0 8m3-11a10 10 0 0 1 0 14" /></>,
  audioOff: <><path d="m11 5-5 4H3v6h3l5 4Z" /><path d="m16 9 6 6m0-6-6 6" /></>,
  info: <><circle cx="12" cy="12" r="9" /><path d="M12 11v6M12 7h.01" /></>,
};
export function CallIcon({ name }: { name: Name }) {
  return <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">{shapes[name]}</svg>;
}
