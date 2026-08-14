import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
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
  delete process.env.WORKER_AUTH_TOKEN_FILE;
});

describe("worker HTTP API", () => {
  it("reports an unavailable health state until a token is configured", async () => {
    const response = await createApp().request("/health");
    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toMatchObject({
      status: "unavailable",
      auth_configured: false,
      error: "Worker Token 未配置",
    });
  });

  it("requires the configured bearer token", async () => {
    const generate = vi.fn(async (_request: GenerateRequest) => result);
    const app = createApp({ generate, auth: () => ({ token: "expected" }) });
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

  it("reads the bearer token from the Compose environment", async () => {
    process.env.WORKER_AUTH_TOKEN = "compose-worker-token";
    const generate = vi.fn(async (_request: GenerateRequest) => result);
    const app = createApp({ generate });

    const response = await app.request("/v1/generate", {
      method: "POST",
      headers: {
        "content-type": "application/json",
        authorization: "Bearer compose-worker-token",
      },
      body: JSON.stringify(validBody),
    });

    expect(response.status).toBe(200);
    expect(generate).toHaveBeenCalledTimes(1);
  });

  it("never enables an unauthenticated fallback", async () => {
    const app = createApp({ generate: async () => result });
    const response = await app.request("/v1/generate", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(validBody),
    });
    expect(response.status).toBe(503);
    await expect(response.json()).resolves.toMatchObject({ error: { code: "AUTH_NOT_CONFIGURED" } });
  });

  it("reloads a rotated token file without restarting the Worker", async () => {
    const directory = mkdtempSync(join(tmpdir(), "film-fusion-worker-auth-"));
    const tokenFile = join(directory, "token");
    process.env.WORKER_AUTH_TOKEN_FILE = tokenFile;
    writeFileSync(tokenFile, "first-token", { mode: 0o600 });
    const generate = vi.fn(async (_request: GenerateRequest) => result);
    const app = createApp({ generate });

    const first = await app.request("/v1/generate", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: "Bearer first-token" },
      body: JSON.stringify(validBody),
    });
    expect(first.status).toBe(200);

    writeFileSync(tokenFile, "rotated-token", { mode: 0o600 });
    const stale = await app.request("/v1/generate", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: "Bearer first-token" },
      body: JSON.stringify(validBody),
    });
    expect(stale.status).toBe(401);
    const rotated = await app.request("/v1/generate", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: "Bearer rotated-token" },
      body: JSON.stringify(validBody),
    });
    expect(rotated.status).toBe(200);

    rmSync(directory, { recursive: true, force: true });
  });

  it("returns structured 422 validation failures", async () => {
    const app = createApp({
      generate: async () => result,
      auth: () => ({ token: "expected" }),
    });
    const response = await app.request("/v1/generate", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: "Bearer expected" },
      body: JSON.stringify({ ...validBody, source: { url_template: "https://example.com", extra: true } }),
    });
    expect(response.status).toBe(422);
    await expect(response.json()).resolves.toMatchObject({ error: { code: "VALIDATION_FAILED" } });
  });
});
