// Deep Discipleship Remote — phone control page.
(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const STORE = { password: 'ddr.password', minutes: 'ddr.minutes', pauseOnTimer: 'ddr.pauseOnTimer' };
  const ACK_TIMEOUT_MS = 3000;
  const PING_MS = 20000;

  const store = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } },
    del(k) { try { localStorage.removeItem(k); } catch { /* private mode */ } },
  };

  // ---- State ----
  let ws = null;
  let password = store.get(STORE.password) || '';
  let rememberPassword = true;
  let authed = false;
  let stopReconnecting = false;
  let reconnectTimer = null;
  let backoffMs = 1000;
  let pingTimer = null;
  let presence = { players: 0, controllers: 0 };
  let player = null;        // last `state` message
  let playerAt = 0;         // performance.now() when it arrived
  let optimisticPaused = null; // shown until the player confirms
  const pending = new Map();   // command id -> { timer, el }
  let selectedMinutes = clampInt(parseInt(store.get(STORE.minutes) || '10', 10), 1, 60, 10);

  // ---- Helpers ----
  function clampInt(n, lo, hi, dflt) {
    return Number.isFinite(n) ? Math.min(hi, Math.max(lo, n)) : dflt;
  }

  function fmtClock(totalSec) {
    const s = Math.max(0, Math.ceil(totalSec));
    const m = Math.floor(s / 60);
    return `${String(m).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`;
  }

  function fmtMedia(sec) {
    if (!Number.isFinite(sec) || sec < 0) sec = 0;
    sec = Math.floor(sec);
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    const mm = h ? String(m).padStart(2, '0') : String(m);
    return (h ? `${h}:` : '') + `${mm}:${String(s).padStart(2, '0')}`;
  }

  function newId() {
    const a = new Uint8Array(8);
    crypto.getRandomValues(a);
    return Array.from(a, (b) => b.toString(16).padStart(2, '0')).join('');
  }

  function buzz() {
    if (navigator.vibrate) navigator.vibrate(15);
  }

  let toastTimer = null;
  function toast(text, bad = false) {
    const el = $('toast');
    el.textContent = text;
    el.classList.toggle('bad', bad);
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.hidden = true; }, bad ? 4000 : 1500);
  }

  function show(screen) {
    $('login').hidden = screen !== 'login';
    $('control').hidden = screen !== 'control';
  }

  // ---- Connection ----
  function wsUrl() {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${location.host}/ws`;
  }

  function connect() {
    clearTimeout(reconnectTimer);
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
    stopReconnecting = false;
    authed = false;
    render();

    const sock = new WebSocket(wsUrl());
    ws = sock;

    sock.onopen = () => {
      sock.send(JSON.stringify({ type: 'auth', role: 'controller', password, clientName: 'Phone' }));
    };

    sock.onmessage = (ev) => {
      let msg;
      try { msg = JSON.parse(ev.data); } catch { return; }
      handleMessage(msg);
    };

    sock.onclose = () => {
      if (ws !== sock) return;
      ws = null;
      authed = false;
      clearInterval(pingTimer);
      failAllPending();
      render();
      if (!stopReconnecting && password) scheduleReconnect();
    };
  }

  function scheduleReconnect() {
    clearTimeout(reconnectTimer);
    const delay = backoffMs * (0.8 + Math.random() * 0.4);
    backoffMs = Math.min(backoffMs * 2, 30000);
    reconnectTimer = setTimeout(connect, delay);
  }

  function disconnect() {
    stopReconnecting = true;
    clearTimeout(reconnectTimer);
    clearInterval(pingTimer);
    if (ws) { const s = ws; ws = null; s.close(); }
    authed = false;
  }

  function handleMessage(msg) {
    switch (msg.type) {
      case 'auth_ok':
        authed = true;
        backoffMs = 1000;
        if (rememberPassword) store.set(STORE.password, password);
        show('control');
        sendRaw({ type: 'request_state', id: newId() });
        clearInterval(pingTimer);
        pingTimer = setInterval(() => sendRaw({ type: 'ping' }), PING_MS);
        break;

      case 'auth_error': {
        stopReconnecting = true;
        const messages = {
          bad_password: 'Wrong password.',
          locked_out: 'Too many attempts. Try again in 15 minutes.',
          timeout: 'Couldn’t sign in. Please try again.',
          malformed: 'Couldn’t sign in. Please try again.',
        };
        if (msg.reason === 'bad_password') store.del(STORE.password);
        showLogin(messages[msg.reason] || 'Couldn’t sign in.');
        break;
      }

      case 'presence':
        presence = { players: msg.players | 0, controllers: msg.controllers | 0 };
        if (presence.players === 0) player = null;
        render();
        break;

      case 'state':
        player = msg;
        playerAt = performance.now();
        optimisticPaused = null;
        render();
        break;

      case 'ack': {
        const p = pending.get(msg.id);
        if (!p) break;
        clearTimeout(p.timer);
        pending.delete(msg.id);
        if (p.el) p.el.classList.remove('busy');
        if (!msg.ok) {
          optimisticPaused = null;
          render();
          toast(ackErrorText(msg.error), true);
        }
        break;
      }

      case 'error':
        if (msg.reason === 'rate_limited') toast('Slow down a little.', true);
        break;
    }
  }

  function ackErrorText(code) {
    return ({
      no_video: 'No video found on the church computer.',
      play_blocked: 'The browser blocked playback. Click play once on the church computer.',
      disabled: 'The extension is turned off on the church computer.',
      no_timer: 'No timer is running.',
    })[code] || 'The church computer couldn’t do that.';
  }

  function sendRaw(obj) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return false;
    ws.send(JSON.stringify(obj));
    return true;
  }

  // Sends a command that the player acknowledges; shows an error if no ack arrives.
  function command(type, extra = {}, el = null) {
    buzz();
    if (!authed) { toast('Not connected yet.', true); return false; }
    const id = newId();
    if (!sendRaw({ type, id, ...extra })) { toast('Not connected yet.', true); return false; }
    if (el) el.classList.add('busy');
    const timer = setTimeout(() => {
      pending.delete(id);
      if (el) el.classList.remove('busy');
      optimisticPaused = null;
      render();
      toast('Didn’t reach the church computer.', true);
    }, ACK_TIMEOUT_MS);
    pending.set(id, { timer, el });
    return true;
  }

  function failAllPending() {
    for (const [, p] of pending) {
      clearTimeout(p.timer);
      if (p.el) p.el.classList.remove('busy');
    }
    pending.clear();
  }

  // ---- Rendering ----
  function currentPosition() {
    if (!player) return 0;
    if (player.paused) return player.currentTime;
    return player.currentTime + (performance.now() - playerAt) / 1000;
  }

  function timerRemaining() {
    const t = player && player.timer;
    if (!t) return 0;
    if (t.paused) return t.remainingSec;
    return Math.max(0, t.remainingSec - (performance.now() - playerAt) / 1000);
  }

  function render() {
    const connected = !!(ws && ws.readyState === WebSocket.OPEN && authed);
    const hasPlayer = connected && presence.players > 0;
    const hasVideo = hasPlayer && player && player.videoFound;

    // Status pill + hint
    const pill = $('status-pill');
    let pillClass = 'pill-red';
    let pillText = 'Reconnecting…';
    let hint = '';
    if (connected && !hasPlayer) {
      pillClass = 'pill-amber';
      pillText = 'Church computer not connected';
      hint = 'Open the video on the church computer and make sure the extension is ON.';
    } else if (hasPlayer && !hasVideo) {
      pillClass = 'pill-amber';
      pillText = 'Church computer connected';
      hint = 'The extension is on, but no video is open on screen.';
    } else if (hasVideo) {
      pillClass = 'pill-green';
      pillText = 'Church computer connected';
    }
    pill.className = `pill ${pillClass}`;
    $('status-text').textContent = pillText;
    $('hint').textContent = hint;
    $('hint').hidden = !hint;
    $('status-sub').textContent = hasVideo
      ? `${player.paused ? 'Paused' : 'Playing'} · ${fmtMedia(currentPosition())} / ${fmtMedia(player.duration)}`
      : ' ';

    // Play button
    const paused = optimisticPaused !== null ? optimisticPaused : (!player || player.paused);
    const playBtn = $('play-btn');
    playBtn.disabled = !hasVideo;
    playBtn.classList.toggle('is-paused', paused);
    playBtn.classList.toggle('is-playing', !paused);
    $('play-label').textContent = paused ? 'Play' : 'Pause';
    $('rewind-btn').disabled = !hasVideo;

    // Timer
    const t = hasVideo ? player.timer : null;
    $('timer-setup').hidden = !!t;
    $('timer-running').hidden = !t;
    $('timer-start').disabled = !hasVideo;
    $('setup-value').textContent = fmtClock(selectedMinutes * 60);
    document.querySelectorAll('.chip').forEach((c) => {
      c.classList.toggle('selected', Number(c.dataset.min) === selectedMinutes);
    });
    if (t) {
      const rem = timerRemaining();
      const done = rem <= 0;
      const el = $('timer-remaining');
      el.textContent = done ? '00:00' : fmtClock(rem);
      el.classList.toggle('done', done);
      el.classList.toggle('low', !done && rem <= 60);
      $('timer-label').textContent = done ? 'Time’s up' : (t.paused ? 'paused' : 'remaining');
      const pct = t.durationSec > 0 ? Math.max(0, Math.min(100, (rem / t.durationSec) * 100)) : 0;
      $('timer-progress').style.width = `${pct}%`;
      $('timer-pause').textContent = t.paused ? 'Resume' : 'Pause';
      $('timer-pause').disabled = done;
    }
  }

  function showLogin(error) {
    authed = false;
    show('login');
    $('password').value = '';
    $('login-error').textContent = error || '';
    $('login-error').hidden = !error;
    $('login-btn').disabled = false;
    $('login-btn').textContent = 'Connect';
  }

  // ---- Wake lock ----
  let wakeLock = null;
  let wantWake = false;
  async function setWake(on) {
    wantWake = on;
    try {
      if (on && !wakeLock) {
        wakeLock = await navigator.wakeLock.request('screen');
        wakeLock.addEventListener('release', () => { wakeLock = null; renderWake(); });
      } else if (!on && wakeLock) {
        await wakeLock.release();
        wakeLock = null;
      }
    } catch {
      wantWake = false;
      toast('Couldn’t keep the screen on.', true);
    }
    renderWake();
  }
  function renderWake() {
    $('wake-btn').textContent = `Keep screen on: ${wakeLock ? 'on' : 'off'}`;
  }

  // ---- Events ----
  $('login-form').addEventListener('submit', (e) => {
    e.preventDefault();
    password = $('password').value;
    rememberPassword = $('remember').checked;
    if (!rememberPassword) store.del(STORE.password);
    if (!password) return;
    $('login-error').hidden = true;
    $('login-btn').disabled = true;
    $('login-btn').textContent = 'Connecting…';
    backoffMs = 1000;
    disconnect();
    connect();
  });

  $('play-btn').addEventListener('click', (e) => {
    const paused = optimisticPaused !== null ? optimisticPaused : (!player || player.paused);
    // Explicit play/pause (never toggle) so retries and double taps are harmless.
    if (command(paused ? 'play' : 'pause', {}, e.currentTarget)) {
      optimisticPaused = !paused;
      render();
    }
  });

  $('rewind-btn').addEventListener('click', (e) => command('seek_relative', { seconds: -10 }, e.currentTarget));

  document.querySelectorAll('.chip').forEach((chip) => {
    chip.addEventListener('click', () => {
      buzz();
      selectedMinutes = Number(chip.dataset.min);
      store.set(STORE.minutes, String(selectedMinutes));
      render();
    });
  });
  $('setup-minus').addEventListener('click', () => {
    buzz();
    selectedMinutes = clampInt(selectedMinutes - 1, 1, 60, 10);
    store.set(STORE.minutes, String(selectedMinutes));
    render();
  });
  $('setup-plus').addEventListener('click', () => {
    buzz();
    selectedMinutes = clampInt(selectedMinutes + 1, 1, 60, 10);
    store.set(STORE.minutes, String(selectedMinutes));
    render();
  });

  const pauseOnTimer = $('pause-on-timer');
  pauseOnTimer.checked = store.get(STORE.pauseOnTimer) !== 'false';
  pauseOnTimer.addEventListener('change', () => store.set(STORE.pauseOnTimer, String(pauseOnTimer.checked)));

  $('timer-start').addEventListener('click', (e) => command('timer_start', {
    durationSec: selectedMinutes * 60,
    pauseVideo: pauseOnTimer.checked,
  }, e.currentTarget));
  $('timer-minus').addEventListener('click', (e) => command('timer_adjust', { deltaSec: -60 }, e.currentTarget));
  $('timer-plus').addEventListener('click', (e) => command('timer_adjust', { deltaSec: 60 }, e.currentTarget));
  $('timer-pause').addEventListener('click', (e) => {
    const t = player && player.timer;
    command(t && t.paused ? 'timer_resume' : 'timer_pause', {}, e.currentTarget);
  });
  $('timer-stop').addEventListener('click', (e) => command('timer_stop', {}, e.currentTarget));

  $('forget-btn').addEventListener('click', () => {
    store.del(STORE.password);
    password = '';
    disconnect();
    showLogin('');
  });

  if ('wakeLock' in navigator) {
    $('wake-btn').hidden = false;
    renderWake();
    $('wake-btn').addEventListener('click', () => setWake(!wakeLock));
  }

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible') return;
    if (wantWake && !wakeLock) setWake(true);
    // Phones often kill sockets while locked; reconnect right away on return.
    if (password && !stopReconnecting && !ws) {
      backoffMs = 1000;
      connect();
    }
  });

  setInterval(() => { if (!$('control').hidden) render(); }, 250);

  // ---- Start ----
  if (password) {
    show('control');
    connect();
  } else {
    showLogin('');
  }
})();
