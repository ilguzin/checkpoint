import { test, expect, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';

const enabled = process.env.CHECKPOINT_NATIVE_TEST === '1';
const cfg = enabled ? JSON.parse(readFileSync(process.env.CHECKPOINT_NATIVE_ACCESS || '../.dev/native/access.json', 'utf8')) : {};
test.skip(!enabled || !cfg.adminPassword || !cfg.secondPassword, 'Requires isolated native accounts');
const roomIDPattern = /^[0-9A-HJKMNP-TV-Z]{4}(?:-[0-9A-HJKMNP-TV-Z]{4}){3}$/;

async function signIn(page: Page, username: string, password: string) {
  await page.goto(cfg.publicURL);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.getByLabel('Username', { exact: true }).fill(username);
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Your rooms', exact: true })).toBeVisible();
}
function roomCard(page: Page, name: string) {
  return page.getByRole('article').filter({ has: page.getByRole('heading', { name, exact: true }) });
}
async function createRoom(page: Page, name: string, mode = 'meeting') {
  await page.getByRole('button', { name: 'Create a room', exact: true }).click();
  const modal = page.getByRole('dialog', { name: 'Create a room', exact: true });
  await expect(modal.getByRole('radio', { name: /^Meeting/ })).toBeChecked();
  await modal.getByLabel('Room name', { exact: true }).fill(name);
  await modal.getByRole('radio', { name: mode === 'webinar' ? /^Webinar/ : /^Meeting/ }).check();
  await modal.getByRole('button', { name: 'Create room', exact: true }).click();
  await expect(modal).toHaveCount(0);
  const card = roomCard(page, name);
  await expect(card).toBeVisible();
  const id = (await card.locator('code').textContent())!.trim();
  expect(id).toMatch(roomIDPattern);
  await expect(card.getByText('Owned by you', { exact: true })).toBeVisible();
  return id;
}
async function leave(page: Page, signedIn = false) {
  await page.getByRole('button', { name: 'Leave', exact: true }).click();
  await expect(page.getByRole('heading', { name: signedIn ? 'Your rooms' : 'Join a room', exact: true })).toBeVisible();
}

test('room IDs and links join as guests while signed-in workspaces stay private and restore on reload', async ({ browser, page }) => {
  const otherContext = await browser.newContext();
  const guestContext = await browser.newContext();
  const other = await otherContext.newPage(), guest = await guestContext.newPage();
  try {
    await signIn(page, cfg.adminUsername || 'admin', cfg.adminPassword);
    const id = await createRoom(page, 'ID and link meeting');
    await signIn(other, cfg.secondUsername, cfg.secondPassword);
    await createRoom(other, 'Alex private meeting');
    await expect(roomCard(other, 'ID and link meeting')).toHaveCount(0);
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Your rooms', exact: true })).toBeVisible();
    await expect(roomCard(page, 'ID and link meeting')).toBeVisible();
    await expect(roomCard(page, 'Alex private meeting')).toHaveCount(0);
    const listed = await page.evaluate(async () => (await fetch('/api/rooms')).json());
    expect(listed.some((room: { name: string }) => room.name === 'Alex private meeting')).toBe(false);

    await roomCard(page, 'ID and link meeting').getByRole('button', { name: 'Invite', exact: true }).click();
    const sharing = page.getByRole('dialog', { name: 'Invite to ID and link meeting', exact: true });
    await expect(sharing.getByLabel('Room ID', { exact: true })).toHaveValue(id);
    const link = await sharing.getByLabel('Room link', { exact: true }).inputValue();
    expect(new URL(link).hash).toBe(`#room=${id}`);
    await guest.goto(link);
    await expect(guest.getByLabel('Room ID or link', { exact: true })).toHaveValue(id);
    await expect(guest.locator('.call-connection')).toHaveCount(0);
    await guest.getByLabel('Your name', { exact: true }).fill('Link guest');
    await guest.getByRole('button', { name: 'Join session' }).click();
    await expect(guest.locator('.call-connection')).toContainText('connected');
    await expect(guest.getByRole('button', { name: 'Microphone', exact: true })).toBeVisible();
    await expect(guest.getByRole('button', { name: 'Manage speakers', exact: true })).toHaveCount(0);
    await leave(guest);
    await guest.getByLabel('Your name', { exact: true }).fill('Manual ID guest');
    await guest.getByLabel('Room ID or link', { exact: true }).fill(id.toLowerCase().replaceAll('-', ' '));
    await guest.getByRole('button', { name: 'Join session' }).click();
    await expect(guest.locator('.call-connection')).toContainText('connected');
    await expect(guest.locator('.call-title')).toHaveText('ID and link meeting');
    await expect(guest.getByRole('button', { name: 'Manage speakers', exact: true })).toHaveCount(0);
    await leave(guest);
    await sharing.getByRole('button', { name: 'Close dialog', exact: true }).click();
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Join a room', exact: true })).toBeVisible();
  } finally { await otherContext.close(); await guestContext.close(); }
});

test('room admin invitation requires acceptance; removal revokes elevated access and retains shared room membership', async ({ browser, page }) => {
  const adminContext = await browser.newContext();
  const guestContext = await browser.newContext();
  const admin = await adminContext.newPage(), guest = await guestContext.newPage();
  try {
    await signIn(page, cfg.adminUsername || 'admin', cfg.adminPassword);
    const id = await createRoom(page, 'Delegated webinar', 'webinar');
    const card = roomCard(page, 'Delegated webinar');
    await card.getByRole('button', { name: 'Invite', exact: true }).click();
    const sharing = page.getByRole('dialog', { name: 'Invite to Delegated webinar', exact: true });
    const ordinaryLink = await sharing.getByLabel('Room link', { exact: true }).inputValue();
    await sharing.getByRole('button', { name: 'Create admin invitation', exact: true }).click();
    const invitationLink = await sharing.getByLabel('Admin invitation link', { exact: true }).inputValue();
    expect(invitationLink).toContain('#adminInvite=');
    await expect(sharing.getByText('One person can accept this invitation within 24 hours.', { exact: true })).toBeVisible();

    await admin.goto(invitationLink);
    await admin.getByRole('button', { name: 'Sign in to accept invitation', exact: true }).click();
    await admin.getByLabel('Username', { exact: true }).fill(cfg.secondUsername);
    await admin.getByLabel('Password', { exact: true }).fill(cfg.secondPassword);
    await admin.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(admin.getByRole('heading', { name: 'Become a room admin', exact: true })).toBeVisible();
    const beforeAcceptance = await admin.evaluate(async () => (await fetch('/api/rooms')).json());
    expect(beforeAcceptance.some((room: { id: string }) => room.id === id)).toBe(false);
    await admin.getByRole('button', { name: 'Accept invitation', exact: true }).click();
    await expect(admin.getByRole('heading', { name: 'Your rooms', exact: true })).toBeVisible();
    const delegated = roomCard(admin, 'Delegated webinar');
    await expect(delegated.getByText('Room admin', { exact: true })).toBeVisible();
    await admin.reload();
    await expect(delegated).toBeVisible();
    await delegated.getByRole('button', { name: 'Invite', exact: true }).click();
    const adminSharing = admin.getByRole('dialog', { name: 'Invite to Delegated webinar', exact: true });
    await expect(adminSharing.getByLabel('Room link', { exact: true })).toHaveValue(ordinaryLink);
    await expect(adminSharing.getByRole('button', { name: 'Create admin invitation', exact: true })).toHaveCount(0);
    await expect(adminSharing.getByRole('heading', { name: 'Room admins', exact: true })).toHaveCount(0);
    const forbidden = await admin.evaluate(async ({ room, token }) => {
      const create = await fetch(`/api/rooms/${room}/admin-invitations`, { method: 'POST', body: '{}' });
      const list = await fetch(`/api/rooms/${room}/admins`);
      const close = await fetch(`/api/rooms/${room}/close`, { method: 'POST', body: '{}' });
      const reuse = await fetch('/api/room-admin-invitations/accept', { method: 'POST', body: JSON.stringify({ invite: token }) });
      return [create.status, list.status, close.status, reuse.status];
    }, { room: id, token: new URL(invitationLink).hash.slice('#adminInvite='.length) });
    expect(forbidden).toEqual([403, 403, 403, 403]);
    await adminSharing.getByRole('button', { name: 'Close dialog', exact: true }).click();
    await delegated.getByRole('button', { name: 'Join room', exact: true }).click();
    await expect(admin.locator('.call-connection')).toContainText('connected');
    const role = await admin.evaluate(async () => {
      const response = await fetch('/api/session', { method: 'POST', headers: { Authorization: `Bearer ${sessionStorage.getItem('checkpoint.session')}` }, body: '{}' });
      return (await response.json()).role;
    });
    expect(role).toBe('moderator');
    await admin.getByRole('button', { name: /^People \(/ }).click();
    await expect(admin.getByRole('button', { name: 'End meeting for everyone', exact: true })).toHaveCount(0);
    await expect(admin.getByLabel('Lock new guest joins', { exact: true })).toHaveCount(0);
    await expect(admin.getByLabel('Waiting room for new guests', { exact: true })).toHaveCount(0);

    await guest.goto(ordinaryLink);
    await guest.getByLabel('Your name', { exact: true }).fill('Ordinary webinar guest');
    await guest.getByRole('button', { name: 'Join session' }).click();
    await expect(guest.locator('.call-connection')).toContainText('connected');
    await expect(guest.getByRole('button', { name: 'Manage speakers', exact: true })).toHaveCount(0);
    await expect(guest.getByRole('button', { name: 'Microphone', exact: true })).toHaveCount(0);
    await admin.locator('summary[aria-label="Actions for Ordinary webinar guest"]').click();
    await expect(admin.getByRole('button', { name: 'Remove', exact: true })).toHaveCount(0);
    await admin.getByRole('button', { name: 'Invite to speak', exact: true }).click();
    await expect(guest.getByRole('button', { name: 'Microphone', exact: true })).toBeVisible();

    await sharing.getByRole('button', { name: 'Close dialog', exact: true }).click();
    await card.getByRole('button', { name: 'Invite', exact: true }).click();
    const remove = sharing.getByRole('button', { name: `Remove ${cfg.secondUsername} as room admin`, exact: true });
    await expect(remove).toBeVisible();
    page.once('dialog', dialog => dialog.accept());
    await remove.click();
    await expect(remove).toHaveCount(0);
    await expect(admin.getByRole('heading', { name: 'Connection interrupted', exact: true })).toBeVisible();
    await admin.getByRole('button', { name: 'Rejoin session', exact: true }).click();
    await expect(admin.getByRole('alert')).toContainText('Session expired or access revoked');
    await leave(admin, true);
    await admin.reload();
    await expect(delegated.getByText('Shared with me', { exact: true })).toBeVisible();
    await expect(delegated.getByText('Room admin', { exact: true })).toHaveCount(0);
    const afterRemoval = await admin.evaluate(async () => (await fetch('/api/rooms')).json());
    expect(afterRemoval.find((room: { id: string }) => room.id === id)?.accessRole).toBe('shared');
    // Removing a room admin does not remove ordinary guests from the ongoing room.
    await expect(guest.locator('.call-connection')).toContainText('connected');
    await leave(guest);
  } finally { await adminContext.close(); await guestContext.close(); }
});
