import { Hono } from "hono";
import { bodyLimit } from "hono/body-limit";
import { requestId } from "hono/request-id";
import { ZodError } from "zod";
import { readWorkerAuthState, type WorkerAuthState } from "./auth.js";
import { asWorkerError, WorkerError } from "./errors.js";
import { generateFeed } from "./generator.js";
import { generateRequestSchema } from "./schema.js";
import { redactSecrets, validBearerToken } from "./security.js";
import type { GenerateRequest, GenerateResult } from "./types.js";

export interface AppDependencies {
  generate?: (request: GenerateRequest) => Promise<GenerateResult>;
  auth?: () => WorkerAuthState;
}

export function createApp(dependencies: AppDependencies = {}) {
  const app = new Hono();
  const generate = dependencies.generate ?? generateFeed;
  const readAuth = dependencies.auth ?? readWorkerAuthState;
  app.use("*", requestId());
  app.use(
    "/v1/*",
    bodyLimit({
      maxSize: 2 * 1024 * 1024,
      onError: (context) =>
        context.json(
          { error: { code: "REQUEST_TOO_LARGE", message: "Request body exceeds 2 MiB" } },
          413,
        ),
    }),
  );
  app.use("/v1/*", async (context, next) => {
    const auth = readAuth();
    if (!auth.token) {
      return context.json(
        {
          error: {
            code: "AUTH_NOT_CONFIGURED",
            message: auth.error ?? "Worker token is not configured",
          },
        },
        503,
      );
    }

    if (!validBearerToken(context.req.header("authorization"), auth.token)) {
      return context.json({ error: { code: "UNAUTHORIZED", message: "Invalid worker token" } }, 401);
    }
    return next();
  });

  app.get("/health", (context) => {
    const auth = readAuth();
    return context.json({
      status: auth.token ? "ok" : "unavailable",
      service: "rss-generator-worker",
      version: "0.1.0",
      auth_configured: Boolean(auth.token),
      ...(auth.error ? { error: auth.error } : {}),
    });
  });

  app.post("/v1/generate", async (context) => {
    let raw: unknown;
    try {
      raw = await context.req.json();
    } catch {
      throw new WorkerError("INVALID_JSON_BODY", "Request body must be valid JSON", 400);
    }
    const request = generateRequestSchema.parse(raw) as GenerateRequest;
    return context.json(await generate(request));
  });

  app.notFound((context) =>
    context.json({ error: { code: "NOT_FOUND", message: "Route not found" } }, 404),
  );
  app.onError((error, context) => {
    if (error instanceof ZodError) {
      return context.json(
        {
          error: {
            code: "VALIDATION_FAILED",
            message: "Request validation failed",
            details: error.issues.map((issue) => ({
              path: issue.path.join("."),
              message: issue.message,
            })),
          },
        },
        422,
      );
    }
    const workerError = asWorkerError(error);
    console.error(
      JSON.stringify({
        level: "error",
        request_id: context.get("requestId"),
        code: workerError.code,
        message: workerError.message,
        details: redactSecrets(workerError.details),
      }),
    );
    return context.json(
      {
        error: {
          code: workerError.code,
          message: workerError.message,
          ...(workerError.details ? { details: redactSecrets(workerError.details) } : {}),
        },
      },
      workerError.status as 400,
    );
  });
  return app;
}

export const app = createApp();
