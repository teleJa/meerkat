#!/usr/bin/env node
// 等待 center 中某台设备变为 online 且带指标；用于联调脚本。
// 用法: node scripts/wait-device.mjs <center-url> <admin-token> [hostname] [timeoutSec]
const [url, token, hostname = "", timeoutSec = "30"] = process.argv.slice(2);

if (!url || !token) {
  console.error("usage: wait-device.mjs <center-url> <admin-token> [hostname] [timeoutSec]");
  process.exit(2);
}

const deadline = Date.now() + Number(timeoutSec) * 1000;
let lastBody = null;

async function main() {
  while (Date.now() < deadline) {
    const response = await fetch(url + "/api/devices", {
      headers: { authorization: "Bearer " + token },
    });
    if (!response.ok) throw new Error("GET /api/devices -> HTTP " + response.status);
    const body = await response.json();
    lastBody = body;
    const device = (body.devices ?? []).find(
      (item) => !hostname || item.hostname === hostname || item.name === hostname
    );
    if (device && device.status === "online" && device.metrics && device.metrics.cpu_pct !== null) {
      console.log(JSON.stringify(device, null, 2));
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  console.error("devices snapshot: " + JSON.stringify(lastBody, null, 2));
  throw new Error("设备在超时前未变为 online 或缺少指标");
}

main().catch((error) => {
  console.error(String(error.message ?? error));
  process.exit(1);
});
