// Content script for the Lifeway player page. Finds the video, carries out
// commands from the background, and draws the discussion-timer overlay.
// It must not open network connections: the page's CSP blocks them.

// Everything Lifeway-specific lives here (verified 2026-09-24, see PLAN.md §0).
const SEL = Object.freeze({
  video: 'video.vjs-tech',
  container: '.video-js',
  playButton: 'button.vjs-play-control',
});

const HEARTBEAT_MS = 5000;
const MAX_TIMER_SEC = 3600;

let enabled = false;
let chime = false;
let port = null;
let reconnectPortTimer = null;
let heartbeat = null;
let observer = null;
let video = null;
let timer = null;   // { durationSec, endsAt, paused, remainingAtPause, finished }
let overlay = null; // { host, root, time, label, bar }
let tickTimer = null;

// ---------- Port to background ----------

function post(msg) {
  if (!port) return;
  try { port.postMessage(msg); } catch { /* reconnect handles it */ }
}

function connectPort() {
  clearTimeout(reconnectPortTimer);
  if (port || !enabled) return;
  try {
    port = api.runtime.connect({ name: PORT_NAME });
  } catch {
    // Extension was reloaded or updated; this copy of the script is orphaned.
    teardown();
    return;
  }
  port.onMessage.addListener(onCommand);
  port.onDisconnect.addListener(() => {
    port = null;
    // The background may have restarted (Chrome service worker); reconnect.
    if (enabled) reconnectPortTimer = setTimeout(connectPort, 1000);
  });
  sendState();
}

function disconnectPort() {
  clearTimeout(reconnectPortTimer);
  if (port) {
    const p = port;
    port = null;
    try { p.disconnect(); } catch { /* already gone */ }
  }
}

// ---------- Video tracking ----------

const VIDEO_EVENTS = ['play', 'pause', 'seeked', 'loadedmetadata', 'durationchange', 'emptied', 'ended'];

function onVideoEvent(ev) {
  // Resuming the video means discussion is over: hide the timer.
  if (ev.type === 'play' && timer) stopTimer();
  sendState();
}

function findVideo() {
  const v = document.querySelector(SEL.video);
  if (v === video) {
    if (overlay) ensureOverlayAttached();
    return;
  }
  if (video) VIDEO_EVENTS.forEach((t) => video.removeEventListener(t, onVideoEvent));
  video = v;
  if (video) VIDEO_EVENTS.forEach((t) => video.addEventListener(t, onVideoEvent));
  if (!video && timer) stopTimer();
  if (overlay) ensureOverlayAttached();
  sendState();
}

let findScheduled = false;
function scheduleFind() {
  if (findScheduled) return;
  findScheduled = true;
  setTimeout(() => { findScheduled = false; findVideo(); }, 100);
}

function finite(n) {
  return Number.isFinite(n) && n >= 0 ? n : 0;
}

function timerRemaining() {
  if (!timer) return 0;
  if (timer.paused) return timer.remainingAtPause;
  return Math.max(0, (timer.endsAt - Date.now()) / 1000);
}

function sendState() {
  if (!port) return;
  post({
    type: 'state',
    videoFound: !!video,
    paused: video ? video.paused : true,
    currentTime: video ? finite(video.currentTime) : 0,
    duration: video ? finite(video.duration) : 0,
    timer: timer ? {
      running: true,
      paused: timer.paused,
      endsAt: Math.round(timer.paused ? Date.now() + timer.remainingAtPause * 1000 : timer.endsAt),
      remainingSec: Math.round(timerRemaining() * 10) / 10,
      durationSec: timer.durationSec,
    } : null,
  });
}

// ---------- Commands ----------

function waitFor(pred, ms) {
  return new Promise((resolve) => {
    const start = Date.now();
    (function check() {
      if (pred()) resolve(true);
      else if (Date.now() - start >= ms) resolve(false);
      else setTimeout(check, 50);
    })();
  });
}

// Plays or pauses. Clicks video.js's own button first so its UI stays in sync,
// then falls back to the media element API.
async function setPaused(wantPaused) {
  if (!video) return false;
  if (video.paused === wantPaused) return true;
  const btn = document.querySelector(SEL.playButton);
  if (btn) btn.click();
  if (await waitFor(() => video && video.paused === wantPaused, 1500)) return true;
  if (!video) return false;
  if (wantPaused) {
    video.pause();
  } else {
    try { await video.play(); } catch { /* reported below */ }
  }
  return !!video && video.paused === wantPaused;
}

async function onCommand(msg) {
  if (!msg || typeof msg !== 'object') return;
  const ack = (ok, error) => {
    if (msg.id) post({ type: 'ack', id: msg.id, ok, ...(error ? { error } : {}) });
  };

  if (msg.type === 'request_state') {
    findVideo();
    sendState();
    return;
  }
  if (!enabled) return ack(false, 'disabled');
  if (!video) findVideo();
  if (!video) return ack(false, 'no_video');

  switch (msg.type) {
    case 'play': {
      const ok = await setPaused(false);
      ack(ok, ok ? undefined : 'play_blocked');
      break;
    }
    case 'pause': {
      const ok = await setPaused(true);
      ack(ok, ok ? undefined : 'failed');
      break;
    }
    case 'toggle': {
      const wantPaused = !video.paused;
      const ok = await setPaused(wantPaused);
      ack(ok, ok ? undefined : (wantPaused ? 'failed' : 'play_blocked'));
      break;
    }
    case 'seek_relative': {
      const secs = Number(msg.seconds) || 0;
      const max = Number.isFinite(video.duration) ? video.duration : Infinity;
      video.currentTime = Math.min(max, Math.max(0, video.currentTime + secs));
      ack(true);
      break;
    }
    case 'timer_start':
      if (msg.pauseVideo !== false) await setPaused(true);
      startTimer(Number(msg.durationSec) || 600);
      ack(true);
      break;
    case 'timer_adjust':
      if (!timer) return ack(false, 'no_timer');
      adjustTimer(Number(msg.deltaSec) || 0);
      ack(true);
      break;
    case 'timer_pause':
      if (!timer) return ack(false, 'no_timer');
      if (!timer.paused) {
        timer.remainingAtPause = timerRemaining();
        timer.paused = true;
      }
      ack(true);
      break;
    case 'timer_resume':
      if (!timer) return ack(false, 'no_timer');
      if (timer.paused) {
        timer.endsAt = Date.now() + timer.remainingAtPause * 1000;
        timer.paused = false;
      }
      ack(true);
      break;
    case 'timer_stop':
      stopTimer();
      ack(true);
      break;
    default:
      ack(false, 'unknown_command');
      return;
  }
  renderOverlay();
  sendState();
}

// ---------- Timer ----------

function startTimer(durationSec) {
  const d = Math.min(MAX_TIMER_SEC, Math.max(1, Math.round(durationSec)));
  timer = { durationSec: d, endsAt: Date.now() + d * 1000, paused: false, remainingAtPause: d, finished: false };
  showOverlay();
}

function adjustTimer(deltaSec) {
  const remaining = Math.min(MAX_TIMER_SEC, Math.max(0, timerRemaining() + deltaSec));
  if (timer.paused) timer.remainingAtPause = remaining;
  else timer.endsAt = Date.now() + remaining * 1000;
  timer.durationSec = Math.min(MAX_TIMER_SEC, Math.max(1, timer.durationSec + deltaSec, Math.ceil(remaining)));
  timer.finished = remaining <= 0;
}

function stopTimer() {
  timer = null;
  removeOverlay();
  sendState();
}

function tick() {
  if (!timer) return;
  if (!timer.finished && timerRemaining() <= 0) {
    timer.finished = true;
    if (chime) playChime();
    sendState();
  }
  ensureOverlayAttached();
  renderOverlay();
}

function playChime() {
  try {
    const Ctx = window.AudioContext || window.webkitAudioContext;
    const ctx = new Ctx();
    [[660, 0], [880, 0.35]].forEach(([freq, at]) => {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = 'sine';
      osc.frequency.value = freq;
      gain.gain.setValueAtTime(0.0001, ctx.currentTime + at);
      gain.gain.exponentialRampToValueAtTime(0.25, ctx.currentTime + at + 0.03);
      gain.gain.exponentialRampToValueAtTime(0.0001, ctx.currentTime + at + 0.9);
      osc.connect(gain).connect(ctx.destination);
      osc.start(ctx.currentTime + at);
      osc.stop(ctx.currentTime + at + 1);
    });
    setTimeout(() => ctx.close(), 2000);
  } catch { /* sound is optional */ }
}

// ---------- Overlay ----------

