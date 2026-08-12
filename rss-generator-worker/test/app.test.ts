import { afterEach, describe, expect, it, vi } from "vitest";
import { createApp } from "../src/app.js";
import type { GenerateRequest, GenerateResult } from "../src/types.js";

const validBody = {
  feed: { title: "Example" },
  kind: "http_json",
  source: { url_template: "https://example.com/feed" },
  mapping: { items: "items", title: "title" },
};

const result: GenerateResult = {
  feed: { title: "Example", link: "https://example.com/feed", items: [] },
  meta: {
    kind_requested: "http_json",
    kind_used: "http_json",
    source_url: "https://example.com/feed",
    final_url: "https://example.com/feed",
    fetched_at: "2026-08-12T00:00:00.000Z",
    duration_ms: 1,
    item_count: 0,
    browser_fallback_used: false,
  },
};

afterEach(() => {
  delete process.env.WORKER_AUTH_TOKEN;
  delete process.env.WORKER_ALLOW_UNAUTHENTICATED;
});

describe("worker HTTP API", () => {
  it("exposes an unauthenticated health endpoint", async () => {
    const response = await createApp().request("/health");
    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toMatchObject({ status: "ok" });
  });

  it("requires the configured bearer token", async () => {
    process.env.WORKER_AUTH_TOKEN = "expected";
    const generate = vi.fn(async (_request: GenerateRequest) => result);
    const app = createApp({ generate });
    const unauthorized = await app.request("/v1/generate", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(validBody),
    });
    expect(unauthorized.status).toBe(401);
    const authorized = await app.request("/v1/generate", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: "Bearer expected" },
      body: JSON.stringify(validBody),
    });
    expect(authorized.status).toBe(200);
    expect(generate).toHaveBeenCalledTimes(1);
  });

  it("does not allow unauthenticated mode unless it is explicitly enabled", async () => {
    const app = createApp({ generate: async () => result });
    const response = await app.request("/v1/generate", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(validBody),
    });
    expect(response.status).toBe(503);
    await expect(response.json()).resolves.toMatchObject({ error: { code: "AUTH_NOT_CONFIGURED" } });
  });

  it("returns structured 422 validation failures", async () => {
    process.env.WORKER_AUTH_TOKEN = "expected";
    const app = createApp({ generate: async () => result });
    const response = await app.request("/v1/generate", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: "Bearer expected" },
      body: JSON.stringify({ ...validBody, source: { url_template: "https://example.com", extra: true } }),
    });
    expect(response.status).toBe(422);
    await expect(response.json()).resolves.toMatchObject({ error: { code: "VALIDATION_FAILED" } });
  });
});
