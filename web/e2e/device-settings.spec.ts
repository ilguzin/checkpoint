import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
const enabled = process.env.CHECKPOINT_UI_SETTINGS === '1';
const cfg = enabled ? JSON.parse(readFileSync(process.env.CHECKPOINT_NATIVE_ACCESS!, 'utf8')) : {};
test.skip(!enabled, 'Requires isolated UI stand');

test('guest settings are a reloadable page and support browser navigation', async ({ page }) => {
  await page.goto(cfg.publicURL + '/settings');
  await expect(page.getByRole('heading', { name: 'User settings', exact: true })).toBeVisible();
  await expect(page.getByLabel('Current password')).toHaveCount(0);
  await page.getByLabel('Interface language').selectOption('ru');
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Настройки пользователя', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '← Назад', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Войти в комнату', exact: true })).toBeVisible();
  await page.goBack();
  await expect(page.getByRole('heading', { name: 'Настройки пользователя', exact: true })).toBeVisible();
});

for (const width of [320, 390, 1280]) test(`device menus, unified history and explicit meeting exit at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 });
  await page.addInitScript(unsupported => {
    if (unsupported) delete (HTMLMediaElement.prototype as any).setSinkId;
    const PC = window.RTCPeerConnection;
    (window as any).__pcCount = 0;
    window.RTCPeerConnection = new Proxy(PC, { construct(target, args) { (window as any).__pcCount++; return Reflect.construct(target, args); } });
  }, width === 320);
  await page.goto(cfg.publicURL);
  const original = await page.evaluate(async c => {
    const response = await fetch('/api/account/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: c.adminUsername, password: c.adminPassword }) });
    if (!response.ok) throw new Error('Login failed');
    const user = await response.json();
    await fetch('/api/account/preferences', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uiLocale: 'en' }) });
    return user.uiLocale;
  }, cfg);
  let room = '';
  try {
    room = await page.evaluate(async name => {
      const response = await fetch('/api/rooms', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, mode: 'meeting' }) });
      if (!response.ok) throw new Error('Room creation failed');
      return (await response.json()).id;
    }, `Device controls ${width} ${Date.now()}`);
    await page.goto(cfg.publicURL + '/rooms');
    await expect(page.getByLabel('Interface language')).toHaveCount(0);
    await expect(page.getByLabel('Current password')).toHaveCount(0);
    await page.getByRole('article').filter({ hasText: room }).getByRole('button', { name: /Join room/ }).click();
    const mic = page.getByRole('button', { name: 'Microphone', exact: true });
    const camera = page.getByRole('button', { name: 'Camera', exact: true });
    await expect(mic).toHaveAttribute('data-lk-enabled', 'false');
    await expect(camera).toHaveAttribute('data-lk-enabled', 'false');
    for (const name of ['Microphone', 'Camera', 'Audio output']) {
      const trigger = page.getByRole('button', { name: `Choose ${name}`, exact: true });
      const toggleBox = await page.getByRole('button', { name, exact: true }).boundingBox();
      const triggerBox = await trigger.boundingBox();
      expect(triggerBox!.x).toBeGreaterThanOrEqual(toggleBox!.x + toggleBox!.width - 1);
      expect(Math.abs(triggerBox!.y - toggleBox!.y)).toBeLessThan(2);
      await trigger.click();
      const menu = page.getByRole('menu', { name: `Choose ${name}`, exact: true });
      await expect(menu.getByRole('menuitemradio', { name: 'System default', exact: true })).toHaveAttribute('aria-checked', 'true');
      const box = await menu.boundingBox(); expect(box!.x).toBeGreaterThanOrEqual(0); expect(box!.x + box!.width).toBeLessThanOrEqual(width);
      if (width === 320 && name === 'Audio output') { await expect(menu.getByRole('menuitemradio', { name: 'System default', exact: true })).toBeDisabled(); await page.keyboard.press('Escape'); } else await menu.getByRole('menuitemradio', { name: 'System default', exact: true }).click();
      await expect(menu).toHaveCount(0);
      await expect(trigger).toBeFocused();
    }
    await expect(mic).toHaveAttribute('data-lk-enabled', 'false');
    await expect(camera).toHaveAttribute('data-lk-enabled', 'false');
    await camera.click(); await expect(camera).toHaveAttribute('data-lk-enabled', 'true');
    await camera.click(); await expect(camera).toHaveAttribute('data-lk-enabled', 'false');
    await page.getByRole('button', { name: 'Choose Camera', exact: true }).click();
    const cameras = page.getByRole('menuitemradio').filter({ hasNotText: 'System default' });
    if (await cameras.count()) await cameras.first().click(); else await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Choose Camera', exact: true }).click();
    await page.getByRole('menuitemradio', { name: 'System default', exact: true }).click();
    await expect(camera).toHaveAttribute('data-lk-enabled', 'false');
    await camera.click(); await expect(camera).toHaveAttribute('data-lk-enabled', 'true');
    await camera.click(); await expect(camera).toHaveAttribute('data-lk-enabled', 'false');
    await page.getByRole('button', { name: 'Choose Microphone', exact: true }).click();
    const inputs = page.getByRole('menuitemradio').filter({ hasNotText: 'System default' });
    if (await inputs.count()) await inputs.first().click(); else await page.keyboard.press('Escape');
    await expect(mic).toHaveAttribute('data-lk-enabled', 'false');
    await mic.click(); await expect(mic).toHaveAttribute('data-lk-enabled', 'true');
    const output = page.getByRole('button', { name: 'Audio output', exact: true });
    await output.click(); await expect(output).toHaveAttribute('aria-pressed', 'false');
    await page.getByRole('button', { name: 'Choose Audio output', exact: true }).click();
    if (width === 320) await page.keyboard.press('Escape'); else await page.getByRole('menuitemradio', { name: 'System default', exact: true }).click();
    await expect(output).toHaveAttribute('aria-pressed', 'false');
    await expect(page.getByRole('button', { name: 'Manage speakers', exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Meeting details', exact: true })).toHaveCount(1);
    await page.locator('.call-dock').getByRole('button', { name: 'Meeting details', exact: true }).click();
    await expect(page.locator('.call-panel').getByRole('heading', { name: 'Devices', exact: true })).toHaveCount(0);
    await expect(page.locator('.call-panel').getByLabel('Interface language')).toHaveCount(0);
    await page.getByRole('button', { name: 'Chat', exact: true }).last().click();
    await page.getByRole('textbox', { name: 'Message', exact: true }).fill('Keep this draft');
    const pcCount = await page.evaluate(() => (window as any).__pcCount);
    const session = await page.evaluate(() => sessionStorage.getItem('checkpoint.session'));
    await expect(page.getByRole('button', { name: 'User settings', exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Sign out', exact: true })).toHaveCount(0);
    const history = page.getByRole('button', { name: 'Room history', exact: true });
    await expect(history).toHaveCount(1);
    await history.hover();
    await expect.poll(() => history.evaluate(button => { const r = button.getBoundingClientRect(); return button.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)); })).toBe(true);
    await history.click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await page.getByRole('button', { name: 'Close dialog', exact: true }).click();
    // Browser navigation to settings must not bypass the explicit leave action.
    await page.evaluate(() => { window.history.pushState(null, '', '/settings'); dispatchEvent(new PopStateEvent('popstate')); });
    await expect(page).toHaveURL(cfg.publicURL + '/rooms');
    expect(await page.evaluate(() => (window as any).__pcCount)).toBe(pcCount);
    expect(await page.evaluate(() => sessionStorage.getItem('checkpoint.session'))).toBe(session);
    await expect(page.getByRole('textbox', { name: 'Message', exact: true })).toHaveValue('Keep this draft');
    await expect(mic).toHaveAttribute('data-lk-enabled', 'true');
    await expect(output).toHaveAttribute('aria-pressed', 'false');
    await expect(page.getByRole('button', { name: 'User settings', exact: true })).toHaveCount(0);
    const dock = await page.locator('.call-dock').boundingBox();
    expect(dock!.x).toBeGreaterThanOrEqual(0); expect(dock!.x + dock!.width).toBeLessThanOrEqual(width);
    const leaveBox = await page.getByRole('button', { name: 'Leave', exact: true }).boundingBox();
    expect(leaveBox!.x + leaveBox!.width).toBeLessThanOrEqual(width);
    await page.getByRole('button', { name: 'Close panel', exact: true }).click();
    await page.getByRole('button', { name: 'Choose Camera', exact: true }).click();
    await page.screenshot({ path: `../.dev/call-devices-${width}.png`, fullPage: true });
    await page.keyboard.press('Escape');
    await page.goto(cfg.publicURL + '/settings');
    await expect(page.getByRole('button', { name: 'Leave', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'User settings', exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Sign out', exact: true })).toHaveCount(0);
    await page.getByRole('button', { name: 'Leave', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Sign out', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'User settings', exact: true }).click();
    await page.getByLabel('Interface language').selectOption('ru');
    await expect(page.getByRole('heading', { name: 'Настройки пользователя', exact: true })).toBeVisible();
    await expect(page.getByLabel('Текущий пароль')).toBeVisible();
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

  } finally {
    await page.evaluate(async ({ room, original }) => {
      if (room) await fetch(`/api/rooms/${room}/delete`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirmed: true }) });
      await fetch('/api/account/preferences', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uiLocale: original }) });
    }, { room, original });
  }
});
