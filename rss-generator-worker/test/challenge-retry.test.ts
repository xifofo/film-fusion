import { describe, expect, it, vi } from "vitest";
import { detectChallenge } from "../src/challenge.js";
import { WorkerError } from "../src/errors.js";
import { withRetry } from "../src/retry.js";

describe("challenge detection and retry", () => {
  it("detects common challenge responses without trying to solve them", () => {
    expect(detectChallenge(403, { "cf-mitigated": "challenge" }, "")).toBe("cf-mitigated header");
    expect(detectChallenge(503, {}, "<title>Just a moment...</title>")).toContain("challenge");
    expect(detectChallenge(200, {}, "ordinary article content")).toBeUndefined();
  });

  it("retries only retryable errors with exponential backoff", async () => {
    let calls = 0;
    const operation = vi.fn(async () => {
      calls += 1;
      if (calls < 3) throw new WorkerError("FETCH_FAILED", "temporary", 502, { retryable: true });
      return "ok";
    });
    const sleep = vi.fn(async () => undefined);
    await expect(withRetry(operation, { retries: 2, sleep, random: () => 0.5 })).resolves.toBe("ok");
    expect(operation).toHaveBeenCalledTimes(3);
    expect(sleep).toHaveBeenNthCalledWith(1, 250);
    expect(sleep).toHaveBeenNthCalledWith(2, 500);
  });
});
