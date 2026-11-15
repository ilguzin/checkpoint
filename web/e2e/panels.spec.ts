import { test, expect, type Page } from '@playwright/test';
import { mkdirSync, readFileSync } from 'node:fs';

const enabled = process.env.CHECKPOINT_NATIVE_TEST === '1';
const cfg = enabled ? JSON.parse(readFileSync('../.dev/native/access.json', 'utf8')) : {};
test.skip(!enabled, 'Requires the isolated native fixture');

async function join(page: Page, name: string, invitation: string) {
  await page.goto(cfg.publicURL);
  await page.getByLabel('Your name', { exact: true }).fill(name);
  await page.getByLabel('Room ID or link', { exact: true }).fill(invitation);
  await page.getByRole('button', { name: 'Join session' }).click();
  await expect(page.locator('.call-connection')).toContainText('connected');
}
async function openChat(page: Page) {
  if (await page.getByRole('complementary', { name: 'Chat', exact: true }).count()) return;
  const dockChat = page.locator('.call-dock').getByRole('button', { name: 'Chat', exact: true });
  if (await dockChat.isVisible()) await dockChat.click();
  else {
    await page.getByRole('button', { name: 'Meeting details', exact: true }).click();
    await page.getByRole('complementary', { name: 'Meeting details', exact: true }).getByRole('button', { name: 'Chat', exact: true }).click();
  }
  await expect(page.getByLabel('Message', { exact: true })).toBeVisible();
}
async function leave(page: Page) {
  await page.getByRole('button', { name: 'Leave', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Join a room' })).toBeVisible();
}
async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth && document.documentElement.scrollHeight <= innerHeight)).toBe(true);
  expect(await page.locator('.call-panel').evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
}

