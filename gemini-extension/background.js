/**
 * Free Gemini API Sync — Background Service Worker
 * Pure memory-based zero-tab cookie synchronizer.
 * Connects to ws://127.0.0.1:9226 (or localhost).
 *
 * Fixes vs. the previous version:
 *   • Removed HTTP fallback to /api/sync-cookies (route does not exist).
 *   • Ignore SIDCC/NID/COMPASS rotation so Google cannot create a feedback loop.
 *   • Rate-limit auto-syncs to once every 30 s.
 *   • Removed undefined CANDIDATE_SERVERS reference.
 *   • Simplified server discovery to 127.0.0.1 / localhost (no subnet sweep).
 *   • Uses chrome.alarms for reconnect / keepalive so MV3 can sleep safely.
 *   • Calls init() at the top level so a woken service worker reconnects.
 */

let activeHost = '127.0.0.1';
let ws = null;
let lastSyncTime = null;
let hasSyncedOnce = false;
let syncDebounceTimeout = null;
let lastAutoSync = 0;
let forcePro = false;

const MIN_AUTO_SYNC_MS = 30000;

const ALLOWED_COOKIE_NAMES = new Set([
  '__Secure-1PSID', '__Secure-3PSID',
  '__Secure-1PAPISID', '__Secure-3PAPISID',
  '__Secure-1PSIDTS', '__Secure-3PSIDTS',
  '__Secure-1PSIDCC', '__Secure-3PSIDCC',
  'SID', 'HSID', 'SSID', 'APISID', 'SAPISID', 'SIDCC',
  'OSID', '__Secure-OSID', 'COMPASS', 'NID'
]);

// Rotate on almost every Google request — must NOT trigger a sync on their own.
const VOLATILE_COOKIE_NAMES = new Set([
  'SIDCC', '__Secure-1PSIDCC', '__Secure-3PSIDCC', 'NID', 'COMPASS'
]);

function inTargetDomain(domain) {
  return domain === '.google.com' ||
         domain === '.google.co' ||
         domain === 'gemini.google.com' ||
         domain.includes('googleusercontent.com');
}

chrome.runtime.onInstalled.addListener(init);
chrome.runtime.onStartup.addListener(init);

chrome.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name === 'reconnect') connectToBackend();
  if (alarm.name === 'keepAlive') keepAlive();
});

async function init() {
  const data = await chrome.storage.local.get(['lastSyncTime', 'discoveredServer', 'forcePro']);
  if (data.discoveredServer) activeHost = data.discoveredServer;
  if (data.lastSyncTime) lastSyncTime = data.lastSyncTime;
  forcePro = !!data.forcePro;

  connectToBackend();
  chrome.alarms.create('keepAlive', { periodInMinutes: 0.4 });
}

// ─── Server discovery (local only) ─────────────────────────────────────────
async function probeServer(host, timeoutMs = 300) {
  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const res = await fetch(`http://${host}:8001/health`, { signal: controller.signal });
    clearTimeout(timer);
    if (res.ok) {
      const data = await res.json();
      if (data && (data.status === 'online' || data.engine)) return host;
    }
  } catch (_) {}
  return null;
}

async function discoverServer() {
  const candidates = [...new Set([activeHost, '127.0.0.1', 'localhost'])];
  for (const host of candidates) {
    const found = await probeServer(host);
    if (found) {
      activeHost = found;
      await chrome.storage.local.set({ discoveredServer: found });
      return activeHost;
    }
  }
  return activeHost;
}

// ─── WebSocket lifecycle ───────────────────────────────────────────────────
async function connectToBackend() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
    return;
  }
  chrome.alarms.clear('reconnect');

  const host = await discoverServer();
  const wsUrl = `ws://${host}:9226`;
  console.log(`[Gemini Sync] Connecting to backend at: ${wsUrl}`);
  hasSyncedOnce = false;

  try {
    ws = new WebSocket(wsUrl);
  } catch (e) {
    scheduleReconnect();
    return;
  }

  ws.onopen = () => {
    console.log(`[Gemini Sync] Connected to Go Backend on ${host}`);
    chrome.alarms.clear('reconnect');
    performSync('ws-open');
  };

  ws.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data);
      if (msg.type === 'trigger_sync') {
        console.log('[Gemini Sync] Backend requested fresh cookies.');
        performSync('backend-request');
      }
    } catch (e) {
      console.error(e);
    }
  };

  ws.onclose = () => {
    console.log(`[Gemini Sync] Connection to ${host} closed. Re-discovering...`);
    ws = null;
    scheduleReconnect();
  };

  ws.onerror = () => {
    console.warn(`[Gemini Sync] WebSocket error for ${host}:9226`);
    try { ws.close(); } catch (_) {}
  };
}

