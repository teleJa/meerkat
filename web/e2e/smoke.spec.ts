import { expect, test, type Page } from "@playwright/test";

async function expectNoHorizontalOverflow(page: Page) {
  const overflow = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(overflow.scrollWidth).toBeLessThanOrEqual(overflow.clientWidth + 1);
}

async function expectCodeInsideDialog(page: Page) {
  const dialog = page.getByTestId("pairing-dialog");
  const code = dialog.getByTestId("pairing-invite-code");
  const [codeBox, dialogBox] = await Promise.all([code.boundingBox(), dialog.boundingBox()]);
  expect(codeBox).not.toBeNull();
  expect(dialogBox).not.toBeNull();
  if (!codeBox || !dialogBox) return;
  expect(codeBox.x + codeBox.width).toBeLessThanOrEqual(dialogBox.x + dialogBox.width + 1);
}

test.describe("Meerkat 发布冒烟", () => {
  test("主路由可打开且无横向溢出", async ({ page }) => {
    const routes = [
      ["实时设备", "/live"],
      ["任务看板", "/live/board"],
      ["实时任务", "/live/tasks"],
      ["实时会话", "/live/sessions"],
      ["设置", "/live/settings"],
      ["DSH", "/live/harness"],
    ];

    for (const [name, path] of routes) {
      await page.goto("/#" + path);
      await expect(page.locator("main").first()).toBeVisible();
      await page.waitForTimeout(500);
      await expectNoHorizontalOverflow(page);
      await expect(page.locator("body")).toContainText(name);
    }
  });

  test("Windows 桌面显示窗口控制按钮", async ({ page }) => {
    await page.goto("/?desktop=1&os=windows#/live");
    await expect(page.getByTestId("window-minimise")).toBeVisible();
    await expect(page.getByTestId("window-maximise")).toBeVisible();
    await expect(page.getByTestId("window-close")).toBeVisible();
  });

  test("旧版对话路由不再存在并回到 DSH", async ({ page }) => {
    await page.goto("/#/live/chat");
    await expect(page).toHaveURL(/#\/live\/harness$/);
    await expect(page.locator("body")).not.toContainText("旧版对话");
  });

  test("连接失败时显示网络配置引导", async ({ page }) => {
    await page.route("**/api/session", async (route) => {
      await route.fulfill({
        status: 502,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "bad_gateway", message: "Bad Gateway" } }),
      });
    });

    await page.goto("/#/live/harness");
    const onboarding = page.getByTestId("network-onboarding");
    await expect(onboarding).toBeVisible();
    await expect(onboarding).toContainText("设置");
    await expect(onboarding).toContainText("内嵌 Tailscale");

    await page.getByTestId("network-settings-link").click();
    await expect(page).toHaveURL(/#\/live\/settings$/);
  });

  test("接入设备弹窗：邀请码 + 二维码、复制与重新生成", async ({ page }) => {
    let inviteCount = 0;
    await page.route("**/desktop/invite", async (route) => {
      inviteCount += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          code: "MEERKAT1:e2e-invite-" + inviteCount,
          hub_url: "http://meerkat-hub:7443",
        }),
      });
    });

    await page.goto("/#/live");
    await page.getByTestId("pairing-open").click();

    const dialog = page.getByTestId("pairing-dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("heading", { name: "邀请设备加入" })).toBeVisible();

    const code = dialog.getByTestId("pairing-invite-code");
    await expect(code).toHaveText(/^MEERKAT1:e2e-invite-/);
    await expect(dialog.getByTestId("pairing-qr").locator("svg")).toBeVisible();
    await expectNoHorizontalOverflow(page);
    await expectCodeInsideDialog(page);

    const firstCode = await code.textContent();
    await dialog.getByTestId("pairing-copy").click();
    await dialog.getByTestId("pairing-regenerate").click();
    await expect.poll(async () => code.textContent()).not.toBe(firstCode);
    await expectNoHorizontalOverflow(page);
    await expectCodeInsideDialog(page);

    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
  });

  test("设置页显示节点角色只读卡片", async ({ page }) => {
    await page.goto("/#/live/settings");
    await expect(page.getByTestId("node-role-card")).toBeVisible();
    await expect(page.getByTestId("node-role-mode")).toBeVisible();
    await expect(page.getByTestId("node-role-center")).not.toBeEmpty();
    await expect(page.getByTestId("node-role-device")).not.toBeEmpty();
  });

  test("设置页在等待登录时显示 Tailscale 登录按钮", async ({ page }) => {
    await page.route("**/api/network/tsnet", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          enabled: true,
          state: "needs_login",
          hostname: "meerkat-hub",
          auth_url: "https://login.tailscale.com/a/e2e-test",
          port: 7443,
        }),
      });
    });

    await page.goto("/#/live/settings");
    await expect(page.getByTestId("hub-tailscale-login")).toBeVisible();
    await expect(page.getByTestId("hub-tailscale-login")).toContainText("打开 Tailscale 登录页");
  });

  test("实时任务：刷新、派发弹窗无横向溢出", async ({ page }) => {
    await page.goto("/#/live/tasks");
    await expect(page.locator("main").first()).toBeVisible();

    await page.getByRole("button", { name: "刷新", exact: true }).click();
    await expectNoHorizontalOverflow(page);

    await page.getByTestId("dispatch-open").first().click();
    const dialog = page.getByRole("dialog", { name: "派发任务" });
    await expect(dialog).toBeVisible();
    await expectNoHorizontalOverflow(page);

    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expectNoHorizontalOverflow(page);
  });

  test("DSH 记住上次设备，切换设备仍可回到列表", async ({ page }) => {
    await page.addInitScript(() => {
      window.localStorage.removeItem("meerkat.live.lastHarnessDevice");
    });
    await page.route("**/api/session", async (route) => {
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ authenticated: true, via: "bearer" }) });
    });
    await page.route("**/api/devices", async (route) => {
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          devices: [
            {
              id: "dev_e2e",
              name: "E2E Mac",
              hostname: "e2e-host",
              os: "darwin",
              arch: "arm64",
              role: "",
              status: "online",
              capabilities: [],
              created_at: "",
              last_seen_at: null,
              last_metrics_at: null,
              edge_version: "",
              protocol_version: "",
              boot_id: "",
              revoked_at: null,
              metrics: null,
              harness_available: true,
            },
          ],
        }),
      });
    });
    await page.route("**/api/devices/dev_e2e/harness-proxy", async (route) => {
      await route.fulfill({
        status: 404,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "harness_not_configured", message: "no dsh" } }),
      });
    });

    await page.goto("/#/live/harness/dev_e2e");
    await expect(page).toHaveURL(/#\/live\/harness\/dev_e2e$/);
    await expect(page.getByRole("link", { name: "切换设备" }).first()).toBeVisible();

    // 切到别的标签页，再点侧边栏 DSH，应直接回到上次设备。
    await page.getByRole("link", { name: "实时设备" }).first().click();
    await expect(page).toHaveURL(/#\/live$/);
    await page.getByRole("link", { name: "DSH" }).first().click();
    await expect(page).toHaveURL(/#\/live\/harness\/dev_e2e$/);

    // 「切换设备」进入列表后应停留在列表，不被自动跳转顶回去。
    await page.getByRole("link", { name: "切换设备" }).first().click();
    await expect(page).toHaveURL(/#\/live\/harness$/);
    await expect(page.getByRole("heading", { name: "DSH" })).toBeVisible();
    await expect(page.getByText("E2E Mac")).toBeVisible();
  });

  test("DSH iframe 在 Meerkat 路由切换后保持", async ({ page, request }) => {
    const response = await request.get("/api/devices");
    test.skip(!response.ok(), "当前中心没有可读取的设备接口");
    const payload = (await response.json()) as {
      devices?: Array<{ id: string; name: string; harness_available?: boolean }>;
    };
    const device = payload.devices?.find((item) => item.harness_available);
    test.skip(!device, "当前没有可用的 DSH 设备");

    await page.goto("/#/live/harness/" + encodeURIComponent(device!.id));
    const frame = page.locator("iframe").first();
    await expect(frame).toBeVisible({ timeout: 25_000 });
    await frame.evaluate((element) => {
      (element as HTMLElement).dataset.e2eKeepalive = "1";
    });

    await page.getByRole("link", { name: "实时设备" }).first().click();
    await expect(page).toHaveURL(/#\/live$/);
    await page.goBack();
    await expect(page).toHaveURL(new RegExp("#/live/harness/" + device!.id));
    await expect(frame).toBeVisible();

    const marker = await frame.evaluate((element) => (element as HTMLElement).dataset.e2eKeepalive);
    expect(marker).toBe("1");
  });
});