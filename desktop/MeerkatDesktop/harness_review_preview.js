(gestureToken => {
  // This script runs before DSH and keeps the gesture credential out of its DOM.
  document.currentScript.remove();
  const unsupported = "这个 DSH 版本的差异面板本机接管尚未验证。仍可在 DSH 差异面板内预览。";
  const release = () => window.__DSH_BOOT__?.release?.version;
  const supported = () => release() === "0.2.2-rc.2";
  const fetchFromPage = window.fetch.bind(window);
  const RequestType = Request;
  const HeaderList = Headers;
  const URLType = URL;
  const setHeader = Function.call.bind(Headers.prototype.set);
  const forEachHeader = Function.call.bind(Headers.prototype.forEach);
  const makeHeaderRecord = Object.create.bind(Object, null);
  const eventPhase = Function.call.bind(Object.getOwnPropertyDescriptor(Event.prototype, "eventPhase").get);
  let openClick;
  let feedback;

  function showFeedback(message) {
    if (!feedback) {
      feedback = document.createElement("p");
      feedback.setAttribute("role", "status");
      feedback.style.cssText = "position:fixed;right:12px;bottom:12px;z-index:2147483647;max-width:min(360px,calc(100vw - 24px));margin:0;padding:10px 14px;border:1px solid CanvasText;border-radius:8px;background:Canvas;color:CanvasText;font:14px system-ui;pointer-events:none";
      document.body.append(feedback);
    }
    feedback.textContent = message;
    feedback.hidden = false;
  }

  document.addEventListener("click", event => {
    if (!event.isTrusted || !(event.target instanceof Element)) return;
    const button = event.target.closest("[data-open-path-open]");
    const panel = button?.closest("[data-changes-review]") ?? (
      event.target.closest('[role="menuitem"]') && document.querySelector('[data-changes-review] [data-open-path-more][aria-expanded="true"]')?.closest("[data-changes-review]")
    );
    const file = panel && panel.querySelector("[data-review-file]")?.getAttribute("data-review-file");
    if (!file || !/\.html?$/i.test(file)) return;
    if (!supported()) {
      event.preventDefault();
      event.stopImmediatePropagation();
      showFeedback(unsupported);
      return;
    }
    if (feedback) feedback.hidden = true;
    openClick = event;
  }, true);

  window.fetch = async (input, init) => {
    const request = input instanceof RequestType ? input : undefined;
    const method = init?.method || request?.method || "GET";
    if (method.toUpperCase() !== "POST") return fetchFromPage(input, init);
    const url = new URLType(request?.url ?? input, window.location.href);
    if (url.origin !== window.location.origin || url.pathname !== "/api/changes.open" || url.searchParams.get("action") === "reveal") {
      return fetchFromPage(input, init);
    }
    const headers = new HeaderList(init?.headers ?? request?.headers);
    setHeader(headers, "X-Meerkat-Review-Release", release() ?? "unknown");
    const options = { ...init, headers };
    if (gestureToken && openClick && eventPhase(openClick) !== 0 && typeof input === "string") {
      openClick = undefined;
      const boundHeaders = makeHeaderRecord();
      forEachHeader(headers, (value, name) => { boundHeaders[name] = value; });
      boundHeaders["x-meerkat-review-gesture"] = gestureToken;
      options.headers = boundHeaders;
      options.redirect = "error";
      input = url.href;
    }
    const response = await fetchFromPage(input, options);
    if (!response.ok && response.headers.get("X-Meerkat-Review-Preview") === "1") {
      showFeedback((await response.clone().text()).trim());
    }
    return response;
  };

  document.addEventListener("DOMContentLoaded", () => {
    if (!supported()) showFeedback(unsupported);
  }, { once: true });
})
