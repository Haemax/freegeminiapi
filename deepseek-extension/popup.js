document.addEventListener('DOMContentLoaded', () => {
  const connStatus = document.getElementById('conn-status');
  const tokenStatus = document.getElementById('token-status');
  const syncBtn = document.getElementById('sync-btn');

  function updateUI() {
    chrome.runtime.sendMessage({ type: 'GET_STATUS' }, (res) => {
      if (chrome.runtime.lastError || !res) return;
      connStatus.innerText = res.connected ? 'Connected' : 'Disconnected';
      connStatus.style.color = res.connected ? '#10b981' : '#ef4444';
      tokenStatus.innerText = res.hasToken ? 'Yes' : 'No';
      tokenStatus.style.color = res.hasToken ? '#10b981' : '#ef4444';
    });
  }

  updateUI();
  chrome.runtime.onMessage.addListener((msg) => { if (msg.type === 'SYNC_UPDATE') updateUI(); });

  syncBtn.addEventListener('click', () => {
    syncBtn.disabled = true;
    syncBtn.innerText = 'Syncing...';
    chrome.runtime.sendMessage({ type: 'FORCE_SYNC' }, () => {
      setTimeout(() => {
        syncBtn.disabled = false;
        syncBtn.innerText = 'Force Sync DeepSeek Session';
        updateUI();
      }, 500);
    });
  });
});
