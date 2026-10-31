/* Firefox MV3 event page. Talks to the free-gemini-api WebSocket cookie bridge
 * at ws://127.0.0.1:9226 using the server's expected envelope:
 *   { "type": "cookies_payload", "cookies": [...] }
 * Falls back to callback-based `chrome` so the same file also runs in Chrome. */

const api = (typeof browser !== 'undefined') ? browser : chrome;
const hasBrowser = (typeof browser !== 'undefined');

const DEFAULT_WS_URL = 'ws://127.0.0.1:9226';
const RECONNECT_MS = 3000;
const KEEPALIVE_MS = 25000;

const ALLOWED_COOKIE_NAMES = new Set([
  'SID', 'HSID', 'SSID', 'APISID', 'SAPISID',
  '__Secure-1PSID', '__Secure-3PSID',
  '__Secure-1PAPISID', '__Secure-3PAPISID',
  '__Secure-1PSIDTS', '__Secure-3PSIDTS',
  '__Secure-OSID', 'OSID', 'LSID',
  'NID', 'AEC', 'CONSENT', 'SOCS', 'SEARCH_SAMESITE',
  '__Host-GAPS', 'GAPS',
  'SIDCC', '__Secure-1PSIDCC', '__Secure-3PSIDCC', 'COMPASS'
]);

// Rotate on nearly every Google request; must NOT trigger a sync on their own.
const VOLATILE_COOKIE_NAMES = new Set([
  'SIDCC', '__Secure-1PSIDCC', '__Secure-3PSIDCC', 'NID', 'COMPASS'
]);
const MIN_AUTO_SYNC_MS = 30000;

const TARGET_DOMAIN_SUFFIXES = [
  '.google.com', 'google.com', 'gemini.google.com',
  '.googleusercontent.com', 'googleusercontent.com'
];

let ws = null;
let reconnectTimer = null;
let keepaliveTimer = null;
let syncDebounce = null;
let lastAutoSync = 0;

/* ---- promise wrappers ---- */
function storageGet(keys) {
  if (hasBrowser) return browser.storage.local.get(keys);
  return new Promise((res) => chrome.storage.local.get(keys, res));
}
function storageSet(obj) {
  if (hasBrowser) return browser.storage.local.set(obj);
  return new Promise((res) => chrome.storage.local.set(obj, res));
}
function getAllCookies(details) {
  if (hasBrowser) return browser.cookies.getAll(details);
  return new Promise((res, rej) => {
    chrome.cookies.getAll(details, (cookies) => {
      const e = chrome.runtime.lastError;
      if (e) rej(new Error(e.message)); else res(cookies);
    });
  });
}

function setBadge(text, color) {
  const target = api.action || api.browserAction;
  if (!target) return;
  try {
    target.setBadgeText({ text });
    target.setBadgeBackgroundColor({ color });
  } catch (_) { /* ignore */ }
}

/* ---- cookie collection ---- */
function inTargetDomain(domain) {
  return TARGET_DOMAIN_SUFFIXES.some((s) => domain === s || domain.endsWith(s));
}

async function getFormattedCookies() {
  const all = await getAllCookies({});
  const seen = new Map();
  for (const c of all) {
    if (!c || !inTargetDomain(c.domain || '')) continue;
    if (!ALLOWED_COOKIE_NAMES.has(c.name)) continue;
    seen.set(`${c.name}|${c.domain}|${c.path}`, {
      name: c.name,
      value: c.value,
      domain: c.domain,
      path: c.path,
      secure: c.secure,
      httpOnly: c.httpOnly,
      hostOnly: c.hostOnly,
      sameSite: c.sameSite,
      session: c.session,
      expirationDate: c.expirationDate
    });
  }
  return [...seen.values()];
}

/* ---- WebSocket lifecycle ---- */
async function getWsUrl() {
  const { wsUrl } = await storageGet(['wsUrl']);
  return (wsUrl && wsUrl.trim()) || DEFAULT_WS_URL;
}

