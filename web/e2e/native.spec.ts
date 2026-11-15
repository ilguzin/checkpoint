import { test, expect, type Page } from '@playwright/test';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { createConnection } from 'node:net';

// Explicit opt-in: M0A's normal browser suite never needs the native fixture.
const enabled = process.env.CHECKPOINT_NATIVE_TEST === '1';
const accessPath = process.env.CHECKPOINT_NATIVE_ACCESS || '../.dev/native/access.json';
const cfg = enabled ? JSON.parse(readFileSync(accessPath, 'utf8')) : {};
test.skip(!enabled, 'Run scripts/native-smoke.sh or opt in with a private VPS access file');

type TestWindow = Window & { nativePeers: RTCPeerConnection[]; reconnects: number };

async function join(page: Page, name: string, relay: boolean) {
  await page.addInitScript(forceRelay => {
    const w = window as unknown as TestWindow;
    w.nativePeers = [];
    w.reconnects = 0;
    window.RTCPeerConnection = new Proxy(window.RTCPeerConnection, {
      construct(target, args) {
        if (forceRelay) args[0] = { ...args[0], iceTransportPolicy: 'relay' };
        const peer = Reflect.construct(target, args) as RTCPeerConnection;
        if (forceRelay) {
          const setConfiguration = peer.setConfiguration.bind(peer);
          peer.setConfiguration = config => setConfiguration({ ...config, iceTransportPolicy: 'relay' });
        }
        w.nativePeers.push(peer);
        return peer;
      },
    });
  }, relay);
  await page.goto(cfg.publicURL);
  await page.getByLabel('Your name').fill(name);
  await page.getByLabel('Room ID or link', { exact: true }).fill(cfg.meeting);
  await page.getByRole('button', { name: 'Join session' }).click();
  await expect(page.locator('.session-controls [role=status]')).toContainText('connected', { timeout: 45000 });
  await page.evaluate(() => {
    const status = document.querySelector('.session-controls [role=status]')!;
    new MutationObserver(() => {
      if (status.textContent?.includes('Reconnecting')) (window as unknown as TestWindow).reconnects++;
    }).observe(status, { childList: true, subtree: true, characterData: true });
  });
}

async function stats(page: Page) {
  return page.evaluate(async () => {
    const result: { pairs: { candidateType: string; relayProtocol?: string; url?: string; remotePort: number }[];
      frames: number; audioPackets: number; reconnects: number } = {
        pairs: [], frames: 0, audioPackets: 0, reconnects: (window as unknown as TestWindow).reconnects,
      };
    for (const peer of (window as unknown as TestWindow).nativePeers) {
      const report = await peer.getStats();
      for (const entry of report.values()) {
        if (entry.type === 'transport' && entry.selectedCandidatePairId) {
          const pair = report.get(entry.selectedCandidatePairId);
          const local = report.get(pair.localCandidateId);
          const remote = report.get(pair.remoteCandidateId);
          result.pairs.push({ candidateType: local.candidateType, relayProtocol: local.relayProtocol,
            url: local.url, remotePort: remote.port });
        }
        if (entry.type === 'inbound-rtp') {
          result.frames += entry.framesDecoded || 0;
          if (entry.kind === 'audio') result.audioPackets += entry.packetsReceived || 0;
        }
      }
    }
    return result;
  });
}

test('native HTTPS routes: Observer is public and management API is not exposed', async ({ page }) => {
  await page.goto(cfg.observerURL);
  await expect(page.getByRole('heading', { name: 'Session services are responding' })).toBeVisible();
  const signal = `https://${cfg.rtc}`;
  expect((await page.request.get(`${signal}/twirp/livekit.RoomService/ListRooms`)).status()).toBe(404);
  expect((await page.request.get(`${signal}/rtc/validate`)).status()).toBe(401);
  if (cfg.ip) {
    for (const port of [7880, 5349, 8443, 8444, 18180, 18181, 18182, 30000]) {
      const reachable = await new Promise<boolean>(resolve => {
        const connection = createConnection({ host: cfg.ip, port });
        connection.setTimeout(500);
        connection.on('connect', () => { connection.destroy(); resolve(true); });
        connection.on('error', () => { connection.destroy(); resolve(false); });
        connection.on('timeout', () => { connection.destroy(); resolve(false); });
      });
      expect(reachable, `Internal TCP port ${port} is exposed`).toBe(false);
    }
  }
});

