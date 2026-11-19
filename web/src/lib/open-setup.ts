/**
 * 打开「加入工作区」窗口。
 *
 * 之前在桌面 App 里点这个按钮会把 `/setup` 交给系统浏览器打开：用户在浏览器里
 * 粘贴邀请码、写完配置，页面提示「App 正在重启」，但真正重启的是桌面进程——
 * 浏览器那一页就停在那儿，用户不知道该看哪边。
 *
 * 现在优先让桌面 App 自己开一个窗口（`/desktop/open-setup`）。只有拿不到这个
 * 端点时才退回新标签页打开，保持浏览器预览下的旧行为。
 */
export async function openJoinWorkspace(): Promise<void> {
  if (typeof window === "undefined") return;
  try {
    const response = await fetch("/desktop/open-setup", { method: "POST" });
    if (response.ok) return;
  } catch {
    // 非桌面 App 环境落到下面的回退。
  }
  window.open(window.location.origin + "/setup", "_blank", "noopener,noreferrer");
}
