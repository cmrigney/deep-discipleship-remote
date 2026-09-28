// Background: owns the relay WebSocket and routes messages between the relay
// and content scripts. On Safari/Firefox this is a persistent MV2 background
// page; on Chrome it's an MV3 service worker kept alive by the 20 s ping.
// Everything is re-read from storage on start, so both models work.

const PING_MS = 20000;
const DEAD_AFTER_MS = 60000;
const LOCKOUT_RETRY_MS = 15 * 60 * 1000;
const MAX_BACKOFF_MS = 30000;

let settings = { ...SETTINGS_DEFAULTS };
let ws = null;
let generation = 0; // ignores events from sockets we've abandoned
let status = 'off';
let reconnectTimer = null;
let pingTimer = null;
let backoffMs = 1000;
let lastRelayMessageAt = 0;
let controllers = 0;

// Content-script ports: port -> { videoFound }
const ports = new Map();
let activePort = null; // the port whose video we control

// ---------- Status & badge ----------

const BADGE = {
  off: { text: '', color: '#6b7280' },
  needs_setup: { text: '!', color: '#f59e0b' },
  connecting: { text: '…', color: '#6b7280' },
  reconnecting: { text: '…', color: '#f59e0b' },
  connected: { text: 'ON', color: '#16a34a' },
  auth_error: { text: '!', color: '#dc2626' },
  locked_out: { text: '!', color: '#dc2626' },
};

function setStatus(next) {
  status = next;
  const b = BADGE[next] || BADGE.off;
  try {
    actionApi.setBadgeText({ text: b.text });
    actionApi.setBadgeBackgroundColor({ color: b.color });
  } catch { /* badge is cosmetic */ }
}

// ---------- Relay connection ----------

function sendToRelay(obj) {
  if (ws && ws.readyState === WebSocket.OPEN && status === 'connected') {
    ws.send(JSON.stringify(obj));
    return true;
  }
  return false;
}

function closeSocket() {
  generation++;
  clearTimeout(reconnectTimer);
  clearInterval(pingTimer);
  if (ws) {
    try { ws.close(1000, 'client closing'); } catch { /* already closed */ }
    ws = null;
  }
  controllers = 0;
}

function connect() {
  closeSocket();
  if (!settings.enabled) { setStatus('off'); return; }
  const url = normalizeRelayUrl(settings.relayUrl);
  if (!url || !settings.password) { setStatus('needs_setup'); return; }

  const gen = generation;
  setStatus(status === 'connected' || status === 'reconnecting' ? 'reconnecting' : 'connecting');

  let sock;
  try {
    sock = new WebSocket(url);
  } catch {
    scheduleReconnect();
    return;
  }
  ws = sock;

  sock.onopen = () => {
    if (gen !== generation) return;
    sock.send(JSON.stringify({
      type: 'auth', role: 'player', password: settings.password, clientName: 'Church computer',
    }));
  };

  sock.onmessage = (ev) => {
    if (gen !== generation) return;
    lastRelayMessageAt = Date.now();
    let msg;
    try { msg = JSON.parse(ev.data); } catch { return; }
    onRelayMessage(msg);
  };

  sock.onclose = () => {
    if (gen !== generation) return;
    ws = null;
    clearInterval(pingTimer);
    controllers = 0;
    if (status === 'auth_error') return; // wait for the user to fix the password
    if (status === 'locked_out') {
      reconnectTimer = setTimeout(connect, LOCKOUT_RETRY_MS);
      return;
    }
    scheduleReconnect();
  };
}

function scheduleReconnect() {
  if (!settings.enabled) { setStatus('off'); return; }
  setStatus('reconnecting');
  clearTimeout(reconnectTimer);
  const delay = backoffMs * (0.8 + Math.random() * 0.4);
  backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF_MS);
  reconnectTimer = setTimeout(connect, delay);
}

function onRelayMessage(msg) {
  switch (msg.type) {
    case 'auth_ok':
      setStatus('connected');
      backoffMs = 1000;
      clearInterval(pingTimer);
      pingTimer = setInterval(() => {
        // The ping keeps Chrome's service worker alive and exposes dead sockets.
        if (Date.now() - lastRelayMessageAt > DEAD_AFTER_MS) {
          if (ws) ws.close();
          return;
        }
        sendToRelay({ type: 'ping' });
      }, PING_MS);
      reportState();
      return;

    case 'auth_error':
      setStatus(msg.reason === 'bad_password' ? 'auth_error'
        : msg.reason === 'locked_out' ? 'locked_out' : 'reconnecting');
      return;

    case 'presence':
      controllers = msg.controllers | 0;
      return;

    case 'pong':
      return;

    case 'error':
      console.warn('[DD Remote] relay error:', msg.reason);
      return;

    default:
      handleCommand(msg);
  }
}

// ---------- Commands & state ----------

function noVideoState() {
  return { type: 'state', videoFound: false, paused: true, currentTime: 0, duration: 0, timer: null };
}

// Asks the active tab for fresh state, or reports that there's no video.
function reportState() {
  if (activePort) {
    postToPort(activePort, { type: 'request_state' });
  } else {
    sendToRelay(noVideoState());
  }
}

function handleCommand(msg) {
  if (msg.type === 'request_state') {
    reportState();
    return;
  }
  if (!activePort) {
    if (msg.id) sendToRelay({ type: 'ack', id: msg.id, ok: false, error: 'no_video' });
    return;
  }
  postToPort(activePort, msg);
}

function postToPort(port, msg) {
  try {
    port.postMessage(msg);
  } catch {
    dropPort(port);
  }
}

function pickActivePort() {
  // Prefer the most recently connected tab that has a video.
  let pick = null;
  for (const [port, info] of ports) {
    if (info.videoFound) pick = port;
  }
  return pick;
}

function dropPort(port) {
  if (!ports.delete(port)) return;
  if (activePort === port) {
    activePort = pickActivePort();
    reportState();
  }
}

api.runtime.onConnect.addListener((port) => {
  if (port.name !== PORT_NAME) return;
  ports.set(port, { videoFound: false });

  port.onMessage.addListener((msg) => {
    const info = ports.get(port);
    if (!info || !msg || typeof msg !== 'object') return;

    if (msg.type === 'state') {
      info.videoFound = !!msg.videoFound;
      if (info.videoFound) {
        activePort = activePort && activePort !== port && ports.get(activePort)?.videoFound ? activePort : port;
      } else if (activePort === port) {
        activePort = pickActivePort();
      }
      if (port === activePort) {
        sendToRelay(msg);
      } else if (!activePort) {
        sendToRelay(noVideoState());
      }
    } else if (msg.type === 'ack' && port === activePort) {
      sendToRelay(msg);
    }
  });

  port.onDisconnect.addListener(() => dropPort(port));
});

// ---------- Popup ----------

api.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (!msg || typeof msg !== 'object') return false;
  if (msg.type === 'get_status') {
    sendResponse({
      status,
      enabled: settings.enabled,
      controllers,
      videoFound: !!activePort,
    });
  } else if (msg.type === 'reconnect') {
    backoffMs = 1000;
    loadSettings().then((s) => { settings = s; connect(); });
    sendResponse({ ok: true });
  }
  return false;
});

// ---------- Settings ----------

api.storage.onChanged.addListener((changes, area) => {
  if (area !== 'local') return;
  if (!('enabled' in changes || 'relayUrl' in changes || 'password' in changes)) return;
  loadSettings().then((s) => {
    settings = s;
    backoffMs = 1000;
    connect();
  });
});

loadSettings().then((s) => {
  settings = s;
  connect();
});
