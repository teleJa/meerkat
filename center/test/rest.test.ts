import { afterEach, describe, expect, it } from "vitest";
import { adminHeaders, createToken, registerEdge, testContext } from "./helpers";
import type { TestContext } from "./helpers";

let current: TestContext | undefined;

function ctx(): TestContext {
  current = testContext();
  return current;
}

afterEach(async () => {
  if (current) {
    await current.app.close();
    current.db.close();
    current = undefined;
  }
});

describe("health", () => {
  it("returns ok without auth", async () => {
    const { app } = ctx();
    const response = await app.inject({ method: "GET", url: "/api/health" });
    expect(response.statusCode).toBe(200);
    const body = response.json();
    expect(body.ok).toBe(true);
    expect(body.version).toBe("0.1.0");
  });
});

describe("admin auth", () => {
  it("rejects device list without token", async () => {
    const { app } = ctx();
    const response = await app.inject({ method: "GET", url: "/api/devices" });
    expect(response.statusCode).toBe(401);
    expect(response.json().error.code).toBe("unauthorized");
  });

  it("rejects wrong token", async () => {
    const { app } = ctx();
    const response = await app.inject({
      method: "GET",
      url: "/api/devices",
      headers: { authorization: "Bearer wrong" },
    });
    expect(response.statusCode).toBe(401);
  });

  it("rejects pairing token creation without token", async () => {
    const { app } = ctx();
    const response = await app.inject({ method: "POST", url: "/api/pairing-tokens", payload: {} });
    expect(response.statusCode).toBe(401);
  });
});

describe("pairing and registration", () => {
  it("creates one-time pairing tokens", async () => {
    const { app } = ctx();
    const token = await createToken(app);
    expect(token.token.startsWith("mkp_")).toBe(true);
    expect(token.token.length).toBeGreaterThan(20);
    expect(Date.parse(token.expires_at)).toBeGreaterThan(Date.now());
  });

  it("registers a device and lists it as offline with null metrics", async () => {
    const { app } = ctx();
    const token = await createToken(app);
    const registered = await registerEdge(app, token.token);
    expect(registered.device_id.startsWith("dev_")).toBe(true);
    expect(registered.device_secret).toHaveLength(64);

    const list = await app.inject({ method: "GET", url: "/api/devices", headers: adminHeaders });
    expect(list.statusCode).toBe(200);
    const devices = list.json().devices;
    expect(devices).toHaveLength(1);
    expect(devices[0].id).toBe(registered.device_id);
    expect(devices[0].status).toBe("offline");
    expect(devices[0].metrics).toBeNull();
    expect(devices[0].capabilities).toEqual(["metrics"]);
  });

  it("rejects token reuse", async () => {
    const { app } = ctx();
    const token = await createToken(app);
    await registerEdge(app, token.token);
    const second = await app.inject({
      method: "POST",
      url: "/api/edges/register",
      payload: {
        pairing_token: token.token,
        protocol_version: "1",
        edge_version: "0.1.0-test",
        device: {
          name: "Second",
          hostname: "second.local",
          os: "macOS 15.4",
          arch: "arm64",
          role: "Edge",
          capabilities: [],
          boot_id: "boot_test02",
        },
      },
    });
    expect(second.statusCode).toBe(401);
    expect(second.json().error.code).toBe("pairing_token_invalid");
  });

  it("rejects unsupported protocol without consuming the token", async () => {
    const { app } = ctx();
    const token = await createToken(app);
    const bad = await app.inject({
      method: "POST",
      url: "/api/edges/register",
      payload: {
        pairing_token: token.token,
        protocol_version: "2",
        edge_version: "0.1.0-test",
        device: {
          name: "Future",
          hostname: "future.local",
          os: "macOS 15.4",
          arch: "arm64",
          role: "Edge",
          capabilities: [],
          boot_id: "boot_test03",
        },
      },
    });
    expect(bad.statusCode).toBe(400);
    expect(bad.json().error.code).toBe("unsupported_protocol");

    const ok = await registerEdge(app, token.token);
    expect(ok.device_id.startsWith("dev_")).toBe(true);
  });

  it("revokes a device", async () => {
    const { app } = ctx();
    const token = await createToken(app);
    const registered = await registerEdge(app, token.token);
    const revoked = await app.inject({
      method: "POST",
      url: "/api/devices/" + registered.device_id + "/revoke",
      headers: adminHeaders,
    });
    expect(revoked.statusCode).toBe(200);
    const list = await app.inject({ method: "GET", url: "/api/devices", headers: adminHeaders });
    expect(list.json().devices[0].revoked_at).toBeTruthy();
    expect(list.json().devices[0].status).toBe("offline");
  });
});
