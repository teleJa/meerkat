import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, expect, test, vi } from "vitest";
import { NewTaskPoolForm, TaskPoolBoard } from "./live-task-pool";

const fixtureItem = {
  id: "tp_fixture", title: "检查交付", request: "检查交付", status: "review", revision: 3,
  assignee_kind: "bot", assignee_id: "builder", attachment_ids: [], updated_at: "2026-11-20T00:00:00Z",
  latest_progress: { note: "构建通过" },
};

const fixtureTargets = [{ id: "builder", kind: "bot", name: "构建助手" }];

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function mount(element: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <Routes>
          <Route path="/" element={element} />
          <Route path="/live/task-pool/:itemId" element={<p>任务已创建</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

test("the board renders all five columns and puts a delivery in review", async () => {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => Response.json(url.endsWith("/targets")
    ? { targets: fixtureTargets } : { items: [fixtureItem] })));
  mount(<TaskPoolBoard />);
  const review = await screen.findByRole("region", { name: "待你验收" });
  expect(within(review).getByRole("link").getAttribute("href")).toBe("/live/task-pool/tp_fixture");
  expect(within(review).getByText("构建通过")).toBeTruthy();
  await within(review).findByText("构建助手 · 待你验收");
  for (const name of ["待分配", "处理中", "卡住", "已完成／已取消"]) {
    expect(within(screen.getByRole("region", { name })).queryByRole("link")).toBeNull();
  }
  expect(screen.getByRole("link", { name: "新任务" }).getAttribute("href")).toBe("/live/task-pool/new");
});

test("retrying the same submission keeps its key, target and uploaded attachment", async () => {
  const uploads = vi.fn();
  const attempts = vi.fn();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/targets")) return Response.json({ targets: fixtureTargets });
    if (url === "/api/attachments") {
      uploads(init);
      return Response.json({ id: "att_fixture" });
    }
    attempts(init);
    if (attempts.mock.calls.length === 1) return Response.json({ error: { message: "暂时无法提交" } }, { status: 503 });
    return Response.json({ item: fixtureItem }, { status: 201 });
  }));
  mount(<NewTaskPoolForm />);
  await screen.findByRole("option", { name: "构建助手（Bot）" });
  fireEvent.change(screen.getByLabelText("任务内容（必填）"), { target: { value: "  检查交付  " } });
  fireEvent.change(screen.getByLabelText("负责人"), { target: { value: "bot:builder" } });
  fireEvent.change(screen.getByLabelText("附件"), { target: { files: [new File(["test"], "check.txt")] } });
  fireEvent.click(screen.getByRole("button", { name: "创建任务" }));
  await screen.findByText("暂时无法提交");
  await waitFor(() => expect((screen.getByRole("button", { name: "创建任务" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "创建任务" }));
  await screen.findByText("任务已创建");
  expect(uploads).toHaveBeenCalledOnce();
  expect(attempts).toHaveBeenCalledTimes(2);
  const [first, second] = attempts.mock.calls.map(([init]) => init as RequestInit);
  expect(first.headers).toMatchObject({ "idempotency-key": expect.stringMatching(/^[a-f0-9]{32}$/) });
  expect(second.headers).toEqual(first.headers);
  expect(JSON.parse(String(second.body))).toEqual({ request: "检查交付", attachment_ids: ["att_fixture"], assignee_kind: "bot", assignee_id: "builder" });
});
