import { browserPool, type BrowserPool } from "./browser-pool.js";
import { ChallengeError, WorkerError } from "./errors.js";
import { fetchHttpDocument, type HttpFetchOptions } from "./http-client.js";
import { mapHtmlFeed } from "./mapping/html.js";
import { mapJsonFeed } from "./mapping/json.js";
import { withRetry } from "./retry.js";
import { normalizeProxy, redactUrlForMetadata, validateSourceHeaders } from "./security.js";
import type {
  FeedItem,
  GenerateRequest,
  GenerateResult,
  HTMLMapping,
  JSONMapping,
} from "./types.js";
import { renderBodyTemplate, renderUrlTemplate } from "./url-template.js";

export interface GeneratorDependencies {
  browserPool?: Pick<BrowserPool, "withSession">;
  fetchHttp?: typeof fetchHttpDocument;
  now?: () => Date;
}

interface ExtractionResult {
  items: FeedItem[];
  finalUrl: string;
  kindUsed: GenerateRequest["kind"];
  browserFallbackUsed: boolean;
}

function httpOptions(request: GenerateRequest): HttpFetchOptions {
  return {
    method: request.source.method ?? "GET",
    ...(request.source.body_template !== undefined
      ? { body: renderBodyTemplate(request.source.body_template, request.params) }
      : {}),
    headers: validateSourceHeaders(request.headers),
    ...(request.cookie ? { cookie: request.cookie } : {}),
    ...(request.proxy ? { proxy: normalizeProxy(request.proxy)! } : {}),
    timeoutMs: request.timeouts?.request_ms ?? 15_000,
    maxResponseBytes: request.max_response_bytes ?? 5 * 1024 * 1024,
    maxRedirects: request.max_redirects ?? 5,
  };
}

function htmlMapping(request: GenerateRequest): HTMLMapping {
  return (request.selectors ?? request.mapping) as HTMLMapping;
}

async function extractWithBrowser(
  sourceUrl: string,
  request: GenerateRequest,
  pool: Pick<BrowserPool, "withSession">,
  fallback: boolean,
): Promise<ExtractionResult> {
  const proxy = normalizeProxy(request.proxy);
  return pool.withSession(sourceUrl, request, proxy, async (session) => {
    const document = await withRetry(() => session.fetchPage(sourceUrl), {
      retries: request.retries ?? 1,
    });
    const items = await mapHtmlFeed(
      document.body,
      document.finalUrl,
      htmlMapping(request),
      async (url) => {
        const detail = await withRetry(() => session.fetchPage(url), {
          retries: request.retries ?? 1,
        });
        return { body: detail.body, finalUrl: detail.finalUrl };
      },
      request.max_items ?? 100,
    );
    return {
      items,
      finalUrl: document.finalUrl,
      kindUsed: "browser",
      browserFallbackUsed: fallback,
    };
  });
}

async function extractWithHttp(
  sourceUrl: string,
  request: GenerateRequest,
  pool: Pick<BrowserPool, "withSession">,
  fetchHttp: typeof fetchHttpDocument,
): Promise<ExtractionResult> {
  const options = httpOptions(request);
  let document;
  try {
    document = await withRetry(() => fetchHttp(sourceUrl, options), {
      retries: request.retries ?? 2,
    });
  } catch (error) {
    if (error instanceof ChallengeError && request.browser_fallback) {
      return extractWithBrowser(sourceUrl, request, pool, true);
    }
    throw error;
  }

  if (request.kind === "http_json") {
    let payload: unknown;
    try {
      payload = JSON.parse(document.body);
    } catch (error) {
      throw new WorkerError("INVALID_JSON_RESPONSE", "Source did not return valid JSON", 502, {
        cause: error,
      });
    }
    return {
      items: mapJsonFeed(payload, document.finalUrl, request.mapping as JSONMapping, request.max_items ?? 100),
      finalUrl: document.finalUrl,
      kindUsed: "http_json",
      browserFallbackUsed: false,
    };
  }

  const listOrigin = new URL(document.finalUrl).origin;
  const detailOptionsFor = (url: string): HttpFetchOptions => {
    const sameOrigin = new URL(url).origin === listOrigin;
    const detailOptions: HttpFetchOptions = {
      ...options,
      method: "GET",
      ...(sameOrigin ? {} : {
        headers: Object.fromEntries(
          Object.entries(options.headers ?? {}).filter(([name]) =>
            !/(authorization|cookie|credential|password|secret|signature|token|api[-_]?key|^key$)/i.test(name),
          ),
        ),
      }),
    };
    delete detailOptions.body;
    if (!sameOrigin) delete detailOptions.cookie;
    return detailOptions;
  };
  let items: FeedItem[];
  try {
    items = await mapHtmlFeed(
      document.body,
      document.finalUrl,
      htmlMapping(request),
      async (url) => {
        const detail = await withRetry(() => fetchHttp(url, detailOptionsFor(url)), {
          retries: request.retries ?? 2,
        });
        return { body: detail.body, finalUrl: detail.finalUrl };
      },
      request.max_items ?? 100,
    );
  } catch (error) {
    if (
      request.browser_fallback &&
      error instanceof WorkerError &&
      error.code === "NO_ITEMS_MATCHED"
    ) {
      return extractWithBrowser(sourceUrl, request, pool, true);
    }
    throw error;
  }
  return {
    items,
    finalUrl: document.finalUrl,
    kindUsed: "http_html",
    browserFallbackUsed: false,
  };
}

export async function generateFeed(
  request: GenerateRequest,
  dependencies: GeneratorDependencies = {},
): Promise<GenerateResult> {
  const now = dependencies.now ?? (() => new Date());
  const pool = dependencies.browserPool ?? browserPool;
  const fetchHttp = dependencies.fetchHttp ?? fetchHttpDocument;
  const startedAt = Date.now();
  const sourceUrl = renderUrlTemplate(request.source.url_template, request.params);
  // Validate early for browser requests too; HTTP requests validate again on every redirect.
  const extracted = request.kind === "browser"
    ? await extractWithBrowser(sourceUrl, request, pool, false)
    : await extractWithHttp(sourceUrl, request, pool, fetchHttp);
  const fetchedAt = now();
  return {
    feed: {
      title: request.feed.title,
      link: request.feed.link ?? redactUrlForMetadata(extracted.finalUrl),
      ...(request.feed.description ? { description: request.feed.description } : {}),
      ...(request.feed.language ? { language: request.feed.language } : {}),
      ...(request.feed.author ? { author: request.feed.author } : {}),
      ...(request.feed.image ? { image: request.feed.image } : {}),
      ...(request.feed.updated_at ? { updated_at: request.feed.updated_at } : {}),
      items: extracted.items,
    },
    meta: {
      kind_requested: request.kind,
      kind_used: extracted.kindUsed,
      source_url: redactUrlForMetadata(sourceUrl),
      final_url: redactUrlForMetadata(extracted.finalUrl),
      fetched_at: fetchedAt.toISOString(),
      duration_ms: Math.max(0, Date.now() - startedAt),
      item_count: extracted.items.length,
      browser_fallback_used: extracted.browserFallbackUsed,
    },
  };
}
