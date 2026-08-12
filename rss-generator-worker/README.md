# FilmFusion RSS Generator Worker

An independent TypeScript execution service for FilmFusion's RSS generator. It fetches declaratively configured JSON/HTML pages, optionally renders pages in Chromium, and returns one normalized `Feed` JSON document. The Go service owns persistence, cache policy, public feed tokens, and RSS 2.0/Atom serialization; this worker is not exposed to public feed clients.

This implementation is original and does not copy RSSHub source code.

## API

### `GET /health`

No authentication is required. It reports process health and whether worker authentication is configured; it does not launch Chromium.

### `POST /v1/generate`

Use the internal service token:

```http
Authorization: Bearer <WORKER_AUTH_TOKEN>
Content-Type: application/json
```

`WORKER_AUTH_TOKEN` must be configured in production. With no token, calls are accepted **only** when `WORKER_ALLOW_UNAUTHENTICATED=true` and the immediate caller has a loopback/private address. This mode is for local development and a trusted private Docker network only.

Recommended Go configuration:

```dotenv
RSS_GENERATOR_WORKER_URL=http://rss-generator-worker:8787
RSS_GENERATOR_WORKER_TOKEN=<same value as WORKER_AUTH_TOKEN>
```

The exact Go environment variable names are owned by the FilmFusion integration; the HTTP contract is the base URL plus bearer token shown above.

## Request contract

```ts
type GenerateRequest = {
  feed: {
    title: string                 // required
    link?: string                 // defaults to final source URL, with query/fragment removed
    description?: string
    language?: string
    author?: string
    image?: string
    updated_at?: string
  }
  kind: "http_json" | "http_html" | "browser"
  source: {
    url_template: string
    method?: "GET" | "POST"     // default GET; browser only supports GET
    body_template?: string        // POST only, max 1 MiB
  }
  params?: Record<string, string | number | boolean>
  headers?: Record<string, string>
  cookie?: string
  proxy?: string | {
    server: string                // http(s)://, socks://, socks5://, socks5h://
    username?: string
    password?: string
    bypass?: string               // browser proxy bypass list
    allow_private?: boolean       // required for a private-network proxy
  }
  selectors?: HTMLMapping
  mapping?: HTMLMapping | JSONMapping
  browser_fallback?: boolean
  storage_state?: PlaywrightStorageState
  wait_until?: "load" | "domcontentloaded" | "networkidle" | "commit"
  wait_for_selector?: string
  render_delay_ms?: number        // 0..30000
  timeouts?: { request_ms?: number; browser_ms?: number }
  retries?: number                // 0..5
  max_response_bytes?: number     // default 5 MiB, hard max 50 MiB
  max_redirects?: number          // default 5, hard max 10
  max_items?: number              // default 100, hard max 500
}
```

All objects use strict validation: unknown fields fail with HTTP 422. `params` can only substitute templates and cannot overwrite headers, cookies, proxy settings, mappings, or the source definition.

### Templates

Only explicit placeholders are supported. Missing values fail with HTTP 422.

- URL and form body: `{{params.name}}` inserts `encodeURIComponent(value)`.
- JSON body: `{{json.params.name}}` inserts `JSON.stringify(value)`.

Example:

```json
{
  "source": {
    "url_template": "https://api.example.com/search",
    "method": "POST",
    "body_template": "{\"query\":{{json.params.query}},\"page\":{{json.params.page}}}"
  },
  "params": { "query": "科幻 电影", "page": 2 },
  "headers": { "content-type": "application/json" }
}
```

### HTML mapping

The zero-code HTML contract supports either `selectors` or `mapping`. Use `item`, `items`, or `list` for the repeating CSS selector, then either a `fields` object or flat fields.

Field expressions use one of:

```text
.title::text
.content::html
a::attr(href)
```

Without a suffix, `::text` is assumed. Supported item fields are `title` (required), `link`, `description`, `content`, `date`, `author`, `category`/`categories`, `guid`, and enclosure fields. Relative HTTP URLs are resolved against the final source URL. Magnet and ed2k enclosure URLs are retained.

```json
{
  "feed": { "title": "Example releases", "language": "zh-CN" },
  "kind": "http_html",
  "source": {
    "url_template": "https://example.com/{{params.category}}?page={{params.page}}"
  },
  "params": { "category": "movies", "page": 1 },
  "headers": { "accept-language": "zh-CN" },
  "cookie": "session=opaque-value",
  "selectors": {
    "item": "article.release",
    "fields": {
      "title": "h2::text",
      "link": "h2 a::attr(href)",
      "description": ".summary::html",
      "date": "time::attr(datetime)",
      "author": ".author::text",
      "categories": ".tag::text",
      "guid": "::attr(data-id)",
      "enclosure": {
        "url": "a.download::attr(href)",
        "type": "a.download::attr(data-type)",
        "length": "a.download::attr(data-length)"
      }
    },
    "detail_link": "h2 a::attr(href)",
    "detail_content": "article .full-body::html",
    "detail_concurrency": 4
  },
  "max_items": 100
}
```

When both `detail_link` and `detail_content` are present, at most `max_items` detail pages are fetched, with configurable concurrency from 1 to 10. Cross-origin detail pages do not receive the list page's Cookie, Authorization, or credential-like custom headers.

For `kind: "browser"`, browser options may be top-level or inside `selectors` for UI compatibility; top-level values win:

