import { describe, expect, it } from "vitest";
import { deriveStatus } from "../src/status";

const now = new Date("2026-11-06T05:00:00.000Z");
const ago = (seconds: number) => new Date(now.getTime() - seconds * 1000).toISOString();

describe("deriveStatus", () => {
  it("is offline when never seen", () => {
    expect(deriveStatus(null, now, 30, 60)).toEqual({ status: "offline", ageSeconds: null });
  });

  it("is online within 30 seconds", () => {
    expect(deriveStatus(ago(5), now, 30, 60)).toEqual({ status: "online", ageSeconds: 5 });
  });

  it("is stale between 30 and 60 seconds", () => {
    expect(deriveStatus(ago(45), now, 30, 60)).toEqual({ status: "stale", ageSeconds: 45 });
  });

  it("is offline after 60 seconds", () => {
    expect(deriveStatus(ago(61), now, 30, 60)).toEqual({ status: "offline", ageSeconds: 61 });
  });

  it("clamps future timestamps to zero", () => {
    const future = new Date(now.getTime() + 5000).toISOString();
    expect(deriveStatus(future, now, 30, 60)).toEqual({ status: "online", ageSeconds: 0 });
  });

  it("treats invalid timestamps as offline", () => {
    expect(deriveStatus("not-a-date", now, 30, 60)).toEqual({ status: "offline", ageSeconds: null });
  });
});
