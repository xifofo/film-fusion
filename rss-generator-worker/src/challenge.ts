import { ChallengeError } from "./errors.js";

const CHALLENGE_PATTERNS = [
  /captcha/i,
  /cf-chl-/i,
  /challenge-platform/i,
  /cloudflare ray id/i,
  /checking (?:your )?browser/i,
  /enable javascript and cookies/i,
  /just a moment/i,
  /please verify (?:that )?you are (?:a )?human/i,
  /访问验证|安全验证|人机验证|滑动验证|验证码/,
];

export function detectChallenge(
  status: number,
  headers: Headers | Record<string, string>,
  body: string,
): string | undefined {
  const getHeader = (name: string): string | undefined => {
    if ("get" in headers && typeof headers.get === "function") return headers.get(name) ?? undefined;
    const entry = Object.entries(headers).find(([key]) => key.toLowerCase() === name.toLowerCase());
    return entry?.[1];
  };
  if (getHeader("cf-mitigated")?.toLowerCase() === "challenge") return "cf-mitigated header";
  if (status === 429) return "HTTP 429 rate-limit/challenge response";
  if ([401, 403, 503].includes(status)) {
    const pattern = CHALLENGE_PATTERNS.find((candidate) => candidate.test(body.slice(0, 512_000)));
    if (pattern) return `HTTP ${status} challenge page`;
  }
  if (CHALLENGE_PATTERNS.some((pattern) => pattern.test(body.slice(0, 128_000)))) {
    return "challenge markers in response body";
  }
  return undefined;
}

export function assertNoChallenge(
  status: number,
  headers: Headers | Record<string, string>,
  body: string,
): void {
  const reason = detectChallenge(status, headers, body);
  if (reason) {
    throw new ChallengeError(
      "The source returned an anti-bot challenge; manual login or a permitted session may be required",
      { reason, upstream_status: status },
    );
  }
}
