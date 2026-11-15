import { test, expect, type Page } from '@playwright/test';
import { readFileSync, mkdirSync } from 'node:fs';
const enabled = process.env.CHECKPOINT_NATIVE_TEST === '1';
const cfg = enabled ? JSON.parse(readFileSync('../.dev/native/access.json', 'utf8')) : {};
test.skip(!enabled, 'Requires isolated native fixture');
async function join(page: Page, name: string) {
  await page.goto(cfg.publicURL);
  await page.getByLabel('Your name').fill(name);
  await page.getByLabel('Room ID or link', { exact: true }).fill(cfg.meeting);
  await page.getByRole('button', { name: 'Join session' }).click();
  await expect(page.locator('.call-connection')).toContainText('connected');
  await expect(page.getByRole('button', { name: 'Microphone', exact: true })).toBeVisible();
}
test('floating controls preserve video space and output mute is independent of the microphone', async ({ browser }) => {
  const one = await browser.newContext({ permissions: ['camera', 'microphone'] });
  const two = await browser.newContext({ permissions: ['camera', 'microphone'] });
  const a = await one.newPage(), b = await two.newPage();
  try {
    await join(a, 'Taylor');
    const mic = a.getByRole('button', { name: 'Microphone', exact: true });
    const output = a.getByRole('button', { name: 'Audio output', exact: true });
    await mic.click();
    await a.getByRole('button', { name: 'Camera', exact: true }).click();
    await output.click();
    await expect(output).toHaveAttribute('aria-pressed', 'false');
    await expect(mic).toHaveAttribute('data-lk-enabled', 'true');
    // A remote audio track arriving after mute must also start inaudible.
    await join(b, 'Jordan');
    await b.getByRole('button', { name: 'Microphone', exact: true }).click();
    await b.getByRole('button', { name: 'Camera', exact: true }).click();
    await expect.poll(() => a.locator('audio').count()).toBeGreaterThan(0);
    await expect.poll(() => a.locator('audio').evaluateAll(nodes => nodes.every(node => (node as HTMLAudioElement).volume === 0))).toBe(true);
    await output.click();
    await expect(output).toHaveAttribute('aria-pressed', 'true');
    await expect.poll(() => a.locator('audio').evaluateAll(nodes => nodes.every(node => (node as HTMLAudioElement).volume === 1))).toBe(true);
    await expect(mic).toHaveAttribute('data-lk-enabled', 'true');
    mkdirSync('../.dev/ui-review', { recursive: true });
    for (const [width, height] of [[1440, 900], [390, 844], [320, 740], [844, 390]]) {
      await a.setViewportSize({ width, height });
      await expect.poll(() => a.evaluate(() => document.documentElement.scrollWidth <= innerWidth && document.documentElement.scrollHeight <= innerHeight)).toBe(true);
      const top = await a.locator('.session-controls').boundingBox();
      const stage = await a.locator('.media-stage').boundingBox();
      const dock = await a.locator('.call-dock').boundingBox();
      expect(top!.height).toBeLessThanOrEqual(48);
      expect(stage!.height).toBeGreaterThan(height * 0.8);
      expect(dock!.y + dock!.height).toBeLessThanOrEqual(height);
      expect(dock!.x).toBeGreaterThanOrEqual(0);
      expect(dock!.x + dock!.width).toBeLessThanOrEqual(width);
      await a.screenshot({ path: `../.dev/ui-review/compact-call-${width}.png` });
    }
    await a.setViewportSize({ width: 320, height: 740 });
    await a.getByRole('button', { name: 'Meeting details' }).click();
    await expect(a.getByRole('button', { name: 'Share screen', exact: true })).toBeVisible();
    await expect(a.getByRole('button', { name: 'Raise hand', exact: true })).toBeVisible();
    await expect(a.getByRole('complementary', { name: 'Meeting details', exact: true }).getByRole('combobox')).toHaveCount(0);
    await a.getByRole('button', { name: 'Choose Microphone', exact: true }).click();
    await expect(a.getByRole('menu', { name: 'Choose Microphone', exact: true })).toBeVisible();
    await a.keyboard.press('Escape');
    await a.getByRole('button', { name: 'Chat', exact: true }).click();
    await expect(a.getByLabel('Message', { exact: true })).toBeVisible();
    await expect(output).toBeVisible();
    await a.getByRole('button', { name: 'Close panel' }).click();
    for (const page of [a,b]) { await page.getByRole('button', { name: 'Leave', exact: true }).click(); await expect(page.getByRole('heading', { name: 'Join a room' })).toBeVisible(); }
  } finally { await one.close(); await two.close(); }
});