function scheduleReconnect() {
  chrome.alarms.create('reconnect', { delayInMinutes: 0.05 });
}

function keepAlive() {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: 'ping' }));
  } else {
    connectToBackend();
  }
}

// ─── Cookie extraction ─────────────────────────────────────────────────────
function getFormattedCookies(callback) {
  chrome.cookies.getAll({}, (cookies) => {
    const googleCookies = cookies.filter(c =>
      inTargetDomain(c.domain) &&
      (ALLOWED_COOKIE_NAMES.has(c.name) || c.domain.includes('googleusercontent.com'))
    );

    const formatted = googleCookies.map(c => {
      let exp = c.expirationDate;
      if (!exp || c.session) exp = Math.floor(Date.now() / 1000) + 31536000;
      let sameSite = c.sameSite || 'unspecified';
      if (sameSite === 'no_restriction') sameSite = 'none';
      return {
        domain: c.domain,
        expirationDate: exp,
        hostOnly: c.hostOnly,
        httpOnly: c.httpOnly,
        name: c.name,
        path: c.path,
        sameSite: sameSite,
        secure: c.secure,
        session: c.session,
        storeId: c.storeId || '0',
        value: c.value
      };
    });
    callback(formatted);
  });
}

// ─── Sync ──────────────────────────────────────────────────────────────────
async function performSync(reason = 'manual') {
  if (!ws || ws.readyState !== WebSocket.OPEN) {
    connectToBackend();
    return;
  }
  getFormattedCookies((formatted) => {
    if (!formatted.length) return;
    try {
      ws.send(JSON.stringify({ type: 'cookies_payload', cookies: formatted }));
      console.log(`[Gemini Sync] Synchronized ${formatted.length} cookies via WebSocket (${reason}, force_pro=${forcePro})`);
    } catch (e) {
      console.warn('[Gemini Sync] Failed to send cookies:', e);
    }

    hasSyncedOnce = true;
    lastSyncTime = Date.now();
    chrome.storage.local.set({ lastSyncTime });
    chrome.runtime.sendMessage({
      type: 'SYNC_UPDATE', success: true, count: formatted.length
    }).catch(() => {});
  });
}

function scheduleAutoSync() {
  const wait = Math.max(1500, lastAutoSync + MIN_AUTO_SYNC_MS - Date.now());
  if (syncDebounceTimeout) clearTimeout(syncDebounceTimeout);
  syncDebounceTimeout = setTimeout(() => {
    lastAutoSync = Date.now();
    performSync('auto-cookie-change');
  }, wait);
}

chrome.cookies.onChanged.addListener((changeInfo) => {
  const cookie = changeInfo.cookie;
  if (changeInfo.removed) return;
  if (!inTargetDomain(cookie.domain)) return;
  if (!ALLOWED_COOKIE_NAMES.has(cookie.name)) return;
  if (VOLATILE_COOKIE_NAMES.has(cookie.name)) return; // rotation must not loop
  scheduleAutoSync();
});

// ─── Popup messaging ───────────────────────────────────────────────────────
chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg.type === 'GET_STATUS') {
    sendResponse({
      connected: !!(ws && ws.readyState === WebSocket.OPEN),
      lastSyncTime,
      hasSyncedOnce,
      serverHost: activeHost
    });
    return false;
  }

  if (msg.type === 'SET_SERVER_HOST') {
    const newHost = (msg.host || '').trim() || '127.0.0.1';
    activeHost = newHost;
    chrome.storage.local.set({ discoveredServer: newHost });
    if (ws) { try { ws.close(); } catch (_) {} ws = null; }
    connectToBackend();
    sendResponse({ ok: true, host: newHost });
    return false;
  }

  if (msg.type === 'FORCE_SYNC') {
    performSync('manual-popup');
    sendResponse({ ok: true });
    return false;
  }

  if (msg.type === 'GET_FORMATTED_COOKIES') {
    getFormattedCookies((cookies) => sendResponse({ ok: true, cookies }));
    return true;
  }

  
  if (msg.type === 'SET_FORCE_PRO') {
    forcePro = !!msg.enabled;
    chrome.storage.local.set({ forcePro });
    performSync('toggle-force-pro');
    sendResponse({ ok: true, forcePro });
    return false;
  }
  return false;
});

// Run on service-worker wake-up (onInstalled/onStartup only fire on those events).
init();