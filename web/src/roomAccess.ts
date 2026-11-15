export type RoomAccess = { value: string; kind: 'roomId' | 'invite' | 'access' };

/** Room credentials live in fragments, so browsers don't send them in URLs or referrers. */
export function accessFromHash(hash: string): RoomAccess | null {
  const params = new URLSearchParams(hash.replace(/^#/, ''));
  const room = params.get('room'), invite = params.get('invite');
  if (room && invite) throw new Error('Use a room link or an invitation link, not both.');
  if (room?.trim()) return { value: room.trim(), kind: 'roomId' };
  if (invite?.trim()) return { value: invite.trim(), kind: 'invite' };
  return null;
}

export function parseRoomAccess(value: string): RoomAccess {
  const input = value.trim();
  if (!input) throw new Error('Enter a room ID or paste a room link.');
  if (/^https?:\/\//i.test(input)) {
    let url: URL;
    try { url = new URL(input); } catch { throw new Error('This room link is not valid.'); }
    if (url.origin !== location.origin) throw new Error('This room belongs to another server. Open its link to join there.');
    const access = accessFromHash(url.hash);
    if (!access) throw new Error('This link does not contain a room ID or invitation.');
    return access;
  }
  if (input.startsWith('#')) {
    const access = accessFromHash(input);
    if (access) return access;
    throw new Error('This link does not contain a room ID or invitation.');
  }
  return { value: input, kind: 'access' };
}
