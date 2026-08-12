import { WorkerError } from "./errors.js";

const PLACEHOLDER = /\{\{params\.([A-Za-z_][A-Za-z0-9_-]*)\}\}/g;

export function renderParameterTemplate(
  template: string,
  params: Record<string, string | number | boolean> = {},
): string {
  const missing = new Set<string>();
  const rendered = template.replace(PLACEHOLDER, (_match, name: string) => {
    const value = params[name];
    if (value === undefined) {
      missing.add(name);
      return "";
    }
    return encodeURIComponent(String(value));
  });

  if (missing.size > 0) {
    throw new WorkerError(
      "MISSING_URL_PARAMETER",
      `Missing template parameter${missing.size > 1 ? "s" : ""}: ${[...missing].join(", ")}`,
      422,
      { details: { parameters: [...missing] } },
    );
  }
  if (/\{\{|\}\}/.test(rendered)) {
    throw new WorkerError(
      "INVALID_PARAMETER_TEMPLATE",
      "Only {{params.name}} placeholders are supported",
      422,
    );
  }
  return rendered;
}

export function renderBodyTemplate(
  template: string,
  params: Record<string, string | number | boolean> = {},
): string {
  const BODY_PLACEHOLDER = /\{\{(?:(json)\.)?params\.([A-Za-z_][A-Za-z0-9_-]*)\}\}/g;
  if (/\{\{|\}\}/.test(template.replace(BODY_PLACEHOLDER, ""))) {
    throw new WorkerError(
      "INVALID_PARAMETER_TEMPLATE",
      "Only {{params.name}} and {{json.params.name}} body placeholders are supported",
      422,
    );
  }
  const missing = new Set<string>();
  const rendered = template.replace(
    BODY_PLACEHOLDER,
    (_match, jsonMode: string | undefined, name: string) => {
      const value = params[name];
      if (value === undefined) {
        missing.add(name);
        return jsonMode ? "null" : "";
      }
      return jsonMode ? JSON.stringify(value) : encodeURIComponent(String(value));
    },
  );
  if (missing.size > 0) {
    throw new WorkerError(
      "MISSING_BODY_PARAMETER",
      `Missing body template parameter${missing.size > 1 ? "s" : ""}: ${[...missing].join(", ")}`,
      422,
      { details: { parameters: [...missing] } },
    );
  }
  return rendered;
}

export function renderUrlTemplate(
  template: string,
  params: Record<string, string | number | boolean> = {},
): string {
  const rendered = renderParameterTemplate(template, params);

  try {
    const url = new URL(rendered);
    if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error("unsupported protocol");
    if (url.username || url.password) throw new Error("URL credentials are not allowed");
    return url.toString();
  } catch (error) {
    throw new WorkerError("INVALID_SOURCE_URL", "Rendered source URL is invalid", 422, {
      cause: error,
    });
  }
}
