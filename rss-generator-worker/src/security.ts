import { timingSafeEqual } from "node:crypto";
import { lookup } from "node:dns/promises";
import ipaddr from "ipaddr.js";
import { WorkerError } from "./errors.js";
import type { ProxyConfig } from "./types.js";

const FORBIDDEN_HEADERS = new Set([
  "connection",
  "content-length",
  "expect",
  "forwarded",
  "host",
  "proxy-authorization",
  "proxy-connection",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
  "via",
  "x-forwarded-for",
  "x-forwarded-host",
  "x-forwarded-proto",
]);

const SECRET_KEY = /(authorization|cookie|password|secret|token|api[-_]?key|storage_state)/i;
const SECRET_QUERY = /^(access_token|api_key|apikey|auth|key|password|secret|signature|token)$/i;

function normalizeAddress(address: string): string {
  const withoutZone = address.replace(/%.+$/, "");
  return withoutZone.startsWith("::ffff:") ? withoutZone.slice(7) : withoutZone;
}

export function isPrivateAddress(address: string): boolean {
  try {
    const parsed = ipaddr.parse(normalizeAddress(address));
    const range = parsed.range();
    return !["unicast"].includes(range);
  } catch {
    return false;
  }
}

export async function assertPublicHttpUrl(rawUrl: string): Promise<URL> {
  let url: URL;
  try {
    url = new URL(rawUrl);
  } catch (error) {
    throw new WorkerError("INVALID_SOURCE_URL", "Source URL is invalid", 422, { cause: error });
  }

  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) {
    throw new WorkerError(
      "UNSAFE_SOURCE_URL",
      "Source URL must be HTTP(S) and must not contain credentials",
      422,
    );
  }

  const hostname = url.hostname.replace(/^\[|\]$/g, "");
  if (hostname.toLowerCase() === "localhost" || hostname.toLowerCase().endsWith(".localhost")) {
    throw new WorkerError("SSRF_BLOCKED", "Loopback and private source addresses are not allowed", 403);
  }

  let addresses: Array<{ address: string }>;
  try {
    addresses = await lookup(hostname, { all: true, verbatim: true });
  } catch (error) {
    throw new WorkerError("DNS_RESOLUTION_FAILED", "Could not resolve source hostname", 502, {
      retryable: true,
      cause: error,
    });
  }
  if (addresses.length === 0 || addresses.some(({ address }) => isPrivateAddress(address))) {
    throw new WorkerError("SSRF_BLOCKED", "Loopback and private source addresses are not allowed", 403);
  }
  return url;
}

export function validateSourceHeaders(headers: Record<string, string> = {}): Record<string, string> {
  const normalized: Record<string, string> = {};
  for (const [name, value] of Object.entries(headers)) {
    const lower = name.trim().toLowerCase();
    if (!/^[!#$%&'*+.^_`|~0-9a-z-]+$/.test(lower) || FORBIDDEN_HEADERS.has(lower)) {
      throw new WorkerError("FORBIDDEN_SOURCE_HEADER", `Source header is not allowed: ${name}`, 422);
    }
    if (/[\r\n]/.test(value)) {
      throw new WorkerError("INVALID_SOURCE_HEADER", `Source header contains a newline: ${name}`, 422);
    }
    normalized[lower] = value;
  }
  return normalized;
}

export function normalizeProxy(proxy?: string | ProxyConfig): ProxyConfig | undefined {
  if (!proxy) return undefined;
  const normalized = typeof proxy === "string" ? { server: proxy } : { ...proxy };
  let url: URL;
  try {
    url = new URL(normalized.server);
  } catch (error) {
    throw new WorkerError("INVALID_PROXY", "Proxy URL is invalid", 422, { cause: error });
  }
  if (!["http:", "https:", "socks:", "socks5:", "socks5h:"].includes(url.protocol)) {
    throw new WorkerError("INVALID_PROXY", "Proxy protocol must be HTTP, HTTPS, SOCKS or SOCKS5", 422);
  }
  if (normalized.username !== undefined) url.username = normalized.username;
  if (normalized.password !== undefined) url.password = normalized.password;
  normalized.server = url.toString();
  return normalized;
}

export async function assertSafeProxy(proxy?: ProxyConfig): Promise<void> {
  if (!proxy || proxy.allow_private) return;
  const url = new URL(proxy.server);
  const hostname = url.hostname.replace(/^\[|\]$/g, "");
  const addresses = await lookup(hostname, { all: true, verbatim: true }).catch((error: unknown) => {
    throw new WorkerError("PROXY_DNS_FAILED", "Could not resolve proxy hostname", 502, {
      retryable: true,
      cause: error,
    });
  });
  if (addresses.length === 0 || addresses.some(({ address }) => isPrivateAddress(address))) {
    throw new WorkerError(
      "PRIVATE_PROXY_BLOCKED",
      "Private proxy addresses require proxy.allow_private=true",
      403,
    );
  }
}

export function redactSecrets(value: unknown, key = ""): unknown {
  if (SECRET_KEY.test(key)) return "[REDACTED]";
  if (typeof value === "string") {
    const bearerRedacted = value.replace(/Bearer\s+[^\s]+/gi, "Bearer [REDACTED]");
    try {
      const url = new URL(bearerRedacted);
      url.username = url.username ? "[REDACTED]" : "";
      url.password = url.password ? "[REDACTED]" : "";
      for (const queryKey of url.searchParams.keys()) {
        if (SECRET_QUERY.test(queryKey)) url.searchParams.set(queryKey, "[REDACTED]");
      }
      return url.toString();
    } catch {
      return bearerRedacted;
    }
  }
  if (Array.isArray(value)) return value.map((item) => redactSecrets(item));
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).map(([entryKey, entryValue]) => [
        entryKey,
        redactSecrets(entryValue, entryKey),
      ]),
    );
  }
  return value;
}

export function redactUrlForMetadata(rawUrl: string): string {
  try {
    const url = new URL(rawUrl);
    url.username = "";
    url.password = "";
    url.search = "";
    url.hash = "";
    return url.toString();
  } catch {
    return "[REDACTED]";
  }
}

export function validBearerToken(header: string | undefined, expected: string): boolean {
  if (!header?.startsWith("Bearer ")) return false;
  const supplied = Buffer.from(header.slice(7));
  const wanted = Buffer.from(expected);
  return supplied.length === wanted.length && timingSafeEqual(supplied, wanted);
}
