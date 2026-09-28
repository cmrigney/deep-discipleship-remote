#!/usr/bin/env node
// End-to-end test: real relay + the Chrome build of the extension (unpacked,
// in Chromium) + test/mock-player.html + the phone page in a mobile viewport.
//
//   cd e2e && npm install && npx playwright install chromium && node run.mjs
//
// Screenshots are written to e2e/out/.
import { chromium, devices } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
import { createServer } from 'node:http';
import { readFileSync, mkdirSync, rmSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..');
const out = join(here, 'out');
const RELAY_PORT = 8099;
const PAGE_PORT = 8098;
const PASSWORD = 'e2e-test-password';

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

const step = (s) => console.log(`\n▶ ${s}`);
const ok = (s) => console.log(`  ✓ ${s}`);

// ---- Build ----
step('build relay + extension');
const relayBin = join(mkdtempSync(join(tmpdir(), 'ddr-')), 'relay');
execFileSync('go', ['build', '-o', relayBin, './cmd/relay'], { cwd: join(repo, 'relay'), stdio: 'inherit' });
execFileSync('node', ['scripts/build.mjs', 'chrome', '--dev'], { cwd: join(repo, 'extension'), stdio: 'inherit' });

// ---- Start relay ----
const relay = spawn(relayBin, ['serve'], {
  env: { ...process.env, RELAY_PASSWORD: PASSWORD, RELAY_LISTEN: `127.0.0.1:${RELAY_PORT}`, RELAY_TRUST_CF_HEADERS: 'false' },
  stdio: ['ignore', 'inherit', 'inherit'],
});
// ---- Serve the mock player ----
const pageServer = createServer((req, res) => {
  res.setHeader('Content-Type', 'text/html');
  res.end(readFileSync(join(repo, 'extension', 'test', 'mock-player.html')));
}).listen(PAGE_PORT, '127.0.0.1');

let context;
async function cleanup() {
  await context?.close().catch(() => {});
  relay.kill();
  pageServer.close();
}

async function waitForHttp(url, ms = 10000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    try { if ((await fetch(url)).ok) return; } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`timeout waiting for ${url}`);
}

