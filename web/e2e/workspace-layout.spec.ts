import { test, expect, type Page } from '@playwright/test';
import { readFileSync, mkdirSync } from 'node:fs';
const enabled = process.env.CHECKPOINT_NATIVE_TEST === '1';
const cfg = enabled ? JSON.parse(readFileSync('../.dev/native/access.json', 'utf8')) : {};
test.skip(!enabled || !cfg.adminPassword, 'Requires isolated native administrator fixture');

async function noHorizontalOverflow(page: Page) {
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}
for (const width of [320, 390, 1440]) {
  test(`organizer login and workspace fit ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(cfg.publicURL);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Welcome back.', exact: true })).toBeVisible();
    await noHorizontalOverflow(page);
    mkdirSync('../.dev/ui-review', { recursive: true });
    await page.screenshot({ path: `../.dev/ui-review/organizer-login-${width}.png`, fullPage: true });
    await page.getByLabel('Username', { exact: true }).fill('admin');
    await page.getByLabel('Password', { exact: true }).fill(cfg.adminPassword);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Your rooms', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Create a room', exact: true })).toBeEnabled();
    await noHorizontalOverflow(page);
    await page.screenshot({ path: `../.dev/ui-review/workspace-${width}.png`, fullPage: true });
    const create = page.getByRole('button', { name: 'Create a room', exact: true });
    await create.click();
    const creation = page.getByRole('dialog', { name: 'Create a room', exact: true });
    await expect(creation).toBeVisible();
    await expect(creation.getByRole('radio', { name: /^Meeting/ })).toBeChecked();
    await page.keyboard.press('Escape');
    await expect(creation).toHaveCount(0);
    await expect(create).toBeFocused();
    await create.click();
    const roomName = width === 320 ? 'A distributed engineering team planning workshop' : `Workspace review ${width}`;
    await creation.getByLabel('Room name', { exact: true }).fill(roomName);
    await creation.getByRole('radio', { name: /^Webinar/ }).check();
    await noHorizontalOverflow(page);
    await page.screenshot({ path: `../.dev/ui-review/workspace-create-${width}.png` });
    await creation.getByRole('button', { name: 'Create room', exact: true }).click();
    await expect(creation).toHaveCount(0);
    const room = page.getByRole('article').filter({ has: page.getByRole('heading', { name: roomName, exact: true }) });
    await expect(room).toBeVisible();
    await noHorizontalOverflow(page);
    const invite = room.getByRole('button', { name: 'Invite', exact: true });
    await invite.click();
    const sharing = page.getByRole('dialog', { name: `Invite to ${roomName}`, exact: true });
    const roomLink = sharing.getByLabel('Room link', { exact: true });
    await expect(roomLink).toBeVisible();
    expect(await roomLink.inputValue()).toContain('#room=');
    expect(await sharing.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
    await noHorizontalOverflow(page);
    await page.screenshot({ path: `../.dev/ui-review/workspace-invitations-${width}.png` });
    await sharing.getByRole('button', { name: 'Close dialog', exact: true }).click();
    await expect(sharing).toHaveCount(0);
    await expect(invite).toBeFocused();
    await expect(page.getByText('Organizer accounts', { exact: true })).toHaveCount(0);
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Join a room', exact: true })).toBeVisible();
  });
}

test('license search explains an empty result and recovers on a phone', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 844 });
  await page.goto(`${cfg.publicURL}/licenses`);
  await expect(page.locator('.license-list details').first()).toBeVisible();
  await page.getByLabel('Find a project').fill('no-such-project-for-ui-review');
  await expect(page.getByRole('heading', { name: 'No matching projects' })).toBeVisible();
  await expect(page.locator('.license-list details')).toHaveCount(0);
  await noHorizontalOverflow(page);
  mkdirSync('../.dev/ui-review', { recursive: true });
  await page.screenshot({ path: '../.dev/ui-review/licenses-empty-320.png', fullPage: true });
  await page.getByRole('button', { name: 'Clear search', exact: true }).click();
  await expect(page.getByLabel('Find a project')).toHaveValue('');
  await expect(page.locator('.license-list details').first()).toBeVisible();
  await page.getByLabel('Find a project').fill('@bufbuild/protobuf');
  await page.locator('summary').click();
  await expect(page.locator('pre').filter({ hasText: 'Copyright 2008 Google Inc.' })).toBeVisible();
  await noHorizontalOverflow(page);
});
