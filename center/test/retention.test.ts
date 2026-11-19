import { afterEach, describe, expect, it } from "vitest";
import { adminHeaders, testContext } from "./helpers";
import type { TestContext } from "./helpers";
import { cleanupOldMetrics } from "../src/repo";

let current: TestContext | undefined;

afterEach(async () => {
  if (current) {
    await current.app.close();
    current.db.close();
    current = undefined;
  }
});

function ctx(): TestContext {
  current = testContext();
  return current;
}

describe("metrics retention", () => {
  it("deletes samples older than the retention window", async () => {
    const { app, db } = ctx();
    const tokenResponse = await app.inject({
      method: "POST",
      url: "/api/pairing-tokens",
      headers: adminHeaders,
      payload: { label: "retention" },
    });
    const token = tokenResponse.json().token as string;
    const register = await app.inject({
      method: "POST",
      url: "/api/edges/register",
      payload: {
        pairing_token: token,
        protocol_version: "1",
        edge_version: "0.1.0-test",
        device: {
          name: "Retention",
          hostname: "retention.local",
          os: "macOS",
          arch: "arm64",
          role: "Edge",
          capabilities: [],
          boot_id: "boot_retention",
        },
      },
    });
    const deviceId = register.json().device_id as string;
    const insert = db.prepare(
      `INSERT INTO metrics (device_id, sampled_at, received_at, cpu_pct) VALUES (?, ?, ?, ?)`
    );
    insert.run(deviceId, "2020-01-01T00:00:00.000Z", "2020-01-01T00:00:00.000Z", 10);
    insert.run(deviceId, new Date().toISOString(), new Date().toISOString(), 20);

    const deleted = cleanupOldMetrics(db, 24);
    expect(deleted).toBe(1);
    const rows = db.prepare("SELECT COUNT(*) AS count FROM metrics WHERE device_id = ?").get(deviceId) as unknown as { count: number };
    expect(rows.count).toBe(1);
  });
});
