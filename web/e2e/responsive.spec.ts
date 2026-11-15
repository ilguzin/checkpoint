import { test, expect } from '@playwright/test';
import { mkdirSync, readFileSync } from 'node:fs';
const enabled = process.env.CHECKPOINT_NATIVE_TEST === '1';
const cfg = enabled ? JSON.parse(readFileSync('../.dev/native/access.json', 'utf8')) : {};
test.skip(!enabled, 'Requires native fixture');
for (const width of [320, 390, 768, 1440]) {
  test(`responsive entry and public health at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: width < 500 ? 844 : 900 });
    await page.goto(cfg.publicURL);
    await expect(page.getByRole('heading', { name: 'Join a room' })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.getByLabel('Your name').focus();
    await expect(page.getByLabel('Your name')).toBeFocused();
    const bounds = await page.getByRole('button', { name: 'Join session' }).boundingBox();
    expect(bounds?.height).toBeGreaterThanOrEqual(44);
    mkdirSync('../.dev/ui-review', { recursive: true });
    await page.screenshot({ path: `../.dev/ui-review/entry-${width}.png`, fullPage: true });
    await page.goto(cfg.observerURL);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: `../.dev/ui-review/health-${width}.png`, fullPage: true });
  });
}
test('phone guest can join, open panels, raise a hand and leave', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(cfg.publicURL);
  await page.getByLabel('Your name').fill('Phone preview');
  await page.getByLabel('Room ID or link', { exact: true }).fill(cfg.meeting);
  await page.getByRole('button', { name: 'Join session' }).click();
  await expect(page.locator('.session-controls [role=status]')).toContainText('connected');
  await page.getByRole('button', { name: 'Meeting details' }).click();
  await page.getByRole('button', { name: 'Raise hand', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Lower hand' })).toBeVisible();
  await page.getByRole('button', { name: 'Chat', exact: true }).click();
  await expect(page.getByLabel('Message', { exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: '../.dev/ui-review/meeting-phone.png', fullPage: true });
  await page.setViewportSize({ width: 844, height: 390 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.getByRole('button', { name: 'Leave', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Join a room' })).toBeVisible();
});
test('Observer history and a real synthetic-media connection check are public', async ({ page }) => {
  await page.goto(cfg.observerURL);
  await page.getByRole('button', { name: 'History', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'See what happened.' })).toBeVisible();
  await expect(page.locator('.day-bars button')).toHaveCount(30);
  await page.getByRole('button', { name: 'Connection quality', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Know what the numbers mean.' })).toBeVisible();
  await page.getByRole('button', { name: 'Connection check', exact: true }).click();
  await page.getByRole('button', { name: 'Run connection check', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Your browser received test media.' })).toBeVisible({ timeout: 30000 });
});

test('license notices are available without signing in', async ({ page }) => {
  await page.goto(cfg.publicURL + '/licenses');
  await expect(page.getByRole('heading', { name: 'Open source licenses' })).toBeVisible();
  await page.getByLabel('Find a project').fill('@bufbuild/protobuf');
  await page.locator('summary').click();
  await expect(page.getByText('NOTICE-varint-BSD', { exact: true })).toBeVisible();
  await expect(page.locator('pre').filter({ hasText: 'Copyright 2008 Google Inc.' })).toBeVisible();
});
