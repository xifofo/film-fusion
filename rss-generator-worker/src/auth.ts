import { readFileSync } from "node:fs";

export interface WorkerAuthState {
  token: string;
  error?: string;
}

// Compose deployments provide the shared secret directly. The file-based form
// remains available for existing installations and is reread for every request.
export function readWorkerAuthState(): WorkerAuthState {
  const environmentToken = process.env.WORKER_AUTH_TOKEN;
  if (environmentToken !== undefined && environmentToken.trim() !== "") {
    if (/[\r\n]/.test(environmentToken)) {
      return { token: "", error: "Worker Token 格式无效" };
    }
    return { token: environmentToken.trim() };
  }

  const path = process.env.WORKER_AUTH_TOKEN_FILE?.trim();
  if (!path) {
    return { token: "", error: "Worker Token 未配置" };
  }

  let raw: string;
  try {
    raw = readFileSync(path, "utf8");
  } catch {
    return { token: "", error: "Worker Token 文件不可读" };
  }

  const token = raw.trim();
  if (!token) {
    return { token: "", error: "Worker Token 未配置" };
  }
  if (/[\r\n]/.test(token)) {
    return { token: "", error: "Worker Token 格式无效" };
  }
  return { token };
}
