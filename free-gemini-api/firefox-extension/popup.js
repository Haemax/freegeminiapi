const api = (typeof browser !== 'undefined') ? browser : chrome;
const hasBrowser = (typeof browser !== 'undefined');
const DEFAULT_ENDPOINT = 'http://127.0.0.1:8001/api/cookies';

const $ = (id) => document.getElementById(id);

function storageGet(keys) {
  if (hasBrowser) return browser.storage.local.get(keys);
  return new Promise((res) => chrome.storage.local.get(keys, res));
}
function storageSet(obj) {
  if (hasBrowser) return browser.storage.local.set(obj);
  return new Promise((res) => chrome.storage.local.set(obj, res));
}
function sendMessage(msg) {
  if (hasBrowser) return browser.runtime.sendMessage(msg);
  return new Promise((res, rej) => {
    chrome.runtime.sendMessage(msg, (r) => {
      const e = chrome.runtime.lastError;
      if (e) rej(new Error(e.message)); else res(r);
    });
  });
}

async function refreshStatus() {
  const s = await storageGet(['lastSync', 'lastSyncStatus']);
  if (!s.lastSync) { $('status').textContent = 'No sync yet.'; return; }
  $('status').textContent =
    `Last sync: ${new Date(s.lastSync).toLocaleString()} â€” ${s.lastSyncStatus || 'unknown'}`;
}

async function load() {
  const cfg = await storageGet(['endpoint']);
  $('endpoint').value = cfg.endpoint || DEFAULT_ENDPOINT;
  refreshStatus();
}

$('save').addEventListener('click', async () => {
  await storageSet({ endpoint: $('endpoint').value.trim() });
  $('status').textContent = 'Endpoint saved.';
});

$('sync').addEventListener('click', async () => {
  $('status').textContent = 'Syncingâ€¦';
  try {
    const resp = await sendMessage({ type: 'sync-now' });
    if (resp && resp.ok) {
      await refreshStatus();
    } else {
      $('status').textContent = 'Sync failed: ' + ((resp && resp.error) || 'unknown');
    }
  } catch (e) {
    $('status').textContent = 'Sync failed: ' + e.message;
  }
});

load();