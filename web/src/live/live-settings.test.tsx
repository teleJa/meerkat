import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { LiveSettingsPage } from "./live-settings";

const scrollIntoView = vi.fn();
vi.mock("@/live/notification-settings", () => ({ TurnNotificationsSection: () => null }));

beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrollIntoView });
  scrollIntoView.mockClear();
});

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
  vi.unstubAllGlobals();
});

function renderSettings(preference: (request?: RequestInit) => Response, path = "/live/settings") {
  const fetch = vi.fn(async (url: string, request?: RequestInit) => {
    if (url === "/desktop/health") return Response.json({ mode: "node" });
    if (url === "/desktop/review-html-open") return preference(request);
    return Response.json({}, { status: 404 });
  });
  vi.stubGlobal("fetch", fetch);
  const router = createMemoryRouter([{ path: "/live/settings", element: <LiveSettingsPage /> }], { initialEntries: [path] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>);
  return { ...view, router };
}

const checkbox = () => screen.getByRole<HTMLInputElement>("checkbox", { name: "打开 HTML 前先询问" });

test("a new open parameter refocuses About in the current route", async () => {
  const { router } = renderSettings(() => Response.json({ ask_before_open: true }), "/live/settings?section=about&open=1");
  const title = screen.getByRole("heading", { name: "关于 Meerkat" });
  await waitFor(() => expect(document.activeElement).toBe(title));
  title.blur();
  scrollIntoView.mockClear();
  await act(() => router.navigate("/live/settings?section=about&open=2"));
  await waitFor(() => expect(document.activeElement).toBe(title));
  expect(scrollIntoView).toHaveBeenCalledOnce();
});

test("the HTML confirmation switch sends the new value and reloads saved state", async () => {
  let ask = true;
  const preference = vi.fn((request?: RequestInit) => {
    if (request?.method === "POST") ask = JSON.parse(String(request.body)).ask_before_open;
    return Response.json({ ask_before_open: ask });
  });
  const view = renderSettings(preference);
  await screen.findByRole("checkbox", { name: "打开 HTML 前先询问" });
  await waitFor(() => expect(checkbox().disabled).toBe(false));
  expect(checkbox().checked).toBe(true);
  fireEvent.click(checkbox());
  await screen.findByText("已保存");
  expect(preference).toHaveBeenCalledWith(expect.objectContaining({ method: "POST", body: JSON.stringify({ ask_before_open: false }) }));
  expect(checkbox().checked).toBe(false);
  view.unmount();
  renderSettings(preference);
  await screen.findByRole("checkbox", { name: "打开 HTML 前先询问" });
  await waitFor(() => expect(checkbox().checked).toBe(false));
  expect(preference).toHaveBeenLastCalledWith(undefined);
});

test("a failed HTML confirmation save keeps the last persisted value", async () => {
  renderSettings(request => request?.method === "POST"
    ? Response.json({ message: "disk error" }, { status: 500 })
    : Response.json({ ask_before_open: true }));
  await screen.findByRole("checkbox", { name: "打开 HTML 前先询问" });
  await waitFor(() => expect(checkbox().disabled).toBe(false));
  fireEvent.click(checkbox());
  await screen.findByText("保存失败，请重试。");
  expect(checkbox().checked).toBe(true);
  expect(checkbox().disabled).toBe(false);
});
