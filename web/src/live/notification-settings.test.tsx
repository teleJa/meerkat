import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { TurnNotificationsSection } from "./notification-settings";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

test("a disconnected desktop can send a local test", async () => {
  const fetch = vi.fn(async (url: string) => {
    if (url.endsWith("/permission")) return Response.json({ supported: true, status: "granted" });
    if (url.endsWith("/test")) return Response.json({ ok: true });
    return new Response(null, { status: 503 });
  });
  vi.stubGlobal("fetch", fetch);
  render(<TurnNotificationsSection />);
  fireEvent.click(await screen.findByRole("button", { name: "发送测试通知" }));
  await screen.findByText("测试通知已发送");
  expect(fetch).toHaveBeenCalledWith("/desktop/notifications/test", expect.objectContaining({ method: "POST" }));
  expect(screen.getByRole("switch", { name: "通知总开关" }).hasAttribute("disabled")).toBe(true);
});

test("a browser without a desktop bridge has no self-test", async () => {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    if (url.endsWith("/permission")) return new Response(null, { status: 404 });
    throw new Error("Hub offline");
  }));
  render(<TurnNotificationsSection />);
  await screen.findByText("Hub offline");
  expect(screen.queryByRole("button", { name: "发送测试通知" })).toBeNull();
});
