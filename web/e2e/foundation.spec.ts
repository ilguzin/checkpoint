import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
const enabled = process.env.CHECKPOINT_NATIVE_TEST === '1';
const cfg = enabled ? JSON.parse(readFileSync(process.env.CHECKPOINT_NATIVE_ACCESS || '../.dev/native/access.json', 'utf8')) : {};
test.skip(!enabled || !cfg.adminPassword, 'Requires the isolated native administrator fixture');

test('organizer creates a webinar, invites a guest, promotes and revokes access', async ({ browser, page }) => {
  await page.goto(cfg.publicURL);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.getByLabel('Username', { exact: true }).fill('admin');
  await page.getByLabel('Password', { exact: true }).fill(cfg.adminPassword);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Your rooms', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Create a room', exact: true }).click();
  await page.getByLabel('Room name').fill('Foundation webinar');
  await page.getByRole('radio', { name: /^Webinar/ }).check();
  await page.getByRole('button', { name: 'Create room', exact: true }).click();
  const card = page.locator('article').filter({ has: page.getByRole('heading', { name: 'Foundation webinar' }) });
  await expect(card).toBeVisible();
  // Keep legacy invitation revocation coverage while the basic UI shares room IDs.
  const invitation = await page.evaluate(async () => {
    const rooms = await (await fetch('/api/rooms')).json();
    const room = rooms.find((item: { name: string }) => item.name === 'Foundation webinar');
    const result = await fetch(`/api/rooms/${room.id}/invitations`, { method: 'POST', body: JSON.stringify({ role: 'attendee', hours: 24, maxUses: 1 }) });
    if (!result.ok) throw new Error('Could not create legacy test invitation');
    const invite = await result.json();
    return { url: invite.url, revokePath: `/api/rooms/${room.id}/invitations/${invite.invitation.id}/revoke` };
  });
  const link = invitation.url;
  const guestContext = await browser.newContext({ ignoreHTTPSErrors: true });
  const guest = await guestContext.newPage();
  try {
    await guest.goto(link);
    await guest.getByLabel('Your name').fill('Foundation guest');
    await guest.getByRole('button', { name: 'Join session' }).click();
    await expect(guest.locator('.session-controls [role=status]')).toContainText('connected');
    await guest.getByRole('button', { name: 'Meeting details' }).click();
    await expect(guest.getByText('You are watching. A host can invite you to speak.')).toBeVisible();
    const revokePath = invitation.revokePath;
    await card.getByRole('button', { name: 'Join room', exact: true }).click();
    await expect(page.locator('.session-controls [role=status]')).toContainText('connected');
    await page.getByRole('button', { name: /^People \(/ }).click();
    await page.locator('summary[aria-label="Actions for Foundation guest"]').click();
    await page.getByRole('button', { name: 'Invite to speak', exact: true }).click();
    await expect(guest.getByText('You can speak and share your screen. Use the controls below to turn on your devices.')).toBeVisible();
    if (!(await page.getByRole('button', { name: 'Move to audience', exact: true }).isVisible())) await page.locator('summary[aria-label="Actions for Foundation guest"]').click();
    await page.getByRole('button', { name: 'Move to audience', exact: true }).click();
    await expect(guest.getByText('You are watching. A host can invite you to speak.')).toBeVisible();
    const code = await page.evaluate(async path => (await fetch(path, { method: 'POST', body: '{}' })).status, revokePath);
    expect(code).toBe(200);
    await expect(guest.getByRole('heading', { name: 'Connection interrupted' })).toBeVisible();
    await guest.getByRole('button', { name: 'Rejoin session', exact: true }).click();
    await expect(guest.getByRole('alert')).toContainText('Session expired or access revoked');
  } finally { await guestContext.close(); }
});

test('waiting room, chat, hands, questions and a fresh meeting after ending', async ({ browser, page }) => {
  await page.goto(cfg.publicURL);
  const auth = await page.evaluate(async password => {
    const res = await fetch('/api/account/login', { method: 'POST', body: JSON.stringify({ username: 'admin', password }) });
    return res.status;
  }, cfg.adminPassword);
  expect(auth).toBe(200);
  const setup = await page.evaluate(async () => {
    const post = async (path: string, data: unknown) => (await fetch(path, { method: 'POST', body: JSON.stringify(data) })).json();
    const room = await post('/api/rooms', { name: 'Lifecycle webinar', mode: 'webinar' });
    const invite = await post(`/api/rooms/${room.id}/invitations`, { role: 'attendee', hours: 24, maxUses: 5 });
    return { room, invite };
  });
  await page.goto(cfg.publicURL + '/rooms');
  await expect(page.getByRole('heading', { name: 'Your rooms', exact: true })).toBeVisible();
  const card = page.locator('article').filter({ has: page.getByRole('heading', { name: 'Lifecycle webinar' }) });
  await card.getByRole('button', { name: 'Join room', exact: true }).click();
  await expect(page.locator('.session-controls [role=status]')).toContainText('connected');
  await page.getByRole('button', { name: /^People \(/ }).click();
  await page.getByLabel('Waiting room for new guests').check();
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true }); const guest = await ctx.newPage();
  try {
    await guest.goto(setup.invite.url);
    await guest.getByLabel('Your name').fill('Waiting guest');
    await guest.getByRole('button', { name: 'Join session' }).click();
    await expect(guest.getByRole('heading', { name: 'Your host will let you in.' })).toBeVisible();
    await page.locator('summary[aria-label="Actions for Waiting guest"]').click();
    await page.getByRole('button', { name: 'Admit', exact: true }).click();
    await expect(guest.locator('.session-controls [role=status]')).toContainText('connected');
    await guest.getByRole('button', { name: 'Raise hand', exact: true }).click();
    await expect(page.locator('.people-list li').filter({ hasText: 'Waiting guest' }).getByLabel('Hand raised', { exact: true })).toBeVisible();
    await guest.getByRole('button', { name: 'Chat', exact: true }).click();
    await guest.getByLabel('Message', { exact: true }).fill('<b>Text stays text</b>');
    await guest.getByRole('button', { name: 'Send', exact: true }).click();
    await page.getByRole('button', { name: 'Chat', exact: true }).click();
    await expect(page.getByText('<b>Text stays text</b>', { exact: true })).toBeVisible();
    expect(await page.locator('.message-list b').count()).toBe(0);
    await guest.getByRole('button', { name: 'Questions', exact: true }).click();
    await guest.getByLabel('Your question').fill('How does reconnect work?');
    await guest.getByRole('button', { name: 'Send', exact: true }).click();
    await page.getByRole('button', { name: 'Questions', exact: true }).click();
    await page.getByRole('button', { name: 'Mark answered', exact: true }).click();
    await expect(guest.getByText('Answered', { exact: true })).toBeVisible();
    await guest.reload();
    await expect(guest.locator('.session-controls [role=status]')).toContainText('connected');
    await expect(guest.locator('button[data-lk-source="microphone"]')).toHaveCount(0);
    page.once('dialog', dialog => dialog.accept());
    await page.getByRole('button', { name: /^People \(/ }).click();
    await page.getByRole('button', { name: 'End meeting for everyone', exact: true }).click();
    await expect(guest.getByRole('heading', { name: 'Connection interrupted' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Your rooms', exact: true })).toBeVisible();
    await page.locator('article').filter({ has: page.getByRole('heading', { name: 'Lifecycle webinar' }) }).getByRole('button', { name: 'Join room', exact: true }).click();
    await expect(page.locator('.session-controls [role=status]')).toContainText('connected');
    await page.getByRole('button', { name: 'Chat', exact: true }).click();
    await expect(page.locator('.message-list article')).toHaveCount(0);
  } finally { await ctx.close(); }
});
