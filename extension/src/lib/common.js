// Shared by background, content and popup scripts. The build prepends this
// file to each entry and wraps the result in an IIFE, so these names are local
// to each bundle. Use only APIs that exist in both MV2 and MV3.

// eslint-disable-next-line no-unused-vars
const api = globalThis.browser ?? globalThis.chrome;
// MV3 calls it `action`, MV2 `browserAction`.
// eslint-disable-next-line no-unused-vars
const actionApi = api.action ?? api.browserAction;

// eslint-disable-next-line no-unused-vars
const PORT_NAME = 'ddr-player';

// eslint-disable-next-line no-unused-vars
const SETTINGS_DEFAULTS = Object.freeze({
  enabled: false,
  relayUrl: '',
  password: '',
  chime: false,
});

// eslint-disable-next-line no-unused-vars
async function loadSettings() {
  const stored = await api.storage.local.get(Object.keys(SETTINGS_DEFAULTS));
  return { ...SETTINGS_DEFAULTS, ...stored };
}

/**
 * Turns what a person types ("remote.example.com", "https://remote.example.com")
 * into a WebSocket URL ("wss://remote.example.com/ws"). Plain ws:// is only
 * allowed for localhost development. Returns null if the input isn't usable.
 */
// eslint-disable-next-line no-unused-vars
function normalizeRelayUrl(input) {
  let s = String(input || '').trim();
  if (!s) return null;
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(s)) s = `wss://${s}`;
  let u;
  try { u = new URL(s); } catch { return null; }
  if (u.protocol === 'https:') u.protocol = 'wss:';
  else if (u.protocol === 'http:') u.protocol = 'ws:';
  if (u.protocol !== 'wss:' && u.protocol !== 'ws:') return null;
  const local = u.hostname === 'localhost' || u.hostname === '127.0.0.1';
  if (u.protocol === 'ws:' && !local) return null;
  if (u.pathname === '' || u.pathname === '/') u.pathname = '/ws';
  u.hash = '';
  u.search = '';
  return u.toString();
}
