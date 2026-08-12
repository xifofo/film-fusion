import { chromium, type Browser, type BrowserContext, type BrowserContextOptions } from "playwright";
import { assertNoChallenge } from "./challenge.js";
import { WorkerError } from "./errors.js";
import { assertPublicHttpUrl, assertSafeProxy, validateSourceHeaders } from "./security.js";
import type { FetchedDocument, GenerateRequest, HTMLMapping, ProxyConfig, StorageState } from "./types.js";

class Semaphore {
  private available: number;
  private readonly waiters: Array<() => void> = [];

  constructor(capacity: number) {
    this.available = capacity;
  }

  async acquire(): Promise<() => void> {
    if (this.available > 0) {
      this.available -= 1;
      return () => this.release();
    }
    await new Promise<void>((resolve) => this.waiters.push(resolve));
    return () => this.release();
  }

  private release(): void {
    const waiter = this.waiters.shift();
    if (waiter) waiter();
    else this.available += 1;
  }
}

export interface BrowserSession {
  fetchPage(url: string): Promise<FetchedDocument>;
}

function envInteger(name: string, fallback: number, min: number, max: number): number {
  const value = Number(process.env[name] ?? fallback);
  return Number.isInteger(value) && value >= min && value <= max ? value : fallback;
}

function proxySettings(proxy: ProxyConfig): NonNullable<BrowserContextOptions["proxy"]> {
  const url = new URL(proxy.server);
  const username = proxy.username ?? (url.username ? decodeURIComponent(url.username) : undefined);
  const password = proxy.password ?? (url.password ? decodeURIComponent(url.password) : undefined);
  url.username = "";
  url.password = "";
  return {
    server: url.toString(),
    ...(username ? { username } : {}),
    ...(password ? { password } : {}),
    ...(proxy.bypass ? { bypass: proxy.bypass } : {}),
  };
}

function playwrightStorageState(state: StorageState): Exclude<BrowserContextOptions["storageState"], string | undefined> {
  return {
    cookies: (state.cookies ?? []).map((cookie) => ({
      ...cookie,
      expires: cookie.expires ?? -1,
      httpOnly: cookie.httpOnly ?? false,
      secure: cookie.secure ?? false,
      sameSite: cookie.sameSite ?? "Lax",
    })),
    origins: state.origins ?? [],
  };
}

function parseCookieHeader(cookieHeader: string, sourceUrl: string) {
  const cookieUrl = new URL("/", sourceUrl).toString();
  return cookieHeader
    .split(";")
    .map((part) => part.trim())
    .filter(Boolean)
    .flatMap((part) => {
      const separator = part.indexOf("=");
      if (separator < 1) return [];
      return [{ name: part.slice(0, separator).trim(), value: part.slice(separator + 1), url: cookieUrl }];
    });
}

function isSensitiveHeader(name: string): boolean {
  return /(authorization|cookie|credential|password|secret|signature|token|api[-_]?key|^key$)/i.test(name);
}

function browserOptions(request: GenerateRequest) {
  const mapping = (request.selectors ?? request.mapping ?? {}) as HTMLMapping;
  const delay = request.render_delay_ms ?? mapping.render_delay_ms ?? 0;
  return {
    waitUntil: request.wait_until ?? mapping.wait_until ?? "domcontentloaded",
    waitForSelector: request.wait_for_selector ?? mapping.wait_for_selector,
    renderDelayMs: typeof delay === "string" ? Number(delay) : delay,
  } as const;
}

export class BrowserPool {
  private readonly poolSize = envInteger("BROWSER_POOL_SIZE", 1, 1, 8);
  private readonly semaphore = new Semaphore(envInteger("BROWSER_MAX_CONTEXTS", 4, 1, 32));
  private readonly browsers: Array<Promise<Browser> | undefined> = [];
  private cursor = 0;

  private async browserAt(index: number): Promise<Browser> {
    let browser = this.browsers[index];
    if (!browser) {
      browser = chromium.launch({ headless: true }).catch((error: unknown) => {
        this.browsers[index] = undefined;
        throw new WorkerError(
          "BROWSER_UNAVAILABLE",
          "Chromium could not start. Install Playwright browsers or use the provided Docker image",
          503,
          { cause: error },
        );
      });
      this.browsers[index] = browser;
    }
    return browser;
  }

