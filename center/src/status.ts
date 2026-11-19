export type DeviceStatus = "online" | "stale" | "offline";

export function deriveStatus(
  lastSeenAt: string | null,
  now: Date,
  onlineThresholdSeconds: number,
  staleThresholdSeconds: number
): { status: DeviceStatus; ageSeconds: number | null } {
  if (!lastSeenAt) return { status: "offline", ageSeconds: null };
  const seen = Date.parse(lastSeenAt);
  if (Number.isNaN(seen)) return { status: "offline", ageSeconds: null };
  const ageSeconds = Math.max(0, Math.round((now.getTime() - seen) / 1000));
  if (ageSeconds <= onlineThresholdSeconds) return { status: "online", ageSeconds };
  if (ageSeconds <= staleThresholdSeconds) return { status: "stale", ageSeconds };
  return { status: "offline", ageSeconds };
}
