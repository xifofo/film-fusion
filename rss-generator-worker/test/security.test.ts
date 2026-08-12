import { describe, expect, it } from "vitest";
import {
  isPrivateAddress,
  redactSecrets,
  validBearerToken,
  validateSourceHeaders,
} from "../src/security.js";

describe("security helpers", () => {
  it("recognizes non-public address ranges", () => {
    for (const address of ["127.0.0.1", "10.0.0.1", "172.16.1.1", "192.168.1.1", "169.254.1.1", "::1", "fc00::1"]) {
      expect(isPrivateAddress(address), address).toBe(true);
    }
    expect(isPrivateAddress("8.8.8.8")).toBe(false);
    expect(isPrivateAddress("2606:4700:4700::1111")).toBe(false);
  });

  it("blocks hop-by-hop and host override headers", () => {
    expect(() => validateSourceHeaders({ Host: "internal" })).toThrowError(/not allowed/i);
    expect(validateSourceHeaders({ "X-API-Key": "secret", Accept: "application/json" })).toEqual({
      "x-api-key": "secret",
      accept: "application/json",
    });
  });

  it("redacts nested secrets and sensitive query values", () => {
    expect(
      redactSecrets({
        authorization: "Bearer abc",
        source: "https://example.com/feed?token=abc&category=movie",
      }),
    ).toEqual({
      authorization: "[REDACTED]",
      source: "https://example.com/feed?token=%5BREDACTED%5D&category=movie",
    });
  });

  it("compares bearer tokens", () => {
    expect(validBearerToken("Bearer expected", "expected")).toBe(true);
    expect(validBearerToken("Bearer nope", "expected")).toBe(false);
  });
});
