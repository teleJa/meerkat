import { createHash, randomBytes } from "node:crypto";

export function sha256Hex(value: string): string {
  return createHash("sha256").update(value, "utf8").digest("hex");
}

export function generatePairingToken(): string {
  return "mkp_" + randomBytes(24).toString("hex");
}

export function generateDeviceSecret(): string {
  return randomBytes(32).toString("hex");
}

export function generateDeviceId(): string {
  return "dev_" + randomBytes(4).toString("hex");
}

export function safeEqual(a: string, b: string): boolean {
  const left = createHash("sha256").update(a, "utf8").digest();
  const right = createHash("sha256").update(b, "utf8").digest();
  return left.length === right.length && left.equals(right);
}
export function generateSessionId(): string {
  return randomBytes(32).toString("hex");
}

export function generateCsrfToken(): string {
  return randomBytes(16).toString("hex");
}