test('panels keep people readable and the chat composer reachable while messages scroll', async ({ browser }) => {
  const one = await browser.newContext();
  const two = await browser.newContext();
  const a = await one.newPage(), b = await two.newPage();
  try {
    await join(a, 'Panel Riley', cfg.meeting);
    await join(b, 'Panel Morgan', cfg.meeting);
    const people = a.getByRole('button', { name: /^People \(/ });
    await people.click();
    const peoplePanel = a.getByRole('complementary', { name: 'People', exact: true });
    await expect(peoplePanel.locator('.people-list li').filter({ hasText: 'Panel Morgan' })).toHaveCount(1);
    await expect(peoplePanel.locator('.people-list li').filter({ hasText: 'Panel Riley' })).toHaveCount(1);
    await a.keyboard.press('Escape');
    await expect(peoplePanel).toHaveCount(0);
    await expect(people).toBeFocused();
    await a.getByRole('button', { name: 'Meeting details', exact: true }).click();
    const settings = a.getByRole('complementary', { name: 'Meeting details', exact: true });
    await expect(settings).toBeVisible();
    await expect(settings.getByRole('combobox')).toHaveCount(0);
    await expect(a.getByRole('button', { name: 'Choose Camera', exact: true })).toBeVisible();
    await a.getByRole('button', { name: 'Close panel', exact: true }).click();
    await expect(a.getByRole('button', { name: 'Meeting details', exact: true })).toBeFocused();

    await openChat(a); await openChat(b);
    const input = a.getByLabel('Message', { exact: true });
    await input.fill('First line'); await input.press('Shift+Enter'); await input.pressSequentially('Second line');
    await expect(input).toHaveValue('First line\nSecond line');
    await expect(b.locator('.message-list article').filter({ hasText: 'First line' })).toHaveCount(0);
    await input.press('Enter');
    await expect(input).toHaveValue('');
    await expect(b.locator('.message-list article').filter({ hasText: 'First line' })).toContainText('Second line');

    await input.fill('Keep this draft');
    await a.getByRole('button', { name: 'Close panel', exact: true }).click();
    await expect(a.getByRole('button', { name: 'Chat', exact: true })).toBeFocused();
    await openChat(a);
    await expect(input).toHaveValue('Keep this draft');
    const longMessage = 'Wrapping check: ' + 'abcdefghij'.repeat(170);
    await input.fill(longMessage); await input.press('Enter');
    await expect(input).toHaveValue('');
    await expect(b.locator('.message-list article').filter({ hasText: 'Wrapping check:' })).toContainText(longMessage);
    await b.getByLabel('Message', { exact: true }).fill('Delivered back to Riley');
    await b.getByRole('button', { name: 'Send', exact: true }).click();
    await expect(a.locator('.message-list article').filter({ hasText: 'Delivered back to Riley' })).toHaveCount(1);

    mkdirSync('../.dev/ui-review', { recursive: true });
    for (const [width, height] of [[1440, 900], [390, 844], [320, 740]]) {
      await a.setViewportSize({ width, height });
      await noOverflow(a);
      const list = a.getByRole('log');
      expect(await list.evaluate(node => node.scrollHeight > node.clientHeight)).toBe(true);
      const before = await input.boundingBox();
      await list.evaluate(node => { node.scrollTop = 0; });
      const after = await input.boundingBox();
      expect(after!.y).toBeCloseTo(before!.y, 0);
      const panel = await a.locator('.call-panel').boundingBox();
      expect(after!.y).toBeGreaterThanOrEqual(panel!.y);
      expect(after!.y + after!.height).toBeLessThanOrEqual(panel!.y + panel!.height);
      const send = await a.getByRole('button', { name: 'Send', exact: true }).boundingBox();
      expect(send!.y + send!.height).toBeLessThanOrEqual(panel!.y + panel!.height);
      await a.screenshot({ path: `../.dev/ui-review/panel-chat-${width}.png` });
      if (await people.isVisible()) await people.click();
      else {
        await a.getByRole('button', { name: 'Meeting details', exact: true }).click();
        await a.getByRole('complementary', { name: 'Meeting details', exact: true }).getByRole('button', { name: 'People', exact: true }).click();
      }
      await noOverflow(a);
      await expect(peoplePanel.locator('.people-list li').filter({ hasText: 'Panel Morgan' })).toBeVisible();
      await a.screenshot({ path: `../.dev/ui-review/panel-people-${width}.png` });
      await a.getByRole('button', { name: 'Meeting details', exact: true }).click();
      await noOverflow(a);
      await expect(settings).toBeVisible();
    await expect(settings.getByRole('combobox')).toHaveCount(0);
      await a.screenshot({ path: `../.dev/ui-review/panel-details-${width}.png` });
      // Closing details also exercises opening chat from the compact 320px menu.
      await a.getByRole('button', { name: 'Close panel', exact: true }).click();
      await openChat(a);
    }
    await leave(a); await leave(b);
  } finally { await one.close(); await two.close(); }
});

test('chat and webinar questions preserve independent drafts across panel switches', async ({ page }) => {
  await join(page, 'Draft host', cfg.host);
  await openChat(page);
  await page.getByLabel('Message', { exact: true }).fill('Unsent chat draft');
  await page.getByRole('button', { name: 'Questions', exact: true }).click();
  await expect(page.getByLabel('Your question', { exact: true })).toHaveValue('');
  await page.getByLabel('Your question', { exact: true }).fill('Unsent question draft');
  await page.getByRole('button', { name: 'Close panel', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Questions', exact: true })).toBeFocused();
  await openChat(page);
  await expect(page.getByLabel('Message', { exact: true })).toHaveValue('Unsent chat draft');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.getByLabel('Message', { exact: true })).toHaveValue('');
  await page.getByRole('button', { name: 'Questions', exact: true }).click();
  await expect(page.getByLabel('Your question', { exact: true })).toHaveValue('Unsent question draft');
  await page.keyboard.press('Escape');
  await expect(page.getByRole('complementary')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Questions', exact: true })).toBeFocused();
  await leave(page);
});
