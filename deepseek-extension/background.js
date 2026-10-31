let activeHost = '127.0.0.1';
let ws = null;
let lastSyncTime = null;
let cachedToken = null;

chrome.runtime.onInstalled.addListener(init);
chrome.runtime.onStartup.addListener(init);

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg.type === 'DEEPSEEK_TOKEN_EXTRACTED') {
    cachedToken = msg.userToken;
    performSync('content-script-token');
  }
  if (msg.type === 'GET_STATUS') {
    sendResponse({
      connected: ws && ws.readyState === WebSocket.OPEN,
      lastSyncTime: lastSyncTime,
      hasToken: !!cachedToken
    });
  }
  if (msg.type === 'FORCE_SYNC') {
    performSync('manual');
    sendResponse({ status: 'ok' });
  }
  return true;
});

async function init() {
  connectToBackend();
  chrome.alarms.create('keepAlive', { periodInMinutes: 0.5 });
}

function connectToBackend() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;

  const wsUrl = `ws://${activeHost}:9226`;
  ws = new WebSocket(wsUrl);

  ws.onopen = () => {
    console.log('[DeepSeek Sync] Connected to Go Backend');
    performSync('ws-open');
  };

  ws.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      if (data.type === 'trigger_sync') performSync('backend-request');
    } catch (_) {}
  };

  ws.onclose = () => {
    ws = null;
    chrome.alarms.create('reconnect', { delayInMinutes: 0.1 });
  };
}

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === 'reconnect' || alarm.name === 'keepAlive') connectToBackend();
});

function performSync(reason) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;

  chrome.cookies.getAll({ domain: 'deepseek.com' }, (cookies) => {
    const payload = {
      type: 'deepseek_session_payload',
      user_token: cachedToken,
      cookies: cookies,
      synced_at: Date.now(),
      sync_reason: reason
    };
    ws.send(JSON.stringify(payload));
    lastSyncTime = Date.now();
    chrome.runtime.sendMessage({ type: 'SYNC_UPDATE' });
  });
}
