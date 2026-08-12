import { WorkerError } from "../errors.js";
import type { FeedEnclosure, FeedItem } from "../types.js";

export function textValue(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "string") return value.trim() || undefined;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return undefined;
}

export function normalizeDate(value: unknown): string | undefined {
  const text = textValue(value);
  if (!text) return undefined;
  const numeric = Number(text);
  const date = Number.isFinite(numeric) && /^\d{10,13}$/.test(text)
    ? new Date(text.length === 10 ? numeric * 1_000 : numeric)
    : new Date(text);
  return Number.isNaN(date.getTime()) ? text : date.toISOString();
}

export function resolveUrl(value: unknown, baseUrl: string): string | undefined {
  const text = textValue(value);
  if (!text) return undefined;
  try {
    const url = new URL(text, baseUrl);
    return ["http:", "https:", "magnet:", "ed2k:"].includes(url.protocol) ? url.toString() : undefined;
  } catch {
    return undefined;
  }
}

export function enclosure(
  url: unknown,
  baseUrl: string,
  type?: unknown,
  length?: unknown,
  title?: unknown,
): FeedEnclosure | undefined {
  const resolved = resolveUrl(url, baseUrl);
  if (!resolved) return undefined;
  const numericLength = Number(textValue(length));
  return {
    url: resolved,
    ...(textValue(type) ? { type: textValue(type)! } : {}),
    ...(Number.isFinite(numericLength) && numericLength >= 0 ? { length: numericLength } : {}),
    ...(textValue(title) ? { title: textValue(title)! } : {}),
  };
}

export function requireItemTitle(item: Partial<FeedItem>, index: number): FeedItem {
  const title = textValue(item.title);
  if (!title) {
    throw new WorkerError(
      "MAPPING_FAILED",
      `Item ${index + 1} did not produce a title`,
      502,
      { details: { item_index: index } },
    );
  }
  return { ...item, title };
}

export async function mapWithConcurrency<T, R>(
  values: T[],
  concurrency: number,
  mapper: (value: T, index: number) => Promise<R>,
): Promise<R[]> {
  const output = new Array<R>(values.length);
  let cursor = 0;
  const workers = Array.from({ length: Math.min(concurrency, values.length) }, async () => {
    while (cursor < values.length) {
      const index = cursor++;
      output[index] = await mapper(values[index]!, index);
    }
  });
  await Promise.all(workers);
  return output;
}
