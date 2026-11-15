import { test, expect, type BrowserContext } from '@playwright/test';
import { readFileSync, mkdirSync } from 'node:fs';
const enabled = process.env.CHECKPOINT_UI_SETTINGS === '1';
const cfg = enabled ? JSON.parse(readFileSync(process.env.CHECKPOINT_NATIVE_ACCESS!, 'utf8')) : {};
test.skip(!enabled, 'Requires isolated UI stand');

test('Paper room filters, ID dialog and hand queue preserve real room access', async ({ page, playwright, browser }) => {
  await page.goto(cfg.publicURL + '/rooms');
  await page.getByLabel('Username', { exact: true }).fill(cfg.adminUsername);
  await page.getByLabel('Password', { exact: true }).fill(cfg.adminPassword);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  const name = `Paper room ${Date.now()}`;
  await page.getByRole('button', { name: 'Create a room', exact: true }).click();
  const creation = page.getByRole('dialog', { name: 'Create a room', exact: true });
  await expect(creation.getByRole('radio', { name: /^Meeting/ })).toBeChecked();
  await creation.getByLabel('Room name', { exact: true }).fill(name);
  await creation.getByRole('button', { name: 'Create room', exact: true }).click();
  const card = page.getByRole('article').filter({ has: page.getByRole('heading', { name, exact: true }) });
  const id = (await card.locator('code').textContent())!.trim();
  await page.getByRole('button', { name: 'Shared with me', exact: true }).click();
  await expect(card).toHaveCount(0);
  await page.getByRole('button', { name: 'My rooms', exact: true }).click();
  await expect(card).toBeVisible();
  await page.getByRole('searchbox', { name: 'Find a room', exact: true }).fill('nothing-matches-this');
  await expect(page.getByText('No matching rooms.', { exact: true })).toBeVisible();
  await page.getByRole('searchbox', { name: 'Find a room', exact: true }).fill(name);
  await expect(card).toBeVisible();
  const entry = page.getByRole('button', { name: 'Join by ID', exact: true });
  await entry.click();
  const dialog = page.getByRole('dialog', { name: 'Join by ID', exact: true });
  await expect(dialog.getByLabel('Room ID or link', { exact: true })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(entry).toBeFocused();
  await entry.click();
  await dialog.getByLabel('Room ID or link', { exact: true }).fill(`${cfg.publicURL}/#room=${id}`);
  await dialog.getByRole('button', { name: /Join/ }).click();
  await expect(page.locator('.call-connection')).toContainText('connected');
  await expect(page.locator('.call-dock').getByRole('button', { name: 'Room history', exact: true })).toHaveCount(1);
  const guests = await playwright.request.newContext({ baseURL: cfg.publicURL, ignoreHTTPSErrors: true });
  const contexts: BrowserContext[] = [];
  const join = async (name: string) => {
    const context = await browser.newContext(); contexts.push(context);
    const guest = await context.newPage();
    await guest.goto(cfg.publicURL);
    await guest.getByLabel('Your name', { exact: true }).fill(name);
    await guest.getByLabel('Room ID or link', { exact: true }).fill(id);
    await guest.getByRole('button', { name: 'Join session' }).click();
    await expect(guest.locator('.call-connection')).toContainText('connected');
    return { session: (await guest.evaluate(() => sessionStorage.getItem('checkpoint.session')))! };
  };
  const a = await join('First guest'), b = await join('Second guest');
  const hand = async (session: string, raised: boolean) => {
    const response = await guests.post('/api/hand', { headers: { Authorization: `Bearer ${session}` }, data: { raised } });
    expect(response.ok()).toBe(true);
  };
  try {
    await hand(a.session, true); await hand(b.session, true);
    const people = page.locator('.call-dock').getByRole('button', { name: /^People/ });
    await people.click(); await expect(people).toHaveAttribute('aria-expanded', 'true');
    const hands = page.locator('.people-group-hands .person-copy > strong');
    await expect(hands).toHaveText(['First guest', 'Second guest']);
    await expect(page.locator('.people-group-owners')).toContainText(cfg.adminUsername);
    await expect(page.locator('.people-group').first()).toHaveClass(/people-group-hands/);
    await hand(a.session, false);
    await expect(page.locator('.people-group-participants')).toContainText('First guest');
    await hand(a.session, true);
    await expect(hands).toHaveText(['Second guest', 'First guest']);
    mkdirSync('../.dev/ui-review', { recursive: true });
    for (const width of [320, 390, 1280]) {
      await page.setViewportSize({ width, height: 900 });
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      const dock = await page.locator('.call-dock').boundingBox();
      expect(dock!.x).toBeGreaterThanOrEqual(0);
      expect(dock!.x + dock!.width).toBeLessThanOrEqual(width);
      await page.screenshot({ path: `../.dev/ui-review/paper-queue-${width}.png` });
    }
  } finally {
    for (const guest of [a,b]) await guests.post('/api/leave', { headers: { Authorization: `Bearer ${guest.session}` }, data: {} });
    await guests.dispose();
    for (const context of contexts) await context.close();
  }
  await page.getByRole('button', { name: 'Leave', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Your rooms', exact: true })).toBeVisible();
});
