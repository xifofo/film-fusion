import { describe, expect, it, vi } from "vitest";
import { mapHtmlFeed, parseHtmlExpression } from "../src/mapping/html.js";

describe("HTML mapping", () => {
  it("parses CSS output suffixes", () => {
    expect(parseHtmlExpression("a.headline::attr(href)")).toEqual({
      selector: "a.headline",
      mode: "attr",
      attribute: "href",
    });
    expect(parseHtmlExpression(".body::html")).toEqual({ selector: ".body", mode: "html" });
    expect(parseHtmlExpression(".title::text")).toEqual({ selector: ".title", mode: "text" });
  });

  it("extracts fields, resolves relative URLs, and supports enclosure/category lists", async () => {
    const items = await mapHtmlFeed(
      `<ul>
        <li class="post">
          <a class="title" href="/post/1"> First </a>
          <div class="summary"><b>Hello</b></div>
          <time datetime="2026-08-12T10:00:00+08:00"></time>
          <span class="tag">Movie</span><span class="tag">4K</span>
          <a class="download" href="magnet:?xt=urn:btih:abc">download</a>
        </li>
      </ul>`,
      "https://example.com/list",
      {
        item: ".post",
        fields: {
          title: ".title::text",
          link: ".title::attr(href)",
          description: ".summary::html",
          date: "time::attr(datetime)",
          categories: ".tag::text",
          enclosure: ".download::attr(href)",
        },
      },
    );

    expect(items).toEqual([
      expect.objectContaining({
        title: "First",
        link: "https://example.com/post/1",
        description: "<b>Hello</b>",
        date: "2026-08-12T02:00:00.000Z",
        categories: ["Movie", "4K"],
        enclosures: [{ url: "magnet:?xt=urn:btih:abc" }],
      }),
    ]);
  });

  it("fetches detail content with bounded concurrency and applies item limit first", async () => {
    let active = 0;
    let peak = 0;
    const detailFetcher = vi.fn(async (url: string) => {
      active += 1;
      peak = Math.max(peak, active);
      await new Promise((resolve) => setTimeout(resolve, 5));
      active -= 1;
      return { body: `<article><p>${url}</p></article>`, finalUrl: url };
    });
    const html = Array.from(
      { length: 8 },
      (_, index) => `<div class="item"><a href="/p/${index}">Title ${index}</a></div>`,
    ).join("");
    const items = await mapHtmlFeed(
      html,
      "https://example.com/",
      {
        item: ".item",
        title: "a::text",
        detail_link: "a::attr(href)",
        detail_content: "article::html",
        detail_concurrency: 2,
      },
      detailFetcher,
      3,
    );

    expect(items).toHaveLength(3);
    expect(detailFetcher).toHaveBeenCalledTimes(3);
    expect(peak).toBeLessThanOrEqual(2);
    expect(items[0]).toMatchObject({
      link: "https://example.com/p/0",
      content: "<p>https://example.com/p/0</p>",
    });
  });
});
