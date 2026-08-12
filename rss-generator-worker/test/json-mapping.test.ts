import { describe, expect, it } from "vitest";
import { WorkerError } from "../src/errors.js";
import { mapJsonFeed, parseDataPath, valuesAtPath } from "../src/mapping/json.js";

describe("JSON mapping", () => {
  it("supports safe dot/bracket paths and wildcards", () => {
    const value = { data: { entries: [{ title: "A" }, { title: "B" }] } };
    expect(parseDataPath("$.data.entries[*].title")).toEqual(["data", "entries", "*", "title"]);
    expect(valuesAtPath(value, "$.data.entries[*].title")).toEqual(["A", "B"]);
    expect(() => parseDataPath("$.__proto__.polluted")).toThrowError(
      expect.objectContaining<Partial<WorkerError>>({ code: "INVALID_DATA_PATH" }),
    );
  });

  it("maps items, arrays, enclosure objects and enforces max items", () => {
    const payload = {
      result: {
        posts: [
          {
            name: "One",
            href: "/one",
            tags: ["电影", "HDR"],
            media: { url: "/one.mp4", mime: "video/mp4", size: 42 },
          },
          { name: "Two", href: "/two" },
        ],
      },
    };
    const items = mapJsonFeed(
      payload,
      "https://api.example.com/v1/posts",
      {
        items: "$.result.posts",
        fields: {
          title: "name",
          link: "href",
          categories: "tags[*]",
          enclosure: {
            url: "media.url",
            type: "media.mime",
            length: "media.size",
          },
        },
      },
      1,
    );
    expect(items).toEqual([
      {
        title: "One",
        link: "https://api.example.com/one",
        categories: ["电影", "HDR"],
        enclosures: [{ url: "https://api.example.com/one.mp4", type: "video/mp4", length: 42 }],
      },
    ]);
  });
});
