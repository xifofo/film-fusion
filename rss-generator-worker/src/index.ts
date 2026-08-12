import { serve } from "@hono/node-server";
import { app } from "./app.js";
import { browserPool } from "./browser-pool.js";

const port = Number(process.env.PORT ?? 8787);
const hostname = process.env.HOST ?? "127.0.0.1";

const server = serve({ fetch: app.fetch, port, hostname }, (info) => {
  console.log(JSON.stringify({ level: "info", message: "RSS generator worker listening", ...info }));
});

async function shutdown(signal: string) {
  console.log(JSON.stringify({ level: "info", message: "Shutting down", signal }));
  await browserPool.close();
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(1), 10_000).unref();
}

process.once("SIGINT", () => void shutdown("SIGINT"));
process.once("SIGTERM", () => void shutdown("SIGTERM"));
