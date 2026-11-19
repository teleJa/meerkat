// 记住上次打开的 DSH 设备。列表页仍可从详情页的「切换设备」进入，
// 只有用户明确进入列表时才显示列表，避免 index 自动跳转把列表变成不可达。
const LAST_HARNESS_DEVICE_KEY = "meerkat.live.lastHarnessDevice";

export const HARNESS_LIST_STATE = { pickDevice: true } as const;

export function readLastHarnessDevice(): string {
  if (typeof window === "undefined") return "";
  try {
    return window.localStorage.getItem(LAST_HARNESS_DEVICE_KEY)?.trim() ?? "";
  } catch {
    return "";
  }
}

export function rememberHarnessDevice(deviceId: string): void {
  if (typeof window === "undefined" || deviceId.trim() === "") return;
  try {
    window.localStorage.setItem(LAST_HARNESS_DEVICE_KEY, deviceId);
  } catch {
    /* localStorage 不可用时只影响“记住上次设备”，不阻塞打开 DSH */
  }
}

export function forgetHarnessDevice(): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.removeItem(LAST_HARNESS_DEVICE_KEY);
  } catch {
    /* ignore */
  }
}

/** 侧边栏/面包屑的 DSH 入口：有记住的设备就直接去详情，否则去列表。 */
export function harnessEntryPath(): string {
  const deviceId = readLastHarnessDevice();
  return deviceId === "" ? "/live/harness" : "/live/harness/" + encodeURIComponent(deviceId);
}
