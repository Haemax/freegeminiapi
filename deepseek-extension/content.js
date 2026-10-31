function extractDeepSeekCredentials() {
  try {
    const userToken = localStorage.getItem('userToken');
    if (userToken) {
      chrome.runtime.sendMessage({
        type: 'DEEPSEEK_TOKEN_EXTRACTED',
        userToken: userToken,
        origin: window.location.origin
      });
    }
  } catch (e) {
    console.error('[DeepSeek Sync] Error extracting token:', e);
  }
}

extractDeepSeekCredentials();
setInterval(extractDeepSeekCredentials, 15000);
