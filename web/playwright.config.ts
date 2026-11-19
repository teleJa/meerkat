import { defineConfig } from "@playwright/test";

const configuredBaseURL = process.env.MEERKAT_E2E_BASE_URL?.trim();
if (!configuredBaseURL) {
  throw new Error(
    "缺少 MEERKAT_E2E_BASE_URL。请先启动本机 Meerkat，再运行：MEERKAT_E2E_BASE_URL=http://127.0.0.1:<端口> pnpm test:e2e",
  );
}

export default defineConfig({
  testDir: "./e2e",
  timeout: 40_000,
  expect: { timeout: 8_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL: configuredBaseURL.replace(/\/+$/, ""),
    viewport: { width: 1280, height: 900 },
    headless: true,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
    permissions: ["clipboard-read", "clipboard-write"],
  },
});
