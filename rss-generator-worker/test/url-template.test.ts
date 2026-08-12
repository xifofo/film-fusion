import { describe, expect, it } from "vitest";
import { WorkerError } from "../src/errors.js";
import { renderBodyTemplate, renderUrlTemplate } from "../src/url-template.js";

describe("URL and body templates", () => {
  it("encodes each URL placeholder as one component", () => {
    expect(
      renderUrlTemplate("https://example.com/search/{{params.term}}?page={{params.page}}", {
        term: "中文 /?&",
        page: 2,
      }),
    ).toBe("https://example.com/search/%E4%B8%AD%E6%96%87%20%2F%3F%26?page=2");
  });

  it("rejects missing and unsupported placeholders", () => {
    expect(() => renderUrlTemplate("https://example.com/{{params.id}}", {})).toThrowError(
      expect.objectContaining<Partial<WorkerError>>({ code: "MISSING_URL_PARAMETER", status: 422 }),
    );
    expect(() => renderUrlTemplate("https://example.com/{{headers.authorization}}", {})).toThrowError(
      expect.objectContaining<Partial<WorkerError>>({ code: "INVALID_PARAMETER_TEMPLATE" }),
    );
  });

  it("supports form-encoded and JSON-safe body placeholders", () => {
    const params = { query: 'A & "中文"', active: true };
    expect(renderBodyTemplate("query={{params.query}}", params)).toBe(
      "query=A%20%26%20%22%E4%B8%AD%E6%96%87%22",
    );
    expect(renderBodyTemplate('{"query":{{json.params.query}},"active":{{json.params.active}}}', params)).toBe(
      '{"query":"A & \\"中文\\"","active":true}',
    );
  });

  it("does not interpret placeholder-looking text injected through JSON values", () => {
    expect(
      renderBodyTemplate('{"value":{{json.params.value}}}', {
        value: "{{params.other}}",
        other: "must-not-expand",
      }),
    ).toBe('{"value":"{{params.other}}"}');
  });
});
