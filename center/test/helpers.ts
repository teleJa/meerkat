import { buildApp } from "../src/app";
import { createConfig, type CenterConfig } from "../src/config";
import { openDb, type Db } from "../src/db";

export const ADMIN_TOKEN = "test-admin-token-123456";

export interface TestContext {
  app: ReturnType<typeof buildApp>;
  db: Db;
  config: CenterConfig;
}

export function testConfig(overrides: Partial<CenterConfig> = {}): CenterConfig {
  return createConfig({
    adminToken: ADMIN_TOKEN,
    dataDir: "/tmp/meerkat-center-test",
    dbPath: ":memory:",
    webDistPath: "/tmp/meerkat-center-test-webdist-missing",
    ...overrides,
  });
}

export function testContext(overrides: Partial<CenterConfig> = {}): TestContext {
  const config = testConfig(overrides);
  const db = openDb(":memory:");
  const app = buildApp({ config, db });
  return { app, db, config };
}

export const adminHeaders = { authorization: "Bearer " + ADMIN_TOKEN };

export async function createToken(
  app: ReturnType<typeof buildApp>,
  label = "test"
): Promise<{ token: string; expires_at: string; label: string | null }> {
  const response = await app.inject({
    method: "POST",
    url: "/api/pairing-tokens",
    headers: adminHeaders,
    payload: { label },
  });
  if (response.statusCode !== 201) throw new Error("create token failed: " + response.body);
  return response.json();
}

export async function registerEdge(
  app: ReturnType<typeof buildApp>,
  token: string,
  overrides: Record<string, unknown> = {}
): Promise<{ device_id: string; device_secret: string }> {
  const response = await app.inject({
    method: "POST",
    url: "/api/edges/register",
    payload: {
      pairing_token: token,
      protocol_version: "1",
      edge_version: "0.1.0-test",
      device: {
        name: "Test Mac",
        hostname: "test-mac.local",
        os: "macOS 15.4",
        arch: "arm64",
        role: "Edge · 测试",
        capabilities: ["metrics"],
        boot_id: "boot_test01",
        ...overrides,
      },
    },
  });
  if (response.statusCode !== 200) throw new Error("register failed: " + response.body);
  return response.json();
}