for (const relay of [false, true]) {
  test(`native media: ${relay ? 'forced TURN TLS 443' : 'direct ICE'}`, async ({ browser }) => {
    const one = await browser.newContext({ permissions: ['camera', 'microphone'] });
    const two = await browser.newContext({ permissions: ['camera', 'microphone'] });
    try {
      const a = await one.newPage(), b = await two.newPage();
      console.log(`media ${relay ? 'relay' : 'direct'}: joining`);
      await join(a, 'Native A', relay); await join(b, 'Native B', relay);
      console.log('media: enabling devices');
      for (const page of [a, b]) {
        await page.locator('button[data-lk-source="camera"]').click();
        await page.locator('button[data-lk-source="microphone"]').click();
      }
      console.log('media: checking RTP');
      for (const page of [a, b]) {
        await expect.poll(async () => (await stats(page)).frames).toBeGreaterThan(10);
        await expect.poll(async () => (await stats(page)).audioPackets).toBeGreaterThan(10);
        const selected = (await stats(page)).pairs;
        expect(selected.length).toBeGreaterThan(0);
        for (const pair of selected) {
          expect(pair.remotePort).toBe(7882);
          if (relay) {
            expect(pair.candidateType).toBe('relay');
            expect(pair.relayProtocol).toBe('tls');
            expect(pair.url).toBe(`turns:${cfg.turn}:443?transport=tcp`);
          } else {
            expect(pair.candidateType).not.toBe('relay');
          }
        }
      }
      console.log('media: sharing screen');
      await a.bringToFront();
      await a.screenshot({ path: `../.dev/ui-review/${relay ? 'relay' : 'direct'}-before-share.png` });
      await a.locator('button[data-lk-source="screen_share"]').click({ timeout: 10000 });
      console.log('media: screen button clicked');
      await expect(b.locator('[data-lk-source="screen_share"] video').first()).toBeVisible();
      // Local harness only: replace the certificate and gracefully reload Nginx
      // while a real relay call is active. Public ACME renewal is a separate check.
      if (relay && process.env.CHECKPOINT_NATIVE_INSECURE === '1') {
        const before = await stats(b);
        const nonce = String(Date.now());
        writeFileSync('../.dev/native/reload-request', nonce);
        await expect.poll(() => existsSync('../.dev/native/reload-done') ? readFileSync('../.dev/native/reload-done', 'utf8') : '').toBe(nonce);
        await expect.poll(async () => (await stats(b)).frames).toBeGreaterThan(before.frames + 20);
        expect((await stats(b)).reconnects).toBe(0);
        await expect(b.locator('.session-controls [role=status]')).toContainText('connected');
      }
      if (!relay && process.env.CHECKPOINT_NATIVE_INSECURE === '1') {
        for (const service of ['core', 'signal']) {
          const before = await stats(b);
          const nonce = `${service}-${Date.now()}`;
          writeFileSync('../.dev/native/restart-request', `${service}:${nonce}`);
          if (service === 'core') {
            await expect.poll(async () => (await (await b.request.get(`${cfg.observerURL}/api/health`)).json()).core.state).toBe('outage');
            await expect.poll(async () => (await stats(b)).frames).toBeGreaterThan(before.frames + 20);
          }
          await expect.poll(() => existsSync('../.dev/native/restart-done') ? readFileSync('../.dev/native/restart-done', 'utf8') : '', { timeout: 30000 }).toBe(nonce);
          await expect(b.locator('.session-controls [role=status]')).toHaveText(/^connected/, { timeout: 30000 });
          await expect.poll(async () => (await stats(b)).audioPackets).toBeGreaterThan(before.audioPackets + 20);
          await expect.poll(async () => (await stats(b)).frames).toBeGreaterThan(before.frames + 30);
          await expect(b.locator('button[data-lk-source="microphone"]')).toHaveAttribute('data-lk-enabled', 'true');
        }
      }
      console.log('media: leaving');
      writeFileSync(`../.dev/native/${relay ? 'relay' : 'direct'}-stats.json`, JSON.stringify(await stats(b), null, 2));
      for (const page of [a, b]) { await page.getByRole('button', { name: 'Leave', exact: true }).click(); await expect(page.getByRole('heading', { name: 'Join a room' })).toBeVisible(); }
    } finally { await one.close(); await two.close(); }
  });
}
