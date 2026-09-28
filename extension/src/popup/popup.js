// Popup: on/off toggle, connection settings and live status.

const $ = (id) => document.getElementById(id);

const STATUS = {
  off: { text: 'Off', dot: '' },
  needs_setup: { text: 'Enter the relay address and password below', dot: 'amber' },
  connecting: { text: 'Connecting…', dot: 'amber' },
  reconnecting: { text: 'Reconnecting…', dot: 'amber' },
  connected: { text: 'Connected', dot: 'green' },
  auth_error: { text: 'Wrong password', dot: 'red' },
  locked_out: { text: 'Too many wrong passwords. Locked for 15 minutes.', dot: 'red' },
};

function formMessage(text, kind) {
  const el = $('form-msg');
  el.textContent = text;
  el.className = `form-msg ${kind || ''}`;
  el.hidden = !text;
}

async function refreshStatus() {
  let s;
  try {
    s = await api.runtime.sendMessage({ type: 'get_status' });
  } catch {
    return;
  }
  if (!s) return;
  const info = STATUS[s.status] || STATUS.off;
  $('status-text').textContent = info.text;
  $('status-dot').className = `dot ${info.dot}`;
  $('toggle-sub').textContent = s.enabled ? 'On' : 'Off';
  const live = s.status === 'connected';
  $('fact-video').textContent = s.enabled ? (s.videoFound ? 'Yes' : 'No') : '—';
  $('fact-phones').textContent = live ? String(s.controllers) : '—';
  $('retry').hidden = !(s.enabled && (s.status === 'reconnecting' || s.status === 'locked_out'));
  if (s.status === 'needs_setup' || s.status === 'auth_error') $('settings').open = true;
}

async function init() {
  const s = await loadSettings();
  $('enabled').checked = s.enabled;
  $('relay-url').value = s.relayUrl;
  $('password').value = s.password;
  $('chime').checked = s.chime;
  if (!s.relayUrl || !s.password) $('settings').open = true;

  $('enabled').addEventListener('change', async () => {
    await api.storage.local.set({ enabled: $('enabled').checked });
    refreshStatus();
  });

  $('chime').addEventListener('change', () => api.storage.local.set({ chime: $('chime').checked }));

  $('show-pw').addEventListener('click', () => {
    const pw = $('password');
    const show = pw.type === 'password';
    pw.type = show ? 'text' : 'password';
    $('show-pw').textContent = show ? 'Hide' : 'Show';
  });

  $('settings-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const url = normalizeRelayUrl($('relay-url').value);
    const password = $('password').value;
    if (!url) {
      formMessage('Enter an address like remote.example.com', 'bad');
      return;
    }
    if (!password) {
      formMessage('Enter the password.', 'bad');
      return;
    }
    $('relay-url').value = url;
    await api.storage.local.set({ relayUrl: url, password });
    formMessage($('enabled').checked ? 'Saved. Connecting…' : 'Saved. Turn on Remote control to connect.', 'good');
    refreshStatus();
  });

  $('retry').addEventListener('click', async () => {
    await api.runtime.sendMessage({ type: 'reconnect' });
    refreshStatus();
  });

  refreshStatus();
  setInterval(refreshStatus, 1000);
}

init();
