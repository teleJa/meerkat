/**
 * 在 Wails WebView 中打开系统浏览器。
 *
 * WebView 里直接 window.open 往往会被拦截；Wails 暴露了 Browser.OpenURL
 * 运行时调用（object=9, method=0），这里直接走它的 HTTP runtime 端点，
 * 普通浏览器预览时再回退到 window.open。
 */
export async function openExternal(rawUrl: string): Promise<void> {
  const url = rawUrl?.trim();
  if (!url) return;
  if (typeof window === "undefined") return;

  try {
    const response = await fetch("/desktop/open-url", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ url }),
    });
    if (response.ok) return;
  } catch {
    // 非桌面 App 环境继续走 Wails runtime / window.open。
  }

  const clientId = globalThis.crypto?.randomUUID?.() ?? String(Date.now()) + Math.random().toString(16).slice(2);
  try {
    const response = await fetch("/wails/runtime", {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-wails-client-id": clientId,
      },
      body: JSON.stringify({ object: 9, method: 0, args: { url } }),
    });
    if (response.ok) return;
  } catch {
    // 非 Wails 环境（浏览器预览）落到下面的回退。
  }
  window.open(url, "_blank", "noopener,noreferrer");
}