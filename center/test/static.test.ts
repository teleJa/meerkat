import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { testContext } from "./helpers";
import type { TestContext } from "./helpers";

let current: TestContext | undefined;
let webDir: string | undefined;

afterEach(async () => {
  if (current) {
    await current.app.close();
    current.db.close();
    current = undefined;
  }
  if (webDir) {
    rmSync(webDir, { recursive: true, force: true });
    webDir = undefined;
  }
});

describe("web panel static serving", () => {
  it("serves index.html when a web build exists", async () => {
    webDir = mkdtempSync(path.join(tmpdir(), "meerkat-web-"));
    writeFileSync(path.join(webDir, "index.html"), "<!doctype html><title>meerkat-live-test</title>");
    current = testContext({ webDistPath: webDir });

    const index = await current.app.inject({ method: "GET", url: "/" });
    expect(index.statusCode).toBe(200);
    expect(index.body).toContain("meerkat-live-test");
    expect(String(index.headers["content-type"])).toContain("text/html");

    const health = await current.app.inject({ method: "GET", url: "/api/health" });
    expect(health.statusCode).toBe(200);
    expect(health.json().ok).toBe(true);
  });

  it("keeps API-only mode when no build exists", async () => {
    current = testContext();
    const index = await current.app.inject({ method: "GET", url: "/" });
    expect(index.statusCode).toBe(404);
  });
});
