import type { Agent } from "node:http";
import { HttpProxyAgent } from "http-proxy-agent";
import { HttpsProxyAgent } from "https-proxy-agent";
import iconv from "iconv-lite";
import fetch, { type RequestInit, type Response } from "node-fetch";
import { SocksProxyAgent } from "socks-proxy-agent";
import { assertNoChallenge } from "./challenge.js";
import { WorkerError } from "./errors.js";
import { assertPublicHttpUrl, assertSafeProxy } from "./security.js";
import type { FetchedDocument, ProxyConfig } from "./types.js";

export interface HttpFetchOptions {
  method?: "GET" | "POST";
  body?: string;
  headers?: Record<string, string>;
  cookie?: string;
  proxy?: ProxyConfig;
  timeoutMs: number;
  maxResponseBytes: number;
  maxRedirects: number;
}

export interface HttpClientDependencies {
  fetchImpl?: typeof fetch;
  assertSafeUrl?: typeof assertPublicHttpUrl;
}

function proxyAgent(proxy: ProxyConfig | undefined, target: URL): Agent | undefined {
  if (!proxy) return undefined;
  const proxyUrl = new URL(proxy.server);
  if (proxyUrl.protocol.startsWith("socks")) return new SocksProxyAgent(proxyUrl) as Agent;
  if (target.protocol === "https:") return new HttpsProxyAgent(proxyUrl) as Agent;
  return new HttpProxyAgent(proxyUrl) as Agent;
}

async function readLimitedBody(response: Response, limit: number): Promise<string> {
  const declaredLength = Number(response.headers.get("content-length") ?? 0);
  if (Number.isFinite(declaredLength) && declaredLength > limit) {
    throw new WorkerError(
      "RESPONSE_TOO_LARGE",
      `Source response exceeds the ${limit} byte limit`,
      502,
    );
  }
  if (!response.body) return "";

  const chunks: Buffer[] = [];
  let total = 0;
  for await (const chunk of response.body) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    total += buffer.length;
    if (total > limit) {
      (response.body as unknown as { destroy(): void }).destroy();
      throw new WorkerError(
        "RESPONSE_TOO_LARGE",
        `Source response exceeds the ${limit} byte limit`,
        502,
      );
    }
    chunks.push(buffer);
  }
  const declaredCharset = /charset\s*=\s*["']?([^;\s"']+)/i.exec(response.headers.get("content-type") ?? "")?.[1];
  const charset = declaredCharset?.toLowerCase() === "gb2312" ? "gbk" : declaredCharset ?? "utf-8";
  const encoding = iconv.encodingExists(charset) ? charset : "utf-8";
  return iconv.decode(Buffer.concat(chunks), encoding);
}

function redirectedMethod(status: number, method: "GET" | "POST"): "GET" | "POST" {
  return method === "POST" && [301, 302, 303].includes(status) ? "GET" : method;
}

export async function fetchHttpDocument(
  sourceUrl: string,
  options: HttpFetchOptions,
  dependencies: HttpClientDependencies = {},
): Promise<FetchedDocument> {
  const fetchImpl = dependencies.fetchImpl ?? fetch;
  const assertSafeUrl = dependencies.assertSafeUrl ?? assertPublicHttpUrl;
  await assertSafeProxy(options.proxy);
  let current = sourceUrl;
  let method = options.method ?? "GET";
  let body = options.body;
  let headers = { ...(options.headers ?? {}) };
  let cookie = options.cookie;

  for (let redirects = 0; redirects <= options.maxRedirects; redirects += 1) {
    const safeUrl = await assertSafeUrl(current);
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), options.timeoutMs);
    let response: Response;
    try {
      const requestHeaders = {
        "accept": "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8",
        "user-agent": "FilmFusion-RSS-Generator/0.1",
        ...headers,
        ...(cookie ? { cookie } : {}),
      };
      const init: RequestInit = {
        method,
        headers: requestHeaders,
        redirect: "manual",
        signal: controller.signal,
        agent: proxyAgent(options.proxy, safeUrl),
      };
      if (method === "POST") init.body = body ?? "";
      response = await fetchImpl(safeUrl, init);
    } catch (error) {
      if (error instanceof WorkerError) throw error;
      if (controller.signal.aborted) {
        throw new WorkerError("FETCH_TIMEOUT", "Source request timed out", 504, {
          retryable: true,
          cause: error,
        });
      }
      throw new WorkerError("FETCH_FAILED", "Could not fetch source URL", 502, {
        retryable: true,
        cause: error,
      });
    } finally {
      clearTimeout(timeout);
    }

    if ([301, 302, 303, 307, 308].includes(response.status)) {
      const location = response.headers.get("location");
      (response.body as unknown as { destroy?(): void } | null)?.destroy?.();
      if (!location) {
        throw new WorkerError("INVALID_REDIRECT", "Source redirect has no Location header", 502);
      }
      if (redirects === options.maxRedirects) {
        throw new WorkerError("TOO_MANY_REDIRECTS", "Source exceeded the redirect limit", 502);
      }
      current = new URL(location, safeUrl).toString();
      const crossOrigin = new URL(current).origin !== safeUrl.origin;
      if (crossOrigin && method === "POST" && [307, 308].includes(response.status)) {
        throw new WorkerError(
          "UNSAFE_CROSS_ORIGIN_REDIRECT",
          "Refusing to forward a POST body across origins",
          502,
        );
      }
      if (crossOrigin) {
        headers = Object.fromEntries(
          Object.entries(headers).filter(([name]) =>
            !/(authorization|cookie|credential|password|secret|signature|token|api[-_]?key|^key$)/i.test(name),
          ),
        );
        cookie = undefined;
      }
      const nextMethod = redirectedMethod(response.status, method);
      if (nextMethod === "GET") body = undefined;
      method = nextMethod;
      continue;
    }

    const responseBody = await readLimitedBody(response, options.maxResponseBytes);
    assertNoChallenge(response.status, response.headers as unknown as Headers, responseBody);
    if (!response.ok) {
      throw new WorkerError(
        "UPSTREAM_HTTP_ERROR",
        `Source returned HTTP ${response.status}`,
        502,
        {
          retryable: response.status === 408 || response.status === 425 || response.status >= 500,
          details: { upstream_status: response.status },
        },
      );
    }
    return {
      body: responseBody,
      finalUrl: safeUrl.toString(),
      status: response.status,
      contentType: response.headers.get("content-type") ?? "",
      usedBrowser: false,
    };
  }

  throw new WorkerError("TOO_MANY_REDIRECTS", "Source exceeded the redirect limit", 502);
}
