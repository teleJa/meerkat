import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { UpdateSettings } from "./update-settings";

const initialState = { version: "0.2.2", mode: "windows", automatic: true, phase: "idle", installable: false };
const capability = "test-capability";

function renderUpdate(reply: (action: Record<string, unknown>) => Response, withCapability = true) {
  if (withCapability) vi.stubGlobal("__meerkatUpdateToken", capability);
  const fetch = vi.fn(async (_path: string, options: RequestInit) => reply(JSON.parse(String(options.body))));
  vi.stubGlobal("fetch", fetch);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchInterval: false } } });
  render(<QueryClientProvider client={client}><UpdateSettings /></QueryClientProvider>);
  return fetch;
}

function expectAction(fetch: ReturnType<typeof renderUpdate>, action: Record<string, unknown>) {
  expect(fetch).toHaveBeenCalledWith("/desktop/update-settings", expect.objectContaining({
    method: "POST", cache: "no-store", body: JSON.stringify(action),
    headers: { "Content-Type": "application/json", "X-Meerkat-Update-Token": capability },
  }));
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("browser without a desktop capability cannot call the updater", () => {
  const fetch = renderUpdate(() => Response.json(initialState), false);
  expect(screen.getByText("版本与更新仅在 Meerkat 桌面应用中可用。")).toBeTruthy();
  expect(fetch).not.toHaveBeenCalled();
});

test("status, check and automatic settings use the desktop actions", async () => {
  const fetch = renderUpdate(request => Response.json({
    ...initialState,
    automatic: request.action !== "automatic",
    check_result: request.action === "check" ? "已是最新版本" : undefined,
  }));
  expect(screen.getByText("正在读取本机版本…")).toBeTruthy();
  await screen.findByText("0.2.2");
  expectAction(fetch, { action: "status" });
  fireEvent.click(screen.getByRole("button", { name: "检查更新" }));
  await screen.findByText("已是最新版本");
  expectAction(fetch, { action: "check" });
  const control = screen.getByRole<HTMLInputElement>("checkbox", { name: "自动更新" });
  expect(control.checked).toBe(true);
  fireEvent.click(control);
  await screen.findByText("自动下载已关闭，仍可手动检查和安装。");
  expect(control.checked).toBe(false);
  expectAction(fetch, { action: "automatic", enabled: false });
});

test.each([true, false])("a 503 gives a retry button and technical detail (automatic=%s)", async automatic => {
  renderUpdate(() => Response.json({ ...initialState, automatic, check_result: "检查失败：更新服务返回 HTTP 503" }));
  await screen.findByText(automatic ? "更新服务暂时不可用。Meerkat 会稍后自动重试，你也可以现在重试。" : "更新服务暂时不可用，请稍后重试。");
  expect(screen.getByRole("button", { name: "重试检查" })).toBeTruthy();
  expect(screen.getByText("检查失败：更新服务返回 HTTP 503").closest("details")?.open).toBe(false);
});

test.each([{ total: 100, bytes: 45, expected: "45%" }, { total: 0, bytes: 2097152, expected: "2.0 MB" }])(
  "download progress disables checking ($expected)", async ({ total, bytes, expected }) => {
    renderUpdate(() => Response.json({ ...initialState, phase: "downloading", total, bytes }));
    await screen.findByText(expected);
    const progress = screen.getByRole<HTMLProgressElement>("progressbar");
    expect(progress.hasAttribute("value")).toBe(total > 0);
    if (total) expect(progress.value).toBe(bytes);
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "检查更新" }).disabled).toBe(true);
  },
);

test("a blocked update requests the exact process and installation version", async () => {
  const blocked = { ...initialState, phase: "blocked", failed_version: "0.2.3", blocker: { pid: 42, name: "Meerkat.exe" } };
  const fetch = renderUpdate(request => Response.json(request.action === "end-process"
    ? { ...blocked, blocker: undefined } : blocked));
  const end = await screen.findByRole("button", { name: "结束该进程并继续" });
  fireEvent.click(end);
  await waitFor(() => expect(screen.queryByRole("button", { name: "结束该进程并继续" })).toBeNull());
  expectAction(fetch, { action: "end-process", version: "0.2.3", pid: 42 });
  fireEvent.click(screen.getByRole("button", { name: "重试更新" }));
  await waitFor(() => expectAction(fetch, { action: "install", version: "0.2.3" }));
});

test.each([
  { mode: "windows", label: "立即更新" },
  { mode: "manual", label: "打开安装包" },
])("available $mode update sends the selected version", async ({ mode, label }) => {
  const fetch = renderUpdate(() => Response.json({ ...initialState, mode, available: "0.2.3", installable: true }));
  fireEvent.click(await screen.findByRole("button", { name: label }));
  await waitFor(() => expectAction(fetch, { action: "install", version: "0.2.3" }));
});

test("a failed update action surfaces its message and reenables checking", async () => {
  const fetch = renderUpdate(request => request.action === "check"
    ? Response.json({ message: "本机更新器暂时不可用" }, { status: 503 })
    : Response.json(initialState));
  const check = await screen.findByRole<HTMLButtonElement>("button", { name: "检查更新" });
  fireEvent.click(check);
  await screen.findByText("本机更新器暂时不可用");
  expect(check.disabled).toBe(false);
  expectAction(fetch, { action: "check" });
});
