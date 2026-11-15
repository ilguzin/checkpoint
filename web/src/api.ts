import errors from './locales/errors.json';
export type Session = { session: string; token: string; identity: string; room: string; name: string; role: string; roomName: string; mode: string; waiting: boolean; moderator: boolean; canPublish: boolean; serverURL: string };
export async function api<T>(path: string, secret = '', body?: unknown): Promise<T> {
  const response = await fetch(path, { method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin', headers: { ...(secret ? { Authorization: `Bearer ${secret}` } : {}), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(8000) });
  const data = await response.json();
  if (!response.ok) throw new Error(data.code ? (errors as Record<string,string>)[data.code] || 'Request failed' : 'Service unavailable');
  return data;
}
export const errorMessage = (error: unknown) => error instanceof Error ? error.message : 'Connection failed';
