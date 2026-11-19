import { useEffect, useState } from "react";

/**
 * 在后台用隐藏 iframe 预热第一台可用 DSH。
 * DSH 的插件 bundle 和浏览器缓存第一次加载最慢；提前加载后 Harness 页面可以命中缓存。
 */
export function HarnessPrewarm() {
  const [url, setUrl] = useState("");

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const response = await fetch("/api/devices");
        if (!response.ok) return;
        const data = (await response.json()) as { devices?: Array<{ id: string; status: string; harness_available?: boolean }> };
        const device = data.devices?.find((item) => item.status === "online" && item.harness_available !== false);
        if (!device) return;
        const proxyResponse = await fetch("/api/devices/" + encodeURIComponent(device.id) + "/harness-proxy");
        if (!proxyResponse.ok) return;
        const proxy = (await proxyResponse.json()) as { url?: string; path?: string };
        const target = proxy.path ?? proxy.url?.replace("127.0.0.1", window.location.hostname);
        if (!target || cancelled) return;
        setUrl(target);
      } catch {
        // 预热失败不影响正常页面；下一轮定时重试。
      }
    };
    void load();
    const interval = window.setInterval(load, 30000);
    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, []);

  if (!url) return null;
  return (
    <iframe
      src={url}
      title="Harness prewarm"
      aria-hidden="true"
      tabIndex={-1}
      style={{ position: "fixed", width: 1, height: 1, opacity: 0, pointerEvents: "none", left: -10000, top: -10000 }}
    />
  );
}