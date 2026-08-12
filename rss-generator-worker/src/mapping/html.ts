import { load, type Cheerio, type CheerioAPI } from "cheerio";
import type { AnyNode } from "domhandler";
import { WorkerError } from "../errors.js";
import type {
  DetailFetcher,
  FeedItem,
  HTMLMapping,
  ItemFieldMapping,
} from "../types.js";
import {
  enclosure,
  mapWithConcurrency,
  normalizeDate,
  requireItemTitle,
  resolveUrl,
  textValue,
} from "./common.js";

interface ParsedExpression {
  selector: string;
  mode: "text" | "html" | "attr";
  attribute?: string;
}

export function parseHtmlExpression(expression: string): ParsedExpression {
  const trimmed = expression.trim();
  const suffix = /::(text|html|attr\(([A-Za-z_:][-A-Za-z0-9_:.]*)\))$/.exec(trimmed);
  if (!suffix) return { selector: trimmed, mode: "text" };
  const selector = trimmed.slice(0, suffix.index).trim();
  if (suffix[2]) {
    return { selector, mode: "attr", attribute: suffix[2]! };
  }
  return { selector, mode: suffix[1] as "text" | "html" };
}

function nodesFor(scope: Cheerio<AnyNode>, selector: string): Cheerio<AnyNode> {
  return selector ? scope.find(selector) : scope;
}

function extractAll(scope: Cheerio<AnyNode>, expression: string): string[] {
  const parsed = parseHtmlExpression(expression);
  const nodes = nodesFor(scope, parsed.selector);
  const values: string[] = [];
  nodes.each((_index, node) => {
    const selected = scope._make(node);
    const value = parsed.mode === "html"
      ? selected.html()
      : parsed.mode === "attr"
        ? selected.attr(parsed.attribute!)
        : selected.text();
    const normalized = textValue(value);
    if (normalized !== undefined) values.push(normalized);
  });
  return values;
}

function extractFirst(scope: Cheerio<AnyNode>, expression?: string): string | undefined {
  return expression ? extractAll(scope, expression)[0] : undefined;
}

function normalizedMapping(mapping: HTMLMapping): {
  itemSelector: string;
  fields: ItemFieldMapping;
  detailLink?: string;
  detailContent?: string;
  detailConcurrency: number;
} {
  const itemSelector = mapping.item ?? mapping.items ?? mapping.list;
  const topLevelFields = Object.fromEntries(
    Object.entries(mapping).filter(([key]) =>
      ![
        "item", "items", "list", "fields", "detail_link", "detail_content", "detail_concurrency",
        "wait_until", "wait_for_selector", "render_delay_ms",
      ].includes(key),
    ),
  ) as Partial<ItemFieldMapping>;
  const fields = mapping.fields ?? topLevelFields;
  if (!itemSelector) {
    throw new WorkerError("INVALID_HTML_MAPPING", "HTML mapping requires item, items, or list selector", 422);
  }
  if (!fields.title) {
    throw new WorkerError("INVALID_HTML_MAPPING", "HTML mapping requires a title field", 422);
  }
  return {
    itemSelector,
    fields: fields as ItemFieldMapping,
    ...(mapping.detail_link ? { detailLink: mapping.detail_link } : {}),
    ...(mapping.detail_content ? { detailContent: mapping.detail_content } : {}),
    detailConcurrency: mapping.detail_concurrency ?? 4,
  };
}

function extractEnclosures(
  scope: Cheerio<AnyNode>,
  fields: ItemFieldMapping,
  baseUrl: string,
) {
  const definition = fields.enclosures ?? fields.enclosure;
  if (typeof definition === "string") {
    return extractAll(scope, definition)
      .map((url) => enclosure(url, baseUrl))
      .filter((value) => value !== undefined);
  }
  const objectMapping = definition ?? (
    fields.enclosure_url
      ? {
          url: fields.enclosure_url,
          ...(fields.enclosure_type ? { type: fields.enclosure_type } : {}),
          ...(fields.enclosure_length ? { length: fields.enclosure_length } : {}),
          ...(fields.enclosure_title ? { title: fields.enclosure_title } : {}),
        }
      : undefined
  );
  if (!objectMapping) return [];
  const urls = extractAll(scope, objectMapping.url);
  const types = objectMapping.type ? extractAll(scope, objectMapping.type) : [];
  const lengths = objectMapping.length ? extractAll(scope, objectMapping.length) : [];
  const titles = objectMapping.title ? extractAll(scope, objectMapping.title) : [];
  return urls
    .map((url, index) => enclosure(url, baseUrl, types[index] ?? types[0], lengths[index] ?? lengths[0], titles[index] ?? titles[0]))
    .filter((value) => value !== undefined);
}

function baseItem(scope: Cheerio<AnyNode>, fields: ItemFieldMapping, baseUrl: string): Partial<FeedItem> {
  const categoriesExpression = fields.categories ?? fields.category;
  const categories = categoriesExpression ? extractAll(scope, categoriesExpression) : [];
  const enclosures = extractEnclosures(scope, fields, baseUrl);
  const link = resolveUrl(extractFirst(scope, fields.link), baseUrl);
  const title = extractFirst(scope, fields.title);
  return {
    ...(title ? { title } : {}),
    ...(link ? { link } : {}),
    ...(extractFirst(scope, fields.description)
      ? { description: extractFirst(scope, fields.description)! }
      : {}),
    ...(extractFirst(scope, fields.content) ? { content: extractFirst(scope, fields.content)! } : {}),
    ...(normalizeDate(extractFirst(scope, fields.date))
      ? { date: normalizeDate(extractFirst(scope, fields.date))! }
      : {}),
    ...(extractFirst(scope, fields.author) ? { author: extractFirst(scope, fields.author)! } : {}),
    ...(categories.length > 0 ? { categories } : {}),
    ...(extractFirst(scope, fields.guid) ? { guid: extractFirst(scope, fields.guid)! } : {}),
    ...(enclosures.length > 0 ? { enclosures } : {}),
  };
}

export async function mapHtmlFeed(
  html: string,
  baseUrl: string,
  mapping: HTMLMapping,
  detailFetcher?: DetailFetcher,
  maxItems = 100,
): Promise<FeedItem[]> {
  const normalized = normalizedMapping(mapping);
  const $ = load(html);
  const records = $(normalized.itemSelector).toArray().slice(0, maxItems);
  if (records.length === 0) {
    throw new WorkerError(
      "NO_ITEMS_MATCHED",
      `HTML item selector matched no elements: ${normalized.itemSelector}`,
      502,
    );
  }

  return mapWithConcurrency(records, normalized.detailConcurrency, async (record, index) => {
    const scope = $(record);
    const item = baseItem(scope, normalized.fields, baseUrl);
    if (normalized.detailLink && normalized.detailContent && detailFetcher) {
      const detailUrl = resolveUrl(extractFirst(scope, normalized.detailLink), baseUrl);
      if (detailUrl) {
        if (!item.link) item.link = detailUrl;
        const detail = await detailFetcher(detailUrl);
        const detailDocument: CheerioAPI = load(detail.body);
        const content = extractFirst(detailDocument.root(), normalized.detailContent);
        if (content) item.content = content;
      }
    }
    return requireItemTitle(item, index);
  });
}
