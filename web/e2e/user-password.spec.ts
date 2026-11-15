import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
const enabled = process.env.CHECKPOINT_UI_PASSWORD_TEST === '1';
const cfg = enabled ? JSON.parse(readFileSync(process.env.CHECKPOINT_NATIVE_ACCESS!, 'utf8')) : {};
test.skip(!enabled, 'Requires a disposable local account');
test('password settings validate the old password and sign out after a successful change', async ({ page }) => {
  await page.goto(cfg.publicURL);
  await page.evaluate(async c => {
    const response = await fetch('/api/account/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: c.settingsUsername, password: c.settingsPassword }) });
    if (!response.ok) throw new Error('Fixture login failed');
  }, cfg);
  await page.goto(cfg.publicURL + '/settings');
  await expect(page.getByRole('heading', { name: 'User settings', exact: true })).toBeVisible();
  await page.getByLabel('Current password', { exact: true }).fill('wrong-password');
  await page.getByLabel('New password', { exact: true }).fill(cfg.settingsNextPassword);
  await page.getByRole('button', { name: 'Change and sign out everywhere', exact: true }).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await expect(page).toHaveURL(cfg.publicURL + '/settings');
  await page.getByLabel('Current password', { exact: true }).fill(cfg.settingsPassword);
  await page.getByRole('button', { name: 'Change and sign out everywhere', exact: true }).click();
  await expect(page.getByLabel('Username', { exact: true })).toBeVisible();
  await page.getByLabel('Username', { exact: true }).fill(cfg.settingsUsername);
  await page.getByLabel('Password', { exact: true }).fill(cfg.settingsNextPassword);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Your rooms', exact: true })).toBeVisible();
});
