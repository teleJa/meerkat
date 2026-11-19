import path from "node:path";
import { buildApp } from "./app";
import { loadConfig } from "./config";
import { openDb } from "./db";

const config = loadConfig();
const db = openDb(config.dbPath);
const app = buildApp({
  config,
  db,
  logger: { level: process.env.LOG_LEVEL ?? "info" },
});

async function main(): Promise<void> {
  await app.listen({ host: config.host, port: config.port });
  app.log.info(
    {
      host: config.host,
      port: config.port,
      dbPath: config.dbPath,
      adminTokenFile: path.join(config.dataDir, "admin-token"),
      protocolVersion: config.protocolVersion,
      centerVersion: config.centerVersion,
    },
    "meerkat center listening (admin token is not printed)"
  );
}

async function shutdown(signal: string): Promise<void> {
  app.log.info({ signal }, "shutting down");
  try {
    await app.close();
  } finally {
    db.close();
  }
  process.exit(0);
}

process.on("SIGINT", () => void shutdown("SIGINT"));
process.on("SIGTERM", () => void shutdown("SIGTERM"));

main().catch((error) => {
  app.log.error({ err: error }, "failed to start meerkat center");
  process.exit(1);
});
