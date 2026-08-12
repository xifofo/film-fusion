import { z } from "zod";

const fieldExpression = z.string().trim().min(1).max(2_000);

const enclosureMapping = z
  .object({
    url: fieldExpression,
    type: fieldExpression.optional(),
    length: fieldExpression.optional(),
    title: fieldExpression.optional(),
  })
  .strict();

const itemFields = {
  title: fieldExpression,
  link: fieldExpression.optional(),
  description: fieldExpression.optional(),
  content: fieldExpression.optional(),
  date: fieldExpression.optional(),
  author: fieldExpression.optional(),
  category: fieldExpression.optional(),
  categories: fieldExpression.optional(),
  guid: fieldExpression.optional(),
  enclosure: z.union([fieldExpression, enclosureMapping]).optional(),
  enclosures: z.union([fieldExpression, enclosureMapping]).optional(),
  enclosure_url: fieldExpression.optional(),
  enclosure_type: fieldExpression.optional(),
  enclosure_length: fieldExpression.optional(),
  enclosure_title: fieldExpression.optional(),
};

const fieldsSchema = z.object(itemFields).strict();

const mappingBase = {
  item: fieldExpression.optional(),
  items: fieldExpression.optional(),
  ...Object.fromEntries(
    Object.entries(itemFields).map(([key, value]) => [key, value.optional()]),
  ),
  fields: fieldsSchema.optional(),
};

export const htmlMappingSchema = z
  .object({
    ...mappingBase,
    list: fieldExpression.optional(),
    detail_link: fieldExpression.optional(),
    detail_content: fieldExpression.optional(),
    detail_concurrency: z.number().int().min(1).max(10).optional(),
    wait_until: z.enum(["load", "domcontentloaded", "networkidle", "commit"]).optional(),
    wait_for_selector: z.string().trim().min(1).max(2_000).optional(),
    render_delay_ms: z.union([
      z.number().int().min(0).max(30_000),
      z.string().regex(/^\d{1,5}$/),
    ]).optional(),
  })
  .strict();

export const jsonMappingSchema = z.object(mappingBase).strict();

const proxySchema = z.union([
  z.string().trim().min(1).max(2_048),
  z
    .object({
      server: z.string().trim().min(1).max(2_048),
      username: z.string().max(1_024).optional(),
      password: z.string().max(4_096).optional(),
      bypass: z.string().max(4_096).optional(),
      allow_private: z.boolean().optional(),
    })
    .strict(),
]);

const storageStateSchema = z
  .object({
    cookies: z
      .array(
        z
          .object({
            name: z.string().min(1).max(1_024),
            value: z.string().max(16_384),
            domain: z.string().min(1).max(1_024),
            path: z.string().min(1).max(2_048),
            expires: z.number().optional(),
            httpOnly: z.boolean().optional(),
            secure: z.boolean().optional(),
            sameSite: z.enum(["Strict", "Lax", "None"]).optional(),
          })
          .strict(),
      )
      .max(500)
      .optional(),
    origins: z
      .array(
        z
          .object({
            origin: z.url().max(2_048),
            localStorage: z
              .array(
                z
                  .object({
                    name: z.string().min(1).max(1_024),
                    value: z.string().max(131_072),
                  })
                  .strict(),
              )
              .max(1_000),
          })
          .strict(),
      )
      .max(100)
      .optional(),
  })
  .strict();

export const generateRequestSchema = z
  .object({
    feed: z
      .object({
        title: z.string().trim().min(1).max(1_024),
        link: z.url().max(4_096).optional(),
        description: z.string().max(16_384).optional(),
        language: z.string().trim().max(128).optional(),
        author: z.string().trim().max(1_024).optional(),
        image: z.url().max(4_096).optional(),
        updated_at: z.string().trim().max(256).optional(),
      })
      .strict(),
    kind: z.enum(["http_json", "http_html", "browser"]),
    source: z
      .object({
        url_template: z.string().trim().min(1).max(8_192),
        method: z.enum(["GET", "POST"]).optional(),
        body_template: z.string().max(1024 * 1024).optional(),
      })
      .strict(),
    params: z.record(z.string().max(128), z.union([z.string(), z.number(), z.boolean()])).optional(),
    headers: z.record(z.string().max(256), z.string().max(32_768)).optional(),
    cookie: z.string().max(131_072).optional(),
    proxy: proxySchema.optional(),
    selectors: htmlMappingSchema.optional(),
    mapping: z.union([htmlMappingSchema, jsonMappingSchema]).optional(),
    browser_fallback: z.boolean().optional(),
    storage_state: storageStateSchema.optional(),
    wait_until: z.enum(["load", "domcontentloaded", "networkidle", "commit"]).optional(),
    wait_for_selector: z.string().trim().min(1).max(2_000).optional(),
    render_delay_ms: z.number().int().min(0).max(30_000).optional(),
    timeouts: z
      .object({
        request_ms: z.number().int().min(100).max(120_000).optional(),
        browser_ms: z.number().int().min(100).max(180_000).optional(),
      })
      .strict()
      .optional(),
    retries: z.number().int().min(0).max(5).optional(),
    max_response_bytes: z.number().int().min(1_024).max(50 * 1024 * 1024).optional(),
    max_redirects: z.number().int().min(0).max(10).optional(),
    max_items: z.number().int().min(1).max(500).optional(),
  })
  .strict()
  .superRefine((value, context) => {
    if ((value.source.method ?? "GET") === "GET" && value.source.body_template !== undefined) {
      context.addIssue({
        code: "custom",
        path: ["source", "body_template"],
        message: "body_template is only valid for POST sources",
      });
    }
    if (value.kind === "browser" && (value.source.method ?? "GET") !== "GET") {
      context.addIssue({
        code: "custom",
        path: ["source", "method"],
        message: "browser sources only support GET navigation",
      });
    }
    if (value.browser_fallback && (value.source.method ?? "GET") !== "GET") {
      context.addIssue({
        code: "custom",
        path: ["browser_fallback"],
        message: "browser_fallback is only available for GET sources",
      });
    }
    if (value.kind === "http_json" && !value.mapping) {
      context.addIssue({
        code: "custom",
        path: ["mapping"],
        message: "mapping is required for http_json",
      });
    }
    if (value.kind !== "http_json" && !(value.selectors ?? value.mapping)) {
      context.addIssue({
        code: "custom",
        path: ["selectors"],
        message: "selectors or mapping is required for HTML/browser extraction",
      });
    }
  });

export type ParsedGenerateRequest = z.infer<typeof generateRequestSchema>;