const OVERLAY_CSS = `
  :host { all: initial; }
  .wrap {
    position: absolute; inset: 0;
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    background: rgba(0, 0, 0, 0.78);
    -webkit-backdrop-filter: blur(4px); backdrop-filter: blur(4px);
    color: #fff;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    container-type: size;
    pointer-events: none;
    animation: fade-in .4s ease;
  }
  .label {
    font-size: max(14px, min(4.2cqw, 7cqh));
    letter-spacing: .22em; text-transform: uppercase; opacity: .85;
    margin-bottom: 1cqh;
  }
  .time {
    font-size: max(40px, min(24cqw, 42cqh));
    font-weight: 700; line-height: 1;
    font-variant-numeric: tabular-nums;
  }
  .bar {
    width: 50cqw; height: max(4px, 0.9cqh);
    margin-top: 4cqh; border-radius: 99px; overflow: hidden;
    background: rgba(255, 255, 255, .2);
  }
  .bar > i { display: block; height: 100%; background: #fff; transition: width .25s linear; }
  .low .time { color: #fbbf24; }
  .low .bar > i { background: #fbbf24; }
  .done .time { color: #fca5a5; animation: pulse 1.2s ease-in-out infinite; }
  .done .bar { visibility: hidden; }
  .paused .time { opacity: .6; }
  .close {
    position: absolute; top: 12px; right: 12px;
    width: 36px; height: 36px; border-radius: 50%;
    border: 0; background: rgba(255, 255, 255, .15); color: #fff;
    font: 22px/36px sans-serif; cursor: pointer; pointer-events: auto;
    opacity: .5;
  }
  .close:hover { opacity: 1; }
  @keyframes pulse { 50% { opacity: .45; } }
  @keyframes fade-in { from { opacity: 0; } }
`;

function fmt(sec) {
  const s = Math.max(0, Math.ceil(sec));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

function showOverlay() {
  if (!overlay) {
    const host = document.createElement('div');
    host.setAttribute('data-dd-remote-overlay', '');
    host.style.cssText = 'position:absolute;inset:0;z-index:2147483647;pointer-events:none;';
    const root = host.attachShadow({ mode: 'closed' });
    root.innerHTML = `<style>${OVERLAY_CSS}</style>
      <div class="wrap" part="wrap">
        <div class="label">Group Discussion</div>
        <div class="time">0:00</div>
        <div class="bar"><i></i></div>
        <button class="close" type="button" title="Hide timer" aria-label="Hide timer">×</button>
      </div>`;
    root.querySelector('.close').addEventListener('click', (e) => {
      e.stopPropagation();
      stopTimer();
    });
    overlay = {
      host,
      wrap: root.querySelector('.wrap'),
      time: root.querySelector('.time'),
      label: root.querySelector('.label'),
      bar: root.querySelector('.bar > i'),
    };
  }
  ensureOverlayAttached();
  renderOverlay();
  clearInterval(tickTimer);
  tickTimer = setInterval(tick, 250);
}

// Keep the overlay inside the player container: that's the element that goes
// fullscreen, so the overlay stays visible there too.
function ensureOverlayAttached() {
  if (!overlay || !timer) return;
  const container = (video && video.closest(SEL.container)) || (video && video.parentElement);
  if (!container) {
    overlay.host.remove();
    return;
  }
  if (overlay.host.parentNode !== container) container.appendChild(overlay.host);
}

function renderOverlay() {
  if (!overlay || !timer) return;
  const rem = timerRemaining();
  const done = rem <= 0;
  overlay.time.textContent = done ? '0:00' : fmt(rem);
  overlay.label.textContent = done ? 'Time’s up' : (timer.paused ? 'Paused' : 'Group Discussion');
  overlay.bar.style.width = `${Math.max(0, Math.min(100, (rem / timer.durationSec) * 100))}%`;
  overlay.wrap.classList.toggle('done', done);
  overlay.wrap.classList.toggle('low', !done && rem <= 60);
  overlay.wrap.classList.toggle('paused', !done && timer.paused);
}

function removeOverlay() {
  clearInterval(tickTimer);
  tickTimer = null;
  if (overlay) overlay.host.remove();
  // Drop the reference so DOM-change handlers can't re-attach a stale overlay.
  overlay = null;
}

// ---------- Enable / disable ----------

function applyEnabled() {
  if (enabled) {
    if (!observer) {
      observer = new MutationObserver(scheduleFind);
      observer.observe(document.documentElement, { childList: true, subtree: true });
    }
    findVideo();
    connectPort();
    clearInterval(heartbeat);
    heartbeat = setInterval(sendState, HEARTBEAT_MS);
  } else {
    teardown();
  }
}

function teardown() {
  clearInterval(heartbeat);
  heartbeat = null;
  if (observer) { observer.disconnect(); observer = null; }
  timer = null;
  removeOverlay();
  disconnectPort();
}

api.storage.onChanged.addListener((changes, area) => {
  if (area !== 'local') return;
  if ('chime' in changes) chime = !!changes.chime.newValue;
  if ('enabled' in changes) {
    enabled = !!changes.enabled.newValue;
    applyEnabled();
  }
});

loadSettings().then((s) => {
  enabled = s.enabled;
  chime = s.chime;
  applyEnabled();
});
