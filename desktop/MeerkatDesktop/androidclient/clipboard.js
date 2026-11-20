(() => {
  const script = document.currentScript;
  const token = script.dataset.meerkatClipboard;
  script.remove();
  if (!token || window.parent !== window.top || location.ancestorOrigins?.length !== 1 ||
      location.ancestorOrigins[0] !== 'https://wails.localhost') return;

  const clipboard = navigator.clipboard;
  const browserWrite = clipboard?.writeText?.bind(clipboard);
  const send = window.fetch.bind(window);
  let clickAt = -Infinity;
  let notice;
  let noticeTimer;
  window.addEventListener('click', (event) => {
    if (event.isTrusted) clickAt = performance.now();
  }, true);

  function copyFailed(message) {
    if (!notice) {
      notice = document.createElement('div');
      notice.setAttribute('role', 'status');
      notice.style.cssText = 'position:fixed;z-index:2147483647;top:54px;left:50%;translate:-50%;max-width:85%;padding:8px 14px;border-radius:8px;background:#222;color:#fff;pointer-events:none';
      document.body.append(notice);
    }
    notice.textContent = message;
    notice.hidden = false;
    clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => { notice.hidden = true; }, 3500);
    return new DOMException(message, 'NotAllowedError');
  }

  function request(payload) {
    return send('/__meerkat/clipboard', {
      method: 'POST', mode: 'same-origin', credentials: 'omit', redirect: 'error', cache: 'no-store',
      headers: { 'Content-Type': 'application/json', 'X-Meerkat-Clipboard': token },
      body: JSON.stringify(payload),
    });
  }

  async function writeText(text) {
    if (typeof text !== 'string') throw new TypeError('Clipboard text must be a string');
    if (!navigator.userActivation?.isActive || performance.now() - clickAt > 5000 || document.hidden) {
      throw copyFailed('复制失败，请再点一次复制');
    }
    // Consume this click for the shim, including calls that await the browser.
    clickAt = -Infinity;
    if (browserWrite) {
      try {
        await browserWrite(text);
        void request({ native: true }).catch(() => {});
        return;
      } catch {
        // The Android WebView may refuse writes even after a trusted click.
      }
    }
    let response;
    try {
      response = await request({ text });
    } catch {
      throw copyFailed('复制失败，请重试');
    }
    if (response.status === 403) throw copyFailed('复制授权已过期，请重载 DSH 后再试');
    if (response.status === 413) throw copyFailed('复制失败，内容超过 1 MB');
    if (response.status !== 204) throw copyFailed('复制失败，请重试');
  }

  if (clipboard) {
    Object.defineProperty(clipboard, 'writeText', { value: writeText, configurable: true, writable: true });
  } else {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
  }
})();
