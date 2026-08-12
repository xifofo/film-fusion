export type FeedKind = "http_json" | "http_html" | "browser";

export interface FeedMetadata {
  title: string;
  link?: string;
  description?: string;
  language?: string;
  author?: string;
  image?: string;
  updated_at?: string;
}

export interface FeedEnclosure {
  url: string;
  type?: string;
  length?: number;
  title?: string;
}

export interface FeedItem {
  title: string;
  link?: string;
  description?: string;
  content?: string;
  date?: string;
  author?: string;
  categories?: string[];
  guid?: string;
  enclosures?: FeedEnclosure[];
}

export interface GeneratedFeed {
  title: string;
  link: string;
  description?: string;
  language?: string;
  author?: string;
  image?: string;
  updated_at?: string;
  items: FeedItem[];
}

export interface GenerateResult {
  feed: GeneratedFeed;
  meta: {
    kind_requested: FeedKind;
    kind_used: FeedKind;
    source_url: string;
    final_url: string;
    fetched_at: string;
    duration_ms: number;
    item_count: number;
    browser_fallback_used: boolean;
  };
}

export type FieldExpression = string;

export interface EnclosureMapping {
  url: FieldExpression;
  type?: FieldExpression;
  length?: FieldExpression;
  title?: FieldExpression;
}

export interface ItemFieldMapping {
  title: FieldExpression;
  link?: FieldExpression;
  description?: FieldExpression;
  content?: FieldExpression;
  date?: FieldExpression;
  author?: FieldExpression;
  category?: FieldExpression;
  categories?: FieldExpression;
  guid?: FieldExpression;
  enclosure?: FieldExpression | EnclosureMapping;
  enclosures?: FieldExpression | EnclosureMapping;
  enclosure_url?: FieldExpression;
  enclosure_type?: FieldExpression;
  enclosure_length?: FieldExpression;
  enclosure_title?: FieldExpression;
}

export interface HTMLMapping extends Partial<ItemFieldMapping> {
  item?: string;
  items?: string;
  list?: string;
  fields?: ItemFieldMapping;
  detail_link?: FieldExpression;
  detail_content?: FieldExpression;
  detail_concurrency?: number;
  wait_until?: "load" | "domcontentloaded" | "networkidle" | "commit";
  wait_for_selector?: string;
  render_delay_ms?: number | string;
}

export interface JSONMapping extends Partial<ItemFieldMapping> {
  item?: string;
  items?: string;
  fields?: ItemFieldMapping;
}

export interface ProxyConfig {
  server: string;
  username?: string;
  password?: string;
  bypass?: string;
  allow_private?: boolean;
}

export interface StorageState {
  cookies?: Array<{
    name: string;
    value: string;
    domain: string;
    path: string;
    expires?: number;
    httpOnly?: boolean;
    secure?: boolean;
    sameSite?: "Strict" | "Lax" | "None";
  }>;
  origins?: Array<{
    origin: string;
    localStorage: Array<{ name: string; value: string }>;
  }>;
}

export interface GenerateRequest {
  feed: FeedMetadata;
  kind: FeedKind;
  source: {
    url_template: string;
    method?: "GET" | "POST";
    body_template?: string;
  };
  params?: Record<string, string | number | boolean>;
  headers?: Record<string, string>;
  cookie?: string;
  proxy?: string | ProxyConfig;
  selectors?: HTMLMapping;
  mapping?: JSONMapping | HTMLMapping;
  browser_fallback?: boolean;
  storage_state?: StorageState;
  wait_until?: "load" | "domcontentloaded" | "networkidle" | "commit";
  wait_for_selector?: string;
  render_delay_ms?: number;
  timeouts?: {
    request_ms?: number;
    browser_ms?: number;
  };
  retries?: number;
  max_response_bytes?: number;
  max_redirects?: number;
  max_items?: number;
}

export interface FetchedDocument {
  body: string;
  finalUrl: string;
  status: number;
  contentType: string;
  usedBrowser: boolean;
}

export interface DetailFetcher {
  (url: string): Promise<{ body: string; finalUrl: string }>;
}
