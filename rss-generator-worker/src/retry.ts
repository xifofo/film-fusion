import { asWorkerError } from "./errors.js";

export interface RetryOptions {
  retries: number;
  baseDelayMs?: number;
  maxDelayMs?: number;
  sleep?: (milliseconds: number) => Promise<void>;
  random?: () => number;
}

export async function withRetry<T>(operation: () => Promise<T>, options: RetryOptions): Promise<T> {
  const sleep = options.sleep ?? ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)));
  const random = options.random ?? Math.random;
  const baseDelay = options.baseDelayMs ?? 250;
  const maxDelay = options.maxDelayMs ?? 5_000;
  let attempt = 0;

  while (true) {
    try {
      return await operation();
    } catch (error) {
      const workerError = asWorkerError(error);
      if (!workerError.retryable || attempt >= options.retries) throw workerError;
      const exponential = Math.min(maxDelay, baseDelay * 2 ** attempt);
      const jittered = Math.round(exponential * (0.75 + random() * 0.5));
      attempt += 1;
      await sleep(jittered);
    }
  }
}