try {
  await waitForHttp(`http://127.0.0.1:${RELAY_PORT}/healthz`);

  // ---- Launch Chromium with the extension ----
  step('launch Chromium with the extension');
  const extPath = join(repo, 'extension', 'dist', 'chrome');
  context = await chromium.launchPersistentContext(mkdtempSync(join(tmpdir(), 'ddr-profile-')), {
    channel: 'chromium',
    headless: true,
    viewport: { width: 1280, height: 720 },
    args: [`--disable-extensions-except=${extPath}`, `--load-extension=${extPath}`, '--autoplay-policy=no-user-gesture-required'],
  });
  let [sw] = context.serviceWorkers();
  if (!sw) sw = await context.waitForEvent('serviceworker');
  const extId = sw.url().split('/')[2];
  ok(`extension loaded (${extId})`);

  // ---- Popup: configure and turn on ----
  step('configure extension via popup');
  const popup = await context.newPage();
  await popup.goto(`chrome-extension://${extId}/popup/popup.html`);
  await popup.fill('#relay-url', `ws://127.0.0.1:${RELAY_PORT}`);
  await popup.fill('#password', PASSWORD);
  await popup.click('button[type=submit]');
  await popup.locator('#form-msg').getByText('Saved').waitFor();
  assert.equal(await popup.inputValue('#relay-url'), `ws://127.0.0.1:${RELAY_PORT}/ws`);
  await popup.locator('#enabled').check();
  await popup.locator('#status-text', { hasText: 'Connected' }).waitFor({ timeout: 10000 });
  ok('popup shows Connected');

  // ---- Player tab ----
  step('open mock player');
  const player = await context.newPage();
  await player.goto(`http://localhost:${PAGE_PORT}/mock-player.html`);
  const videoPaused = () => player.evaluate(() => document.querySelector('video.vjs-tech').paused);
  const overlayShown = () => player.evaluate(() => !!document.querySelector('.video-js [data-dd-remote-overlay]'));

  // ---- Phone ----
  step('phone logs in');
  const phoneCtx = await chromium.launch({ headless: true }).then((b) => b.newContext({ ...devices['iPhone 13'] }));
  const phone = await phoneCtx.newPage();
  await phone.goto(`http://127.0.0.1:${RELAY_PORT}/`);
  await phone.screenshot({ path: join(out, '1-phone-login.png') });

  await phone.fill('#password', 'wrong-password');
  await phone.click('#login-btn');
  await phone.locator('#login-error', { hasText: 'Wrong password' }).waitFor();
  ok('wrong password rejected');

  await phone.fill('#password', PASSWORD);
  await phone.click('#login-btn');
  await phone.locator('#status-text', { hasText: 'Church computer connected' }).waitFor({ timeout: 10000 });
  await phone.locator('#status-pill.pill-green').waitFor();
  ok('phone sees the church computer with a video');
  await phone.screenshot({ path: join(out, '2-phone-connected.png') });

  await popup.locator('#fact-phones', { hasText: /^1$/ }).waitFor({ timeout: 5000 });
  ok('popup shows 1 phone connected');

  // ---- Play / pause ----
  step('play / pause');
  assert.equal(await videoPaused(), true);
  await phone.click('#play-btn');
  await player.waitForFunction(() => !document.querySelector('video.vjs-tech').paused);
  await phone.locator('#play-label', { hasText: 'Pause' }).waitFor();
  await player.waitForFunction(() => document.querySelector('.vjs-play-control').classList.contains('vjs-playing'));
  ok('video playing; phone button says Pause; player button in sync');
  await phone.screenshot({ path: join(out, '3-phone-playing.png') });

  await phone.click('#play-btn');
  await player.waitForFunction(() => document.querySelector('video.vjs-tech').paused);
  await phone.locator('#play-label', { hasText: 'Play' }).waitFor();
  ok('video paused');

  // Someone clicks on the computer directly: the phone must follow.
  await player.click('.vjs-play-control');
  await phone.locator('#play-label', { hasText: 'Pause' }).waitFor();
  await player.click('.vjs-play-control');
  await phone.locator('#play-label', { hasText: 'Play' }).waitFor();
  ok('phone follows clicks made on the computer');

  // ---- Rewind ----
  step('back 10s');
  await player.evaluate(() => { document.querySelector('video.vjs-tech').currentTime = 30; });
  await phone.click('#rewind-btn');
  await player.waitForFunction(() => Math.abs(document.querySelector('video.vjs-tech').currentTime - 20) < 1);
  await phone.locator('#status-sub', { hasText: '0:20 / 5:00' }).waitFor();
  ok('rewound 30s → 20s; phone shows 0:20 / 5:00');

  // ---- Timer ----
  step('timer');
  await phone.click('#play-btn'); // start playing so we can check the auto-pause
  await player.waitForFunction(() => !document.querySelector('video.vjs-tech').paused);
  assert.equal(await phone.locator('#setup-value').textContent(), '10:00', 'default is 10 minutes');
  await phone.click('#timer-start');
  await player.waitForFunction(() => document.querySelector('video.vjs-tech').paused);
  assert.equal(await overlayShown(), true);
  await phone.locator('#timer-running').waitFor();
  const rem = await phone.locator('#timer-remaining').textContent();
  assert.match(rem, /^(10:00|09:5\d)$/);
  ok(`timer started, video auto-paused, overlay shown, phone shows ${rem}`);
  await player.screenshot({ path: join(out, '4-player-overlay.png') });
  await phone.screenshot({ path: join(out, '5-phone-timer.png') });

  await phone.click('#timer-plus');
  await phone.waitForFunction(() => /^1[01]:/.test(document.getElementById('timer-remaining').textContent));
  ok('+1 min');
  await phone.click('#timer-minus');
  await phone.waitForFunction(() => /^(10:00|09:)/.test(document.getElementById('timer-remaining').textContent));
  ok('−1 min');

  await phone.click('#timer-pause');
  await phone.locator('#timer-pause', { hasText: 'Resume' }).waitFor();
  const frozen = await phone.locator('#timer-remaining').textContent();
  await phone.waitForTimeout(1500);
  assert.equal(await phone.locator('#timer-remaining').textContent(), frozen, 'paused timer is frozen');
  await phone.click('#timer-pause');
  await phone.locator('#timer-pause', { hasText: 'Pause' }).waitFor();
  ok('pause / resume');

  // Fullscreen: overlay stays inside the fullscreen element.
  await player.click('.vjs-fullscreen-control');
  await player.waitForFunction(() => !!document.fullscreenElement);
  const inFs = await player.evaluate(() => document.fullscreenElement.contains(document.querySelector('[data-dd-remote-overlay]')));
  assert.equal(inFs, true);
  await player.screenshot({ path: join(out, '6-player-overlay-fullscreen.png') });
  await player.evaluate(() => document.exitFullscreen());
  ok('overlay visible in fullscreen');

  // Resuming playback hides the overlay.
  await phone.click('#play-btn');
  await player.waitForFunction(() => !document.querySelector('[data-dd-remote-overlay]'));
  await phone.locator('#timer-setup').waitFor();
  // Regression: video.js mutates the DOM constantly; that must not bring back a stopped overlay.
  await player.evaluate(() => document.querySelector('.video-js').appendChild(document.createElement('div')));
  await player.waitForTimeout(400);
  assert.equal(await overlayShown(), false, 'overlay must stay hidden after DOM changes');
  ok('playing the video hides the timer (and it stays hidden)');

  // Start + stop.
  await phone.click('#timer-start');
  await player.waitForFunction(() => !!document.querySelector('[data-dd-remote-overlay]'));
  await phone.click('#timer-stop');
  await player.waitForFunction(() => !document.querySelector('[data-dd-remote-overlay]'));
  await phone.locator('#timer-setup').waitFor();
  ok('stop timer');

  // Short timer reaches "Time's up".
  await phone.click('.chip[data-min="5"]');
  assert.equal(await phone.locator('#setup-value').textContent(), '05:00');
  for (let i = 0; i < 4; i++) await phone.click('#setup-minus');
  assert.equal(await phone.locator('#setup-value').textContent(), '01:00');
  await phone.click('#timer-start');
  await phone.locator('#timer-running').waitFor();
  await phone.click('#timer-minus'); // 1:00 - 1:00 = done
  await phone.locator('#timer-label', { hasText: 'Time’s up' }).waitFor();
  await player.waitForFunction(() => !!document.querySelector('[data-dd-remote-overlay]'));
  await player.screenshot({ path: join(out, '7-player-times-up.png') });
  await phone.screenshot({ path: join(out, '8-phone-times-up.png') });
  await phone.click('#timer-stop');
  ok('time’s up state, then stop');

  // ---- Page reload keeps working ----
  step('player tab reload');
  await player.reload();
  await phone.locator('#status-pill.pill-green').waitFor({ timeout: 10000 });
  await phone.click('#play-btn');
  await player.waitForFunction(() => !document.querySelector('video.vjs-tech').paused);
  await phone.click('#play-btn');
  ok('controls work after reload');

  // ---- No video ----
  step('player tab closed');
  await player.close();
  await phone.locator('#hint', { hasText: 'no video is open' }).waitFor({ timeout: 10000 });
  assert.equal(await phone.locator('#play-btn').isDisabled(), true);
  ok('phone says no video; controls disabled');
  await phone.screenshot({ path: join(out, '9-phone-no-video.png') });

  // ---- Extension off ----
  step('extension turned off');
  await popup.locator('#enabled').uncheck();
  await phone.locator('#status-text', { hasText: 'Church computer not connected' }).waitFor({ timeout: 10000 });
  await popup.locator('#status-text', { hasText: 'Off' }).waitFor();
  ok('phone sees the computer disconnect; popup shows Off');

  // ---- Wrong password in extension ----
  step('extension with wrong password');
  await popup.fill('#password', 'nope-nope');
  await popup.click('button[type=submit]');
  await popup.locator('#enabled').check();
  await popup.locator('#status-text', { hasText: 'Wrong password' }).waitFor({ timeout: 10000 });
  await popup.screenshot({ path: join(out, '10-popup-wrong-password.png') });
  ok('popup shows Wrong password');
  await popup.fill('#password', PASSWORD);
  await popup.click('button[type=submit]');
  await popup.locator('#status-text', { hasText: 'Connected' }).waitFor({ timeout: 10000 });
  await popup.screenshot({ path: join(out, '11-popup-connected.png') });
  ok('fixing the password reconnects');

  await phoneCtx.browser().close();
  console.log('\nAll E2E checks passed. Screenshots in e2e/out/');
} catch (err) {
  console.error('\nE2E FAILED:', err);
  process.exitCode = 1;
} finally {
  await cleanup();
}
