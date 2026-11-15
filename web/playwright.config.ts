import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './e2e', timeout: 90000, expect: { timeout: 15000 }, workers: 1,
  reporter: 'list', outputDir: '../.dev/browser-results',
  use: { headless: true, viewport: { width: 1280, height: 900 },
    ignoreHTTPSErrors: process.env.CHECKPOINT_NATIVE_INSECURE === '1',
    launchOptions: { args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream', '--auto-select-desktop-capture-source=Entire screen', '--enable-usermedia-screen-capturing',
      ...(process.env.CHECKPOINT_AUDIO_FIXTURE ? ['--use-file-for-fake-audio-capture='+process.env.CHECKPOINT_AUDIO_FIXTURE] : []),
      ...(process.env.CHECKPOINT_NATIVE_INSECURE === '1' ? ['--ignore-certificate-errors'] : [])] },
    trace: 'off', screenshot: 'off', video: 'off',
  },
});