  async withSession<T>(
    sourceUrl: string,
    request: GenerateRequest,
    proxy: ProxyConfig | undefined,
    callback: (session: BrowserSession) => Promise<T>,
  ): Promise<T> {
    if ((request.source.method ?? "GET") !== "GET") {
      throw new WorkerError("BROWSER_POST_UNSUPPORTED", "Browser navigation only supports GET sources", 422);
    }
    await assertPublicHttpUrl(sourceUrl);
    await assertSafeProxy(proxy);
    const release = await this.semaphore.acquire();
    const browserIndex = this.cursor++ % this.poolSize;
    let context: BrowserContext | undefined;
    try {
      const browser = await this.browserAt(browserIndex);
      const allHeaders = validateSourceHeaders(request.headers ?? {});
      const sourceOrigin = new URL(sourceUrl).origin;
      const sensitiveHeaders = Object.fromEntries(
        Object.entries(allHeaders).filter(([name]) => isSensitiveHeader(name)),
      );
      const safeHeaders = Object.fromEntries(
        Object.entries(allHeaders).filter(([name]) => !isSensitiveHeader(name)),
      );
      context = await browser.newContext({
        ...(request.storage_state
          ? { storageState: playwrightStorageState(request.storage_state as StorageState) }
          : {}),
        ...(Object.keys(safeHeaders).length > 0 ? { extraHTTPHeaders: safeHeaders } : {}),
        ...(proxy ? { proxy: proxySettings(proxy) } : {}),
      });
      if (request.cookie) {
        await context.addCookies(parseCookieHeader(request.cookie, sourceUrl));
      }

      // Validate every browser request, including redirects and subresources, so a
      // public HTML page cannot use Chromium as a bridge to an internal service.
      await context.route("**/*", async (route) => {
        try {
          const requestUrl = new URL(route.request().url());
          if (!["http:", "https:"].includes(requestUrl.protocol)) {
            await route.continue();
            return;
          }
          await assertPublicHttpUrl(requestUrl.toString());
          if (requestUrl.origin === sourceOrigin && Object.keys(sensitiveHeaders).length > 0) {
            await route.continue({ headers: { ...route.request().headers(), ...sensitiveHeaders } });
          } else {
            await route.continue();
          }
        } catch {
          await route.abort("blockedbyclient");
        }
      });

      const timeoutMs = request.timeouts?.browser_ms ?? 30_000;
      const maxBytes = request.max_response_bytes ?? 5 * 1024 * 1024;
      const navigation = browserOptions(request);
      const session: BrowserSession = {
        fetchPage: async (url) => {
          await assertPublicHttpUrl(url);
          const page = await context!.newPage();
          page.setDefaultTimeout(timeoutMs);
          try {
            const response = await page.goto(url, {
              waitUntil: navigation.waitUntil,
              timeout: timeoutMs,
            });
            if (!response) {
              throw new WorkerError("BROWSER_NAVIGATION_FAILED", "Browser navigation returned no response", 502, {
                retryable: true,
              });
            }
            if (navigation.waitForSelector) {
              await page.waitForSelector(navigation.waitForSelector, { timeout: timeoutMs });
            }
            if (navigation.renderDelayMs > 0) {
              await page.waitForTimeout(navigation.renderDelayMs);
            }
            const html = await page.content();
            if (Buffer.byteLength(html) > maxBytes) {
              throw new WorkerError(
                "RESPONSE_TOO_LARGE",
                `Rendered response exceeds the ${maxBytes} byte limit`,
                502,
              );
            }
            const headers = await response.allHeaders();
            assertNoChallenge(response.status(), headers, html);
            if (!response.ok()) {
              throw new WorkerError(
                "UPSTREAM_HTTP_ERROR",
                `Browser source returned HTTP ${response.status()}`,
                502,
                { details: { upstream_status: response.status() } },
              );
            }
            return {
              body: html,
              finalUrl: page.url(),
              status: response.status(),
              contentType: headers["content-type"] ?? "text/html",
              usedBrowser: true,
            };
          } catch (error) {
            if (error instanceof WorkerError) throw error;
            throw new WorkerError("BROWSER_NAVIGATION_FAILED", "Browser navigation failed", 502, {
              retryable: true,
              cause: error,
            });
          } finally {
            await page.close().catch(() => undefined);
          }
        },
      };
      return await callback(session);
    } finally {
      await context?.close().catch(() => undefined);
      release();
    }
  }

  async close(): Promise<void> {
    const browsers = await Promise.allSettled(this.browsers.filter(Boolean) as Array<Promise<Browser>>);
    await Promise.all(
      browsers.flatMap((result) =>
        result.status === "fulfilled" ? [result.value.close().catch(() => undefined)] : [],
      ),
    );
    this.browsers.length = 0;
  }
}

export const browserPool = new BrowserPool();
