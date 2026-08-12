export class WorkerError extends Error {
  readonly code: string;
  readonly status: number;
  readonly retryable: boolean;
  readonly details: Record<string, unknown> | undefined;

  constructor(
    code: string,
    message: string,
    status = 500,
    options: { retryable?: boolean; details?: Record<string, unknown>; cause?: unknown } = {},
  ) {
    super(message, { cause: options.cause });
    this.name = "WorkerError";
    this.code = code;
    this.status = status;
    this.retryable = options.retryable ?? false;
    this.details = options.details;
  }
}

export class ChallengeError extends WorkerError {
  constructor(message: string, details?: Record<string, unknown>) {
    super("ANTI_BOT_CHALLENGE", message, 502, {
      retryable: true,
      ...(details ? { details } : {}),
    });
    this.name = "ChallengeError";
  }
}

export function asWorkerError(error: unknown): WorkerError {
  if (error instanceof WorkerError) return error;
  if (error instanceof Error && error.name === "AbortError") {
    return new WorkerError("FETCH_TIMEOUT", "Source request timed out", 504, {
      retryable: true,
      cause: error,
    });
  }
  return new WorkerError("GENERATION_FAILED", "Feed generation failed", 502, {
    cause: error,
  });
}
