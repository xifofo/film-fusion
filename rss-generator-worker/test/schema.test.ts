import { describe, expect, it } from "vitest";
import { generateRequestSchema } from "../src/schema.js";

const base = {
  feed: { title: "News" },
  source: { url_template: "https://example.com/{{params.category}}" },
  params: { category: "movies" },
};

describe("generate request schema", () => {
  it("accepts the flat zero-code HTML contract and bounded item count", () => {
    expect(
      generateRequestSchema.parse({
        ...base,
        kind: "browser",
        selectors: {
          item: ".post",
          title: ".title::text",
          link: ".title::attr(href)",
          detail_link: ".title::attr(href)",
          detail_content: "article::html",
          wait_until: "networkidle",
          wait_for_selector: ".post",
          render_delay_ms: "500",
        },
        max_items: 100,
      }).max_items,
    ).toBe(100);
  });

  it("rejects missing titles, unknown keys, browser POST and GET bodies", () => {
    expect(() =>
      generateRequestSchema.parse({
        ...base,
        feed: {},
        kind: "http_json",
        mapping: { items: "items", title: "title" },
      }),
    ).toThrow();
    expect(() =>
      generateRequestSchema.parse({
        ...base,
        kind: "http_json",
        mapping: { items: "items", title: "title" },
        attacker_override: true,
      }),
    ).toThrow();
    expect(() =>
      generateRequestSchema.parse({
        ...base,
        kind: "browser",
        source: { ...base.source, method: "POST", body_template: "q=x" },
        selectors: { item: ".post", title: ".title" },
      }),
    ).toThrow();
    expect(() =>
      generateRequestSchema.parse({
        ...base,
        kind: "http_html",
        source: { ...base.source, method: "GET", body_template: "q=x" },
        selectors: { item: ".post", title: ".title" },
      }),
    ).toThrow();
  });
});
