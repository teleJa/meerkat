import { afterEach, describe, expect, it } from "vitest";
import { ADMIN_TOKEN, adminHeaders, testContext } from "./helpers";
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

const CSRF = { "x-meerkat-request": "1" };

async function login(app: TestContext["app"], token = ADMIN_TOKEN) {
  return app.inject({
    method: "POST",
    url: "/api/session",
    headers: CSRF,
    payload: { token },
  });
}

function cookieOf(response: { headers: Record<string, unknown> }): string {
  const raw = response.headers["set-cookie"];
  const value = Array.isArray(raw) ? String(raw[0]) : String(raw ?? "");
  return value.split(";")[0] ?? "";
}

describe("browser session", () => {
  it("requires the csrf header on login", async () => {
    const { app } = ctx();
    const response = await app.inject({
      method: "POST",
      url: "/api/session",
      payload: { token: ADMIN_TOKEN },
    });
    expect(response.statusCode).toBe(403);
    expect(response.json().error.code).toBe("csrf_required");
  });

  it("rejects a bad token", async () => {
    const { app } = ctx();
    const response = await login(app, "wrong-token");
    expect(response.statusCode).toBe(401);
    expect(response.json().error.code).toBe("invalid_credentials");
  });

  it("issues an HttpOnly session cookie and authenticates subsequent requests", async () => {
    const { app } = ctx();
    const response = await login(app);
    expect(response.statusCode).toBe(200);
    expect(response.json().authenticated).toBe(true);
    const setCookie = String(response.headers["set-cookie"]);
    expect(setCookie).toContain("HttpOnly");
    expect(setCookie).toContain("SameSite=Lax");
    expect(setCookie).toContain("Path=/");

    const cookie = cookieOf(response);
    const session = await app.inject({ method: "GET", url: "/api/session", headers: { cookie } });
    expect(session.statusCode).toBe(200);
    expect(session.json().via).toBe("session");

    const devices = await app.inject({ method: "GET", url: "/api/devices", headers: { cookie } });
    expect(devices.statusCode).toBe(200);
    expect(Array.isArray(devices.json().devices)).toBe(true);
  });

  it("does not authenticate without a cookie", async () => {
    const { app } = ctx();
    const response = await app.inject({ method: "GET", url: "/api/session" });
    expect(response.statusCode).toBe(401);
  });

  it("requires the csrf header for cookie-authenticated mutations", async () => {
    const { app } = ctx();
    const cookie = cookieOf(await login(app));

    const withoutCsrf = await app.inject({
      method: "POST",
      url: "/api/pairing-tokens",
      headers: { cookie },
      payload: { label: "no-csrf" },
    });
    expect(withoutCsrf.statusCode).toBe(403);

    const withCsrf = await app.inject({
      method: "POST",
      url: "/api/pairing-tokens",
      headers: { cookie, ...CSRF },
      payload: { label: "with-csrf" },
    });
    expect(withCsrf.statusCode).toBe(201);
  });

  it("keeps bearer-token requests working without csrf", async () => {
    const { app } = ctx();
    const response = await app.inject({
      method: "POST",
      url: "/api/pairing-tokens",
      headers: adminHeaders,
      payload: { label: "bearer" },
    });
    expect(response.statusCode).toBe(201);
  });

  it("invalidates the session on logout", async () => {
    const { app } = ctx();
    const cookie = cookieOf(await login(app));
    const logout = await app.inject({
      method: "DELETE",
      url: "/api/session",
      headers: { cookie, ...CSRF },
    });
    expect(logout.statusCode).toBe(200);
    expect(String(logout.headers["set-cookie"])).toContain("Max-Age=0");

    const session = await app.inject({ method: "GET", url: "/api/session", headers: { cookie } });
    expect(session.statusCode).toBe(401);
  });

  it("rejects expired sessions", async () => {
    const { app, db } = ctx();
    const cookie = cookieOf(await login(app));
    db.prepare("UPDATE web_sessions SET expires_at = ?").run("2020-01-01T00:00:00.000Z");
    const session = await app.inject({ method: "GET", url: "/api/session", headers: { cookie } });
    expect(session.statusCode).toBe(401);
  });
});
