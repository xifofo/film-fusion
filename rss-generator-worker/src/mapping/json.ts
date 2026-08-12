import { WorkerError } from "../errors.js";
import type { FeedItem, ItemFieldMapping, JSONMapping } from "../types.js";
import {
  enclosure,
  normalizeDate,
  requireItemTitle,
  resolveUrl,
  textValue,
} from "./common.js";

type PathToken = string | number | "*";
const BLOCKED_PROPERTIES = new Set(["__proto__", "constructor", "prototype"]);

export function parseDataPath(path: string): PathToken[] {
  let source = path.trim();
  if (source === "$" || source === "@" || source === "") return [];
  if (source.startsWith("$.") || source.startsWith("@.")) source = source.slice(2);
  else if (source.startsWith("$") || source.startsWith("@")) source = source.slice(1);
  const tokens: PathToken[] = [];
  let index = 0;
  while (index < source.length) {
    if (source[index] === ".") {
      index += 1;
      continue;
    }
    if (source[index] === "[") {
      const end = source.indexOf("]", index + 1);
      if (end < 0) throw new WorkerError("INVALID_DATA_PATH", `Invalid data path: ${path}`, 422);
      let token = source.slice(index + 1, end).trim();
      if ((token.startsWith("'") && token.endsWith("'")) || (token.startsWith('"') && token.endsWith('"'))) {
        token = token.slice(1, -1);
      }
      if (token === "*") tokens.push("*");
      else if (/^\d+$/.test(token)) tokens.push(Number(token));
      else if (/^[A-Za-z_$][A-Za-z0-9_$-]*$/.test(token)) tokens.push(token);
      else throw new WorkerError("INVALID_DATA_PATH", `Invalid data path: ${path}`, 422);
      index = end + 1;
      continue;
    }
    const match = /^[A-Za-z_$][A-Za-z0-9_$-]*/.exec(source.slice(index));
    if (!match) throw new WorkerError("INVALID_DATA_PATH", `Invalid data path: ${path}`, 422);
    tokens.push(match[0]);
    index += match[0].length;
  }
  if (tokens.some((token) => typeof token === "string" && BLOCKED_PROPERTIES.has(token))) {
    throw new WorkerError("INVALID_DATA_PATH", "Unsafe property in data path", 422);
  }
  return tokens;
}

export function valuesAtPath(root: unknown, path: string): unknown[] {
  let values: unknown[] = [root];
  for (const token of parseDataPath(path)) {
    values = values.flatMap((value) => {
      if (token === "*") {
        if (Array.isArray(value)) return value;
        if (value && typeof value === "object") return Object.values(value);
        return [];
      }
      if (value === null || typeof value !== "object") return [];
      if (typeof token === "number") return Array.isArray(value) && token < value.length ? [value[token]] : [];
      return Object.hasOwn(value, token) ? [(value as Record<string, unknown>)[token]] : [];
    });
  }
  return values;
}

function first(record: unknown, path?: string): unknown {
  return path ? valuesAtPath(record, path)[0] : undefined;
}

function normalizedMapping(mapping: JSONMapping): { itemsPath: string; fields: ItemFieldMapping } {
  const itemsPath = mapping.items ?? mapping.item;
  const topLevelFields = Object.fromEntries(
    Object.entries(mapping).filter(([key]) => !["item", "items", "fields"].includes(key)),
  ) as Partial<ItemFieldMapping>;
  const fields = mapping.fields ?? topLevelFields;
  if (!itemsPath) throw new WorkerError("INVALID_JSON_MAPPING", "JSON mapping requires items or item path", 422);
  if (!fields.title) throw new WorkerError("INVALID_JSON_MAPPING", "JSON mapping requires a title field", 422);
  return { itemsPath, fields: fields as ItemFieldMapping };
}

function jsonEnclosures(record: unknown, fields: ItemFieldMapping, baseUrl: string) {
  const definition = fields.enclosures ?? fields.enclosure;
  if (typeof definition === "string") {
    return valuesAtPath(record, definition)
      .flatMap((value) => Array.isArray(value) ? value : [value])
      .map((value) => {
        if (value && typeof value === "object") {
          const object = value as Record<string, unknown>;
          return enclosure(object.url, baseUrl, object.type, object.length, object.title);
        }
        return enclosure(value, baseUrl);
      })
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
  const urls = valuesAtPath(record, objectMapping.url).flatMap((value) => Array.isArray(value) ? value : [value]);
  const types = objectMapping.type ? valuesAtPath(record, objectMapping.type).flat() : [];
  const lengths = objectMapping.length ? valuesAtPath(record, objectMapping.length).flat() : [];
  const titles = objectMapping.title ? valuesAtPath(record, objectMapping.title).flat() : [];
  return urls
    .map((url, index) => enclosure(url, baseUrl, types[index] ?? types[0], lengths[index] ?? lengths[0], titles[index] ?? titles[0]))
    .filter((value) => value !== undefined);
}

export function mapJsonFeed(payload: unknown, baseUrl: string, mapping: JSONMapping, maxItems = 100): FeedItem[] {
  const normalized = normalizedMapping(mapping);
  const selected = valuesAtPath(payload, normalized.itemsPath);
  const records = (selected.length === 1 && Array.isArray(selected[0]) ? selected[0] : selected).slice(0, maxItems);
  if (records.length === 0) {
    throw new WorkerError(
      "NO_ITEMS_MATCHED",
      `JSON items path matched no records: ${normalized.itemsPath}`,
      502,
    );
  }

  return records.map((record, index) => {
    const fields = normalized.fields;
    const categoriesRaw = fields.categories ?? fields.category;
    const categories = categoriesRaw
      ? valuesAtPath(record, categoriesRaw)
          .flatMap((value) => Array.isArray(value) ? value : [value])
          .map(textValue)
          .filter((value): value is string => value !== undefined)
      : [];
    const link = resolveUrl(first(record, fields.link), baseUrl);
    const enclosures = jsonEnclosures(record, fields, baseUrl);
    const title = textValue(first(record, fields.title));
    const item: Partial<FeedItem> = {
      ...(title ? { title } : {}),
      ...(link ? { link } : {}),
      ...(textValue(first(record, fields.description))
        ? { description: textValue(first(record, fields.description))! }
        : {}),
      ...(textValue(first(record, fields.content))
        ? { content: textValue(first(record, fields.content))! }
        : {}),
      ...(normalizeDate(first(record, fields.date)) ? { date: normalizeDate(first(record, fields.date))! } : {}),
      ...(textValue(first(record, fields.author)) ? { author: textValue(first(record, fields.author))! } : {}),
      ...(categories.length > 0 ? { categories } : {}),
      ...(textValue(first(record, fields.guid)) ? { guid: textValue(first(record, fields.guid))! } : {}),
      ...(enclosures.length > 0 ? { enclosures } : {}),
    };
    return requireItemTitle(item, index);
  });
}
