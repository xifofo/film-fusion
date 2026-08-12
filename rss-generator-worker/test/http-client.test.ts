import iconv from "iconv-lite";
import fetch, { Response, type RequestInit } from "node-fetch";
import { describe, expect, it, vi } from "vitest";
import { WorkerError } from "../src/errors.js";
import { fetchHttpDocument, type HttpFetchOptions } from "../src/http-client.js";

const defaults: HttpFetchOptions = {
  headers: {},
  timeoutMs: 1_000,
  maxResponseBytes: 1024 * 1024,
  maxRedirects: 3,
};

describe("HTTP source client", () => {
  it("decodes common GB2312-labelled Chinese responses as GBK", async () => {
    const fetchImpl = vi.fn(async () =>
      new Response(iconv.encode("电影更新", "gbk"), {
        status: 200,
        headers: { "content-type": "text/html; charset=gb2312" },
      }),
    );
    const result = await fetchHttpDocument(
      "https://source.example/feed",
      defaults,
      {
        fetchImpl: fetchImpl as unknown as typeof fetch,
        assertSafeUrl: async (url) => new URL(url),
      },
    );
    expect(result.body).toBe("电影更新");
  });

  it("re-validates each redirect and blocks an unsafe target before requesting it", async () => {
    const fetchImpl = vi.fn(async () =>
      new Response(null, { status: 302, headers: { location: "http://127.0.0.1/admin" } }),
    );
    const checked: string[] = [];
    await expect(
      fetchHttpDocument("https://public.example/feed", defaults, {
        fetchImpl: fetchImpl as unknown as typeof fetch,
        assertSafeUrl: async (url) => {
          checked.push(url);
          if (url.includes("127.0.0.1")) throw new WorkerError("SSRF_BLOCKED", "blocked", 403);
          return new URL(url);
        },
      }),
    ).rejects.toMatchObject({ code: "SSRF_BLOCKED" });
    expect(checked).toEqual(["https://public.example/feed", "http://127.0.0.1/admin"]);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it("strips cookies and credential-like headers on cross-origin redirects", async () => {
    const calls: Array<{ url: string; headers: Record<string, string> }> = [];
    const fetchImpl = vi.fn(async (url: URL, init?: RequestInit) => {
      calls.push({ url: url.toString(), headers: init?.headers as Record<string, string> });
      return calls.length === 1
        ? new Response(null, { status: 302, headers: { location: "https://cdn.example/items" } })
        : new Response("ok", { status: 200, headers: { "content-type": "text/plain" } });
    });
    await fetchHttpDocument(
      "https://source.example/feed",
      {
        ...defaults,
        cookie: "session=secret",
        headers: {
          authorization: "Bearer secret",
          "x-api-key": "secret-key",
          "x-theme": "dark",
        },
      },
      {
        fetchImpl: fetchImpl as unknown as typeof fetch,
        assertSafeUrl: async (url) => new URL(url),
      },
    );

    expect(calls[0]!.headers).toMatchObject({
      authorization: "Bearer secret",
      "x-api-key": "secret-key",
      cookie: "session=secret",
    });
    expect(calls[1]!.headers).toMatchObject({ "x-theme": "dark" });
    expect(calls[1]!.headers).not.toHaveProperty("authorization");
    expect(calls[1]!.headers).not.toHaveProperty("x-api-key");
    expect(calls[1]!.headers).not.toHaveProperty("cookie");
  });

  it("refuses to forward a POST body through a cross-origin 307 redirect", async () => {
    const fetchImpl = vi.fn(async () =>
      new Response(null, { status: 307, headers: { location: "https://other.example/collect" } }),
    );
    await expect(
      fetchHttpDocument(
        "https://source.example/login",
        { ...defaults, method: "POST", body: "password=secret" },
        {
          fetchImpl: fetchImpl as unknown as typeof fetch,
          assertSafeUrl: async (url) => new URL(url),
        },
      ),
    ).rejects.toMatchObject({ code: "UNSAFE_CROSS_ORIGIN_REDIRECT" });
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it("enforces streamed response size limits", async () => {
    const fetchImpl = vi.fn(async () => new Response("123456", { status: 200 }));
    await expect(
      fetchHttpDocument(
        "https://source.example/feed",
        { ...defaults, maxResponseBytes: 5 },
        {
          fetchImpl: fetchImpl as unknown as typeof fetch,
          assertSafeUrl: async (url) => new URL(url),
        },
      ),
    ).rejects.toMatchObject({ code: "RESPONSE_TOO_LARGE" });
  });
});
