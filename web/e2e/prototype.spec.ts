import { test, expect, type Page } from '@playwright/test';
import { readFileSync, writeFileSync } from 'node:fs';
const cfg = JSON.parse(readFileSync('../.dev/access.json', 'utf8'));
const local = process.env.CHECKPOINT_BROWSER_LOCAL === '1';
const base = local ? 'http://127.0.0.1:18180' : `https://${cfg.dns}`;
const observer = local ? 'http://127.0.0.1:18182' : `https://${cfg.dns}:9444`;
async function join(page: Page, role: string, name: string) {
  await page.addInitScript(() => {
    const peers: RTCPeerConnection[] = [];
    (window as unknown as { checkpointTestPeers: RTCPeerConnection[] }).checkpointTestPeers = peers;
    window.RTCPeerConnection = new Proxy(window.RTCPeerConnection, {
      construct(target, args) { const peer = Reflect.construct(target, args) as RTCPeerConnection; peers.push(peer); return peer; },
    });
    const sockets: WebSocket[] = [];
    (window as unknown as { checkpointTestSockets: WebSocket[] }).checkpointTestSockets = sockets;
    window.WebSocket = new Proxy(window.WebSocket, {
      construct(target, args) { const socket = Reflect.construct(target, args) as WebSocket; sockets.push(socket); return socket; },
    });
  });
  await page.goto(base, { waitUntil: 'networkidle' });
  await page.getByLabel('Your name').fill(name);
  await page.getByLabel('Test invitation').fill(cfg[role]);
  await page.getByRole('button', { name: 'Join session' }).click();
  await expect(page.locator('.session-controls [role=status]')).toContainText('connected');
}
const camera = (page: Page) => page.locator('button[data-lk-source="camera"]');
const microphone = (page: Page) => page.locator('button[data-lk-source="microphone"]');

test('meeting: fake audio/video, screen capture, ICE and muted signaling reconnect', async ({ browser }) => {
  const one = await browser.newContext({ permissions: ['camera', 'microphone'] });
  const two = await browser.newContext({ permissions: ['camera', 'microphone'] });
  try {
    const a = await one.newPage(), b = await two.newPage();
    await join(a, 'meeting', 'Browser A'); await join(b, 'meeting', 'Browser B');
    await expect(camera(a)).toBeVisible(); await expect(camera(b)).toBeVisible();
    await expect(microphone(a)).toHaveAttribute('data-lk-enabled', 'false');
    await camera(a).click(); await microphone(a).click(); await camera(b).click(); await microphone(b).click();
    await expect.poll(() => b.locator('video').evaluateAll(elements => elements.filter(e => (e as HTMLVideoElement).videoWidth > 0).length)).toBeGreaterThanOrEqual(2);
    await expect.poll(() => a.locator('video').evaluateAll(elements => elements.filter(e => (e as HTMLVideoElement).videoWidth > 0).length)).toBeGreaterThanOrEqual(2);
    const share = a.locator('button[data-lk-source="screen_share"]');
    await share.click(); await expect(share).toHaveAttribute('data-lk-enabled', 'true');
    await expect(b.locator('[data-lk-source="screen_share"] video').first()).toBeVisible();
    await share.click();
    await microphone(b).click();
    await expect(microphone(b)).toHaveAttribute('data-lk-enabled', 'false');
    const metrics = await b.evaluate(async () => {
      const peers = (window as unknown as { checkpointTestPeers: RTCPeerConnection[] }).checkpointTestPeers;
      const results = [];
      for (const peer of peers) {
        const stats = await peer.getStats();
        for (const entry of stats.values()) {
          if (entry.type === 'candidate-pair' && entry.state === 'succeeded' && entry.nominated) {
            const remote = stats.get(entry.remoteCandidateId);
            results.push({ type: 'selected-pair', remoteAddress: remote.address, remotePort: remote.port, protocol: remote.protocol, roundTripTime: entry.currentRoundTripTime });
          }
          if (entry.type === 'inbound-rtp') results.push({ type: 'inbound-rtp', kind: entry.kind, packetsReceived: entry.packetsReceived, packetsLost: entry.packetsLost, bytesReceived: entry.bytesReceived, framesDecoded: entry.framesDecoded });
        }
      }
      return results;
    });
    const pairs = metrics.filter(m => m.type === 'selected-pair');
    expect(pairs.length).toBeGreaterThan(0);
    expect(pairs.every(p => p.remoteAddress === cfg.ip && [7881, 7882].includes(p.remotePort))).toBe(true);
    writeFileSync('../.dev/browser-media.json', JSON.stringify(metrics, null, 2));
    await two.setOffline(true);
    // Chromium offline emulation alone leaves established WebRTC/WS paths alive.
    // Close the real signaling socket as well; physical network loss is a manual test.
    await b.evaluate(() => (window as unknown as { checkpointTestSockets: WebSocket[] }).checkpointTestSockets.forEach(socket => socket.close(4000, 'test interruption')));
    await expect(b.locator('.session-controls [role=status]')).toContainText('Reconnecting', { timeout: 15000 });
    await b.waitForTimeout(3000); await two.setOffline(false);
    await expect(b.locator('.session-controls [role=status]')).toContainText('connected', { timeout: 30000 });
    await expect(b.locator('.role')).toHaveText('Presenter');
    await expect(microphone(b)).toHaveAttribute('data-lk-enabled', 'false');
    await expect(camera(b)).toHaveAttribute('data-lk-enabled', 'true');
    await expect.poll(() => a.locator('video').evaluateAll(elements => elements.filter(e => (e as HTMLVideoElement).videoWidth > 0).length)).toBeGreaterThanOrEqual(2);
    await a.screenshot({ path: '../.dev/meeting-desktop.png' });
  } finally { await one.close(); await two.close(); }
});

