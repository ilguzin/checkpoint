import { test, expect } from '@playwright/test';
import { readFileSync, mkdirSync } from 'node:fs';
const enabled = process.env.CHECKPOINT_NATIVE_TEST === '1';
const cfg = enabled ? JSON.parse(readFileSync('../.dev/native/access.json', 'utf8')) : {};
test.skip(!enabled, 'Requires the native fixture');

test('device check stays local, traps focus and releases preview tracks when dismissed', async ({ page }) => {
  await page.addInitScript(() => {
    const state = window as unknown as { previewTracks: MediaStreamTrack[] };
    state.previewTracks = [];
    const original = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
    navigator.mediaDevices.getUserMedia = async constraints => {
      const stream = await original(constraints);
      state.previewTracks.push(...stream.getTracks());
      return stream;
    };
  });
  await page.goto(cfg.publicURL);
  const trigger = page.getByRole('button', { name: 'Check devices before joining' });
  await trigger.click();
  const dialog = page.getByRole('dialog', { name: 'Check your devices' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Close device check' })).toBeFocused();
  await page.keyboard.press('Shift+Tab');
  await expect(dialog.getByRole('button', { name: 'Use these devices' })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('button', { name: 'Close device check' })).toBeFocused();
  expect(await page.evaluate(() => (window as unknown as { previewTracks: MediaStreamTrack[] }).previewTracks.length)).toBe(0);
  await dialog.locator('button[data-lk-source="camera"]').click();
  await expect.poll(() => page.evaluate(() => (window as unknown as { previewTracks: MediaStreamTrack[] }).previewTracks.some(track => track.readyState === 'live'))).toBe(true);
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();
  await expect.poll(() => page.evaluate(() => (window as unknown as { previewTracks: MediaStreamTrack[] }).previewTracks.every(track => track.readyState === 'ended'))).toBe(true);
  await trigger.click();
  await dialog.getByLabel('Your preview name').fill('Device preview');
  await dialog.getByRole('button', { name: 'Use these devices' }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByLabel('Your name', { exact: true })).toHaveValue('Device preview');
  await expect(page.getByRole('heading', { name: 'Join a room' })).toBeVisible();
  await trigger.click();
  await page.mouse.click(5, 5);
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();
});

test('device check fits phone and desktop and shows device errors inside the dialog', async ({ page }) => {
  await page.addInitScript(() => {
    navigator.mediaDevices.getUserMedia = async () => { throw new DOMException('Camera permission was denied.', 'NotAllowedError'); };
  });
  await page.goto(cfg.publicURL);
  const trigger = page.getByRole('button', { name: 'Check devices before joining' });
  await trigger.click();
  const dialog = page.getByRole('dialog', { name: 'Check your devices' });
  mkdirSync('../.dev/ui-review', { recursive: true });
  for (const width of [320, 390, 1440]) {
    await page.setViewportSize({ width, height: 844 });
    await expect(dialog).toBeVisible();
    const bounds = await dialog.boundingBox();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
    expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(844);
    expect(await dialog.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
    await page.screenshot({ path: `../.dev/ui-review/device-modal-${width}.png` });
  }
  await dialog.locator('button[data-lk-source="camera"]').click();
  await expect(dialog.getByRole('alert')).toContainText('Something went wrong. Please try again.');
  await dialog.getByRole('button', { name: 'Close device check' }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole('alert')).toHaveCount(0);
});