```json
{
  "feed": { "title": "Rendered releases" },
  "kind": "browser",
  "source": { "url_template": "https://app.example.com/releases" },
  "selectors": {
    "item": ".release-card",
    "title": ".title::text",
    "link": "a::attr(href)",
    "wait_until": "networkidle",
    "wait_for_selector": ".release-card",
    "render_delay_ms": "500"
  },
  "storage_state": {
    "cookies": [],
    "origins": [
      {
        "origin": "https://app.example.com",
        "localStorage": [{ "name": "login-state", "value": "opaque-value" }]
      }
    ]
  }
}
```

The Chromium process is pooled, but every generation call receives a new isolated browser context. Cookies and localStorage never carry over to another feed. A raw `cookie` string is injected as origin-scoped cookies, not as a global browser header. The worker detects common challenge/CAPTCHA pages and reports `ANTI_BOT_CHALLENGE`; it does not solve or bypass CAPTCHAs.

### JSON mapping

JSON paths use a deliberately safe data-path subset: `$`/`@` root, dot properties, bracket properties/indexes, and `*` wildcards. Script/filter expressions are not executed.

```json
{
  "feed": { "title": "API releases" },
  "kind": "http_json",
  "source": { "url_template": "https://api.example.com/v1/releases?page={{params.page}}" },
  "params": { "page": 1 },
  "mapping": {
    "items": "$.data.items",
    "fields": {
      "title": "name",
      "link": "url",
      "description": "summary",
      "content": "content_html",
      "date": "published_at",
      "author": "author.name",
      "categories": "tags[*]",
      "guid": "id",
      "enclosure": {
        "url": "media.url",
        "type": "media.mime_type",
        "length": "media.size"
      }
    }
  }
}
```

### Response

```json
{
  "feed": {
    "title": "API releases",
    "link": "https://api.example.com/v1/releases",
    "description": "Optional description",
    "language": "zh-CN",
    "items": [
      {
        "title": "Release 1",
        "link": "https://example.com/release/1",
        "description": "Summary",
        "content": "<p>Full content</p>",
        "date": "2026-08-12T02:00:00.000Z",
        "author": "Author",
        "categories": ["Movie", "4K"],
        "guid": "release-1",
        "enclosures": [
          { "url": "https://example.com/release-1.torrent", "type": "application/x-bittorrent", "length": 42 }
        ]
      }
    ]
  },
  "meta": {
    "kind_requested": "http_json",
    "kind_used": "http_json",
    "source_url": "https://api.example.com/v1/releases",
    "final_url": "https://api.example.com/v1/releases",
    "fetched_at": "2026-08-12T02:00:01.000Z",
    "duration_ms": 120,
    "item_count": 1,
    "browser_fallback_used": false
  }
}
```

Metadata URLs intentionally omit every query parameter and fragment so static source credentials cannot leak into Go logs. When `feed.link` is omitted, its fallback is similarly sanitized. Explicit `feed.link` is treated as public feed metadata.

Errors use `{ "error": { "code", "message", "details"? } }` and never echo cookies, storage state, bearer tokens, proxy credentials, or request bodies.

## Security and resource limits

- HTTP(S) only for source and detail-page fetches; credentials embedded in source URLs are rejected.
- DNS is resolved and loopback, private, link-local, multicast, reserved, and mixed public/private answers are rejected before each fetch and browser request, including redirects.
- Redirects are handled manually. Credential-like headers and cookies are removed on cross-origin redirects. Cross-origin 307/308 POST redirects are refused so request bodies cannot leak.
- `Host`, proxy authorization, forwarding, connection, transfer and other dangerous source headers are rejected.
- HTTP/HTTPS/SOCKS proxies are supported. Private-network proxies require the explicit per-request `allow_private: true` flag.
- Default request timeout is 15 seconds; default browser timeout is 30 seconds.
- Default body limit is 5 MiB, request JSON limit is 2 MiB, item limit is 100, and detail concurrency defaults to 4.
- Retries use exponential backoff with jitter and only retry errors marked retryable.
- HTTP challenge detection can fall back to Chromium for GET HTML sources. An HTTP 200 JavaScript shell that matches no items can also fall back when `browser_fallback` is enabled.

SSRF checks protect the worker itself. The Go layer should additionally allow only administrators to save route definitions and must keep its public token endpoint bound to already-saved feed IDs, never an arbitrary caller-supplied source URL.

## Run locally

```bash
cp .env.example .env
pnpm install
pnpm exec playwright install chromium
WORKER_AUTH_TOKEN='replace-me' pnpm dev
```

Node.js 20 or later is required. The service does not automatically load `.env`; inject variables through your process manager, Compose, or shell.

Validate without launching a browser:

```bash
pnpm test
pnpm typecheck
pnpm build
```

Unit tests use mocks and pure extraction helpers, so they do not require a downloaded Chromium binary. A real browser generation call does require `pnpm exec playwright install chromium` and its OS dependencies. The provided Docker image already includes compatible Chromium and dependencies.

## Docker

```bash
docker build -t film-fusion-rss-generator-worker .
docker run --rm \
  -p 127.0.0.1:8787:8787 \
  -e WORKER_AUTH_TOKEN='replace-with-a-random-secret' \
  film-fusion-rss-generator-worker
```

The container listens on `0.0.0.0:8787`, runs as Playwright's unprivileged `pwuser`, reuses Chromium processes, and isolates each request in a fresh context.