test('webinar: grant and revoke publishing, devices stay off on promotion', async ({ browser }) => {
  const hostContext = await browser.newContext({ permissions: ['camera', 'microphone'] });
  const viewerContext = await browser.newContext({ permissions: ['camera', 'microphone'] });
  try {
    const host = await hostContext.newPage(), viewer = await viewerContext.newPage();
    await join(host, 'host', 'Browser host'); await join(viewer, 'viewer', 'Browser viewer');
    await expect(viewer.locator('.role')).toHaveText('Viewer');
    await expect(camera(viewer)).toHaveCount(0);
    await host.getByText('Manage speakers', { exact: true }).click();
    await host.getByRole('button', { name: 'Invite to speak' }).click();
    await expect(viewer.locator('.role')).toHaveText('Presenter');
    await expect(microphone(viewer)).toHaveAttribute('data-lk-enabled', 'false');
    await camera(viewer).click(); await expect(camera(viewer)).toHaveAttribute('data-lk-enabled', 'true');
    await host.getByRole('button', { name: 'Move to audience' }).click();
    await expect(viewer.locator('.role')).toHaveText('Viewer');
    await expect(camera(viewer)).toHaveCount(0);
  } finally { await hostContext.close(); await viewerContext.close(); }
});

test('Observer is independent, media unknown, and stale browser data becomes unknown', async ({ page }) => {
  await page.goto(observer);
  await expect(page.getByRole('heading', { name: 'Session services are responding' })).toBeVisible();
  await expect(page.locator('article').last().locator('.badge')).toHaveText('unknown');
  await page.screenshot({ path: '../.dev/observer-desktop.png' });
  await page.route('**/api/health', route => route.abort());
  await expect(page.getByRole('heading', { name: 'Waiting for fresh measurements' })).toBeVisible({ timeout: 22000 });
  await expect(page.locator('.badge.operational')).toHaveCount(0);
});

test('phone-sized join page fits the viewport', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(base, { waitUntil: 'networkidle' });
  await expect(page.getByRole('button', { name: 'Join session' })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: '../.dev/join-mobile.png' });
});
