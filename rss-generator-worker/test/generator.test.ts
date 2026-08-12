import { describe, expect, it, vi } from "vitest";
import { WorkerError } from "../src/errors.js";
import { generateFeed } from "../src/generator.js";
import type { BrowserSession } from "../src/browser-pool.js";
import type { GenerateRequest, ProxyConfig } from "../src/types.js";

describe("feed generator", () => {
  it("falls back to an isolated browser when a JS shell has no HTTP items", async () => {
    const request: GenerateRequest = {
      feed: { title: "Dynamic" },
      kind: "http_html",
      source: { url_template: "https://example.com/feed" },
      selectors: { item: ".post", title: ".title::text" },
      browser_fallback: true,
      retries: 0,
    };
    const fetchHttp = vi.fn(async () => ({
      body: '<div id="app"></div>',
      finalUrl: "https://example.com/feed",
      status: 200,
      contentType: "text/html",
      usedBrowser: false,
    }));
    const withSession = vi.fn(
      async <T>(
        _url: string,
        _request: GenerateRequest,
        _proxy: ProxyConfig | undefined,
        callback: (session: BrowserSession) => Promise<T>,
      ) =>
        callback({
          fetchPage: async () => ({
            body: '<article class="post"><h2 class="title">Rendered</h2></article>',
            finalUrl: "https://example.com/feed?session=secret",
            status: 200,
            contentType: "text/html",
            usedBrowser: true,
          }),
        }),
    );
    const result = await generateFeed(request, {
      fetchHttp: fetchHttp as never,
      browserPool: { withSession } as never,
      now: () => new Date("2026-08-12T00:00:00Z"),
    });

    expect(result.feed.items[0]?.title).toBe("Rendered");
    expect(result.feed.link).toBe("https://example.com/feed");
    expect(result.meta).toMatchObject({
      kind_used: "browser",
      browser_fallback_used: true,
      source_url: "https://example.com/feed",
      final_url: "https://example.com/feed",
    });
    expect(withSession).toHaveBeenCalledTimes(1);
  });

  it("does not use browser fallback for unrelated extraction failures", async () => {
    const request: GenerateRequest = {
      feed: { title: "Broken" },
      kind: "http_html",
      source: { url_template: "https://example.com/feed" },
      selectors: { item: ".post", title: ".title" },
      browser_fallback: true,
      retries: 0,
    };
    const fetchHttp = vi.fn(async () => ({
      body: '<article class="post"></article>',
      finalUrl: "https://example.com/feed",
      status: 200,
      contentType: "text/html",
      usedBrowser: false,
    }));
    await expect(
      generateFeed(request, {
        fetchHttp: fetchHttp as never,
        browserPool: { withSession: vi.fn() } as never,
      }),
    ).rejects.toMatchObject<Partial<WorkerError>>({ code: "MAPPING_FAILED" });
  });
});