async function connectWS() {
  if (ws && (ws.readyState === WebSocket.CONNECTING || ws.readyState === WebSocket.OPEN)) {
    return;
  }
  let url;
  try {
    url = await getWsUrl();
  } catch (_) {
    url = DEFAULT_WS_URL;
  }

  try {
    ws = new WebSocket(url);
  } catch (e) {
    console.error('[cookie-sync] WebSocket construct failed:', e);
    scheduleReconnect();
    return;
  }

  ws.onopen = () => {
    console.log('[cookie-sync] WS connected to', url);
    setBadge('●', '#3b82f6');
    // Push immediately on (re)connect so the server gets cookies after restart.
    sendCookies('ws-open');
    startKeepalive();
  };

  ws.onmessage = (ev) => {
    // Server echoes {"type":"pong"} for our pings; nothing else expected.
    console.log('[cookie-sync] WS message:', ev.data);
  };

  ws.onerror = (e) => {
    console.warn('[cookie-sync] WS error:', e);
  };

  ws.onclose = () => {
    console.log('[cookie-sync] WS closed');
    setBadge('×', '#dc2626');
    ws = null;
    stopKeepalive();
    scheduleReconnect();
  };
}

function scheduleReconnect() {
  if (reconnectTimer) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    connectWS();
  }, RECONNECT_MS);
}

function startKeepalive() {
  stopKeepalive();
  keepaliveTimer = setInterval(() => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      try { ws.send(JSON.stringify({ type: 'ping' })); } catch (_) {}
    }
  }, KEEPALIVE_MS);
}
function stopKeepalive() {
  if (keepaliveTimer) { clearInterval(keepaliveTimer); keepaliveTimer = null; }
}

/* ---- push ---- */
async function sendCookies(reason) {
  if (!ws || ws.readyState !== WebSocket.OPEN) {
    connectWS();
    return { ok: false, error: 'WS not connected; reconnecting…' };
  }

  let cookies;
  try {
    cookies = await getFormattedCookies();
  } catch (e) {
    setBadge('✗', '#dc2626');
    await storageSet({ lastSync: Date.now(), lastSyncStatus: 'cookie read error: ' + e.message });
    return { ok: false, error: e.message };
  }

  if (!cookies.length) {
    setBadge('!', '#f59e0b');
    await storageSet({ lastSync: Date.now(), lastSyncStatus: 'no cookies found' });
    return { ok: false, error: 'no cookies found' };
  }

  try {
    // Server expects ExtensionMessage{Type, Cookies}, see gemini/monitor.go:StartCookieWebSocketServer
    ws.send(JSON.stringify({ type: 'cookies_payload', cookies }));
  } catch (e) {
    setBadge('✗', '#dc2626');
    await storageSet({ lastSync: Date.now(), lastSyncStatus: 'WS send failed: ' + e.message });
    return { ok: false, error: e.message };
  }

  setBadge('✓', '#16a34a');
  await storageSet({
    lastSync: Date.now(),
    lastSyncStatus: `sent ${cookies.length} cookies via WS (${reason})`
  });
  return { ok: true };
}

async function scheduleAutoSync(reason) {
  const now = Date.now();
  const wait = Math.max(1500, lastAutoSync + MIN_AUTO_SYNC_MS - now);
  if (syncDebounce) clearTimeout(syncDebounce);
  syncDebounce = setTimeout(async () => {
    lastAutoSync = Date.now();
    sendCookies(reason);
  }, wait);
}

/* ---- event wiring ---- */
api.cookies.onChanged.addListener((info) => {
  if (info.removed) return;
  const c = info.cookie;
  if (!c) return;
  if (!inTargetDomain(c.domain || '')) return;
  if (!ALLOWED_COOKIE_NAMES.has(c.name)) return;
  if (VOLATILE_COOKIE_NAMES.has(c.name)) return; // rotation must not cause a loop
  scheduleAutoSync('cookie-change');
});

api.alarms.create('periodic-sync', { periodInMinutes: 5 });
api.alarms.onAlarm.addListener((a) => {
  if (a.name === 'periodic-sync') sendCookies('periodic');
});

api.runtime.onInstalled.addListener(() => {
  setTimeout(() => { connectWS(); }, 1000);
});
api.runtime.onStartup.addListener(() => {
  setTimeout(() => { connectWS(); }, 1000);
});

api.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg && msg.type === 'sync-now') {
    (async () => {
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        await connectWS();
        await new Promise((r) => setTimeout(r, 800));
      }
      sendResponse(await sendCookies('manual'));
    })();
    return true;
  }
  if (msg && msg.type === 'get-status') {
    storageGet(['lastSync', 'lastSyncStatus', 'wsUrl']).then(sendResponse);
    return true;
  }
  if (msg && msg.type === 'reconnect') {
    if (ws) { try { ws.close(); } catch (_) {} }
    ws = null;
    connectWS().then(() => sendResponse({ ok: true }));
    return true;
  }
  return false;
});

connectWS();