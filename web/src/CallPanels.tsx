import { tr, useLocale, locale } from './i18n';
import { useLayoutEffect, useRef, useState } from 'react';
import { CallIcon } from './CallIcon';

export type Member = { identity: string; name: string; role: string; moderator: boolean; canPublish: boolean; admitted: boolean; handAt: number };
export type Message = { id: number; identity: string; name: string; body: string; kind: string; created: number; answered: boolean };
export type Meeting = { members: Member[]; messages: Message[]; locked: boolean; waitingRoom: boolean; screenOwner: string; name: string; mode: string };
export type CallPanel = 'people' | 'chat' | 'questions' | 'settings';

export function groupMembers(members: Member[]) {
  const hands = members.filter(member => member.admitted && member.handAt > 0).sort((a, b) => a.handAt - b.handAt || a.identity.localeCompare(b.identity));
  const rest = members.filter(member => member.handAt <= 0 || !member.admitted);
  return [
    { key: 'hands', label: tr('Raised hands'), members: hands },
    { key: 'owners', label: tr('Owner'), members: rest.filter(member => member.admitted && member.role === 'owner') },
    { key: 'admins', label: tr('Room admins'), members: rest.filter(member => member.admitted && member.role !== 'owner' && member.moderator) },
    { key: 'participants', label: tr('Participants'), members: rest.filter(member => member.admitted && member.role !== 'owner' && !member.moderator) },
    { key: 'waiting', label: tr('Waiting to join'), members: rest.filter(member => !member.admitted) },
  ].filter(group => group.members.length > 0);
}

export function Avatar({ name }: { name: string }) {
  useLocale();
  const initials = name.trim().split(/\s+/).slice(0, 2).map(word => Array.from(word)[0] || '').join('').toLocaleUpperCase();
  return <span className="person-avatar" aria-hidden="true">{initials || '·'}</span>;
}

export function MessagePanel({ kind, messages, identity, draft, onDraft, onSend, onAnswer, busy, moderator, loading }: {
  kind: 'chat' | 'questions'; messages: Message[]; identity: string; draft: string;
  onDraft: (value: string) => void; onSend: () => void; onAnswer: (id: number) => void;
  busy: boolean; moderator: boolean; loading: boolean;
}) {
  useLocale();
  const list = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  const [hasNew, setHasNew] = useState(false);
  const lastID = messages.at(-1)?.id;
  function goToLatest() { const element = list.current; if (element) element.scrollTop = element.scrollHeight; follow.current = true; setHasNew(false); }
  useLayoutEffect(() => {
    if (follow.current) goToLatest();
    else setHasNew(true);
  }, [lastID]);
  return <>
    <div className="message-feed">
      <div ref={list} className="message-list" role="log" aria-label={kind === 'chat' ? tr("Meeting messages") : tr("Meeting questions")} aria-live="polite" aria-relevant="additions" onScroll={() => {
        const element = list.current!; follow.current = element.scrollHeight - element.scrollTop - element.clientHeight < 64;
        if (follow.current) setHasNew(false);
      }}>
        {messages.length === 0 && <div className="panel-empty"><span className="empty-icon"><CallIcon name={kind === 'chat' ? 'chat' : 'questions'} /></span><strong>{loading ? tr("Getting things ready…") : kind === 'chat' ? tr("Start the conversation") : tr("Make room for questions")}</strong><p>{kind === 'chat' ? tr("Share a thought or a useful link with everyone here.") : tr("Ask a question for the host and presenters.")}</p></div>}
        {messages.map(message => <article className={`chat-message ${message.identity === identity ? 'own-message' : ''}`} key={message.id}>
          <div className="message-meta"><strong>{message.name}</strong><time dateTime={new Date(message.created * 1000).toISOString()} title={new Date(message.created * 1000).toLocaleString(locale())}>{new Date(message.created * 1000).toLocaleTimeString(locale(), { hour: '2-digit', minute: '2-digit' })}</time></div>
          <p>{message.body}</p>
          {message.kind === 'question' && (message.answered ? <span className="answered-badge"><CallIcon name="check" />{tr("Answered")}</span> : moderator && <button className="quiet answer-button" disabled={busy} onClick={() => onAnswer(message.id)}>{tr("Mark answered")}</button>)}
        </article>)}
      </div>
      {hasNew && <button className="new-messages secondary" onClick={goToLatest}><CallIcon name="down" />{tr("New messages")}</button>}
    </div>
    <form className="message-composer" onSubmit={event => { event.preventDefault(); if (!busy && draft.trim()) { follow.current = true; onSend(); } }}>
      <div className="compose-field">
        <textarea aria-label={kind === 'chat' ? tr("Message") : tr("Your question")} placeholder={kind === 'chat' ? tr("Message everyone…") : tr("Ask a question…")} value={draft} maxLength={2000} rows={2} required onChange={event => onDraft(event.target.value)} onKeyDown={event => {
          if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); }
        }} />
        <button type="submit" aria-label={tr("Send")} title={tr("Send · Enter")} disabled={busy || !draft.trim()}><CallIcon name="send" /></button>
      </div>
      <p className="composer-hint">{tr("Everyone can see this · Cleared after the meeting or 24h")}</p>
    </form>
  </>;
}
