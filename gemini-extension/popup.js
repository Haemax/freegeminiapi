document.addEventListener('DOMContentLoaded', () => {
  const statusBadge = document.getElementById('status-badge');
  const statusText  = document.getElementById('status-text');
  const lastSync    = document.getElementById('last-sync');
  const syncBtn     = document.getElementById('sync-btn');
  const copyBtn     = document.getElementById('copy-btn');

  function updateUI() {
    chrome.runtime.sendMessage({ type: 'GET_STATUS' }, (response) => {
      if (chrome.runtime.lastError || !response) return;

      if (response.connected) {
        statusBadge.className = 'badge connected';
        statusText.innerText = 'Connected';
        statusText.style.color = '#10b981';
      } else {
        statusBadge.className = 'badge disconnected';
        statusText.innerText = 'Disconnected';
        statusText.style.color = '#ef4444';
      }

      lastSync.innerText = response.lastSyncTime
        ? new Date(response.lastSyncTime).toLocaleTimeString()
        : 'Never';
    });
  }

  updateUI();

  chrome.runtime.onMessage.addListener((msg) => {
    if (msg.type === 'SYNC_UPDATE') updateUI();
  });

  syncBtn.addEventListener('click', () => {
    syncBtn.disabled = true;
    syncBtn.innerText = 'Syncing...';
    chrome.runtime.sendMessage({ type: 'FORCE_SYNC' }, () => {
      // give the WS a tick to send before flipping the button back
      setTimeout(() => {
        syncBtn.disabled = false;
        syncBtn.innerText = 'Force Sync Cookies';
        updateUI();
      }, 500);
    });
  });

  copyBtn.addEventListener('click', () => {
    copyBtn.disabled = true;
    copyBtn.innerText = 'Extracting...';

    chrome.runtime.sendMessage({ type: 'GET_FORMATTED_COOKIES' }, async (response) => {
      if (chrome.runtime.lastError || !response || !response.cookies || response.cookies.length === 0) {
        copyBtn.innerText = '⚠️ No Cookies Found';
        setTimeout(() => {
          copyBtn.disabled = false;
          copyBtn.innerText = '📋 Copy Cookies';
        }, 2000);
        return;
      }

      try {
        await navigator.clipboard.writeText(JSON.stringify(response.cookies, null, 2));
        copyBtn.className = 'btn-copy success';
        copyBtn.innerText = `✅ Copied (${response.cookies.length} Cookies)!`;
      } catch (_) {
        copyBtn.innerText = '❌ Copy Failed';
      }

      setTimeout(() => {
        copyBtn.className = 'btn-copy';
        copyBtn.disabled = false;
        copyBtn.innerText = '📋 Copy Cookies';
      }, 2200);
    });
  });
});
const forceProToggle = document.getElementById('force-pro-toggle');
if (forceProToggle) {
  forceProToggle.addEventListener('change', () => {
    chrome.runtime.sendMessage(
      { type: 'SET_FORCE_PRO', enabled: forceProToggle.checked },
      () => updateUI()
    );
  });
}
