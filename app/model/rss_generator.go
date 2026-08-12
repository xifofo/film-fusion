package model

import "time"

const (
	RSSGeneratorKindHTTPJSON = "http_json"
	RSSGeneratorKindHTTPHTML = "http_html"
	RSSGeneratorKindBrowser  = "browser"
)

// RSSGeneratorFeedDefinition is a saved, declarative source definition. Secret
// request state is encrypted before it reaches these columns; API responses are
// built from a separate redacted view and never serialize ciphertext.
type RSSGeneratorFeedDefinition struct {
	ID                     uint      `gorm:"primarykey" json:"id"`
	PublicID               string    `gorm:"size:32;not null;uniqueIndex" json:"public_id"`
	Name                   string    `gorm:"size:160;not null" json:"name"`
	Slug                   string    `gorm:"size:120;not null;uniqueIndex" json:"slug"`
	Description            string    `gorm:"type:text" json:"description,omitempty"`
	HomePageURL            string    `gorm:"type:text" json:"home_page_url,omitempty"`
	Language               string    `gorm:"size:64" json:"language,omitempty"`
	Author                 string    `gorm:"size:255" json:"author,omitempty"`
	ImageURL               string    `gorm:"type:text" json:"image_url,omitempty"`
	RouteKind              string    `gorm:"size:24;not null;index" json:"route_kind"`
	SourceURLTemplate      string    `gorm:"type:text;not null" json:"source_url_template"`
	Method                 string    `gorm:"size:8;not null;default:GET" json:"method"`
	RequestBodyCiphertext  string    `gorm:"type:text" json:"-"`
	HeadersCiphertext      string    `gorm:"type:text" json:"-"`
	CookieCiphertext       string    `gorm:"type:text" json:"-"`
	ProxyURLCiphertext     string    `gorm:"type:text" json:"-"`
	ProxyAllowPrivate      bool      `gorm:"not null;default:false" json:"proxy_allow_private"`
	SecretQueryCiphertext  string    `gorm:"type:text" json:"-"`
	BrowserStateCiphertext string    `gorm:"type:text" json:"-"`
	ParametersJSON         string    `gorm:"type:text;not null" json:"-"`
	SelectorsJSON          string    `gorm:"type:text;not null" json:"-"`
	MappingJSON            string    `gorm:"type:text;not null" json:"-"`
	WaitUntil              string    `gorm:"size:32" json:"wait_until,omitempty"`
	WaitForSelector        string    `gorm:"type:text" json:"wait_for_selector,omitempty"`
	RenderDelayMS          int       `gorm:"not null;default:0" json:"render_delay_ms"`
	ItemLimit              int       `gorm:"not null;default:100" json:"item_limit"`
	BrowserFallback        bool      `gorm:"not null;default:false" json:"browser_fallback"`
	Enabled                bool      `gorm:"not null;default:true;index" json:"enabled"`
	CacheTTLSeconds        int       `gorm:"not null;default:300" json:"cache_ttl_seconds"`
	StaleTTLSeconds        int       `gorm:"not null;default:3600" json:"stale_ttl_seconds"`
	Version                int       `gorm:"not null;default:1" json:"version"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

func (RSSGeneratorFeedDefinition) TableName() string { return "rss_generator_feed_definitions" }

// RSSGeneratorFeedAccessToken stores only a one-way hash. The clear token is
// returned once by create/rotate operations and cannot be recovered later.
type RSSGeneratorFeedAccessToken struct {
	ID                 uint       `gorm:"primarykey" json:"id"`
	FeedID             uint       `gorm:"not null;index" json:"feed_id"`
	Name               string     `gorm:"size:120;not null" json:"name"`
	TokenHash          string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	Prefix             string     `gorm:"size:20;not null;index" json:"prefix"`
	RateLimitPerMinute int        `gorm:"not null;default:60" json:"rate_limit_per_minute"`
	ExpiresAt          *time.Time `gorm:"index" json:"expires_at,omitempty"`
	RevokedAt          *time.Time `gorm:"index" json:"revoked_at,omitempty"`
	LastUsedAt         *time.Time `json:"last_used_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (RSSGeneratorFeedAccessToken) TableName() string { return "rss_generator_feed_access_tokens" }

// RSSGeneratorFeedCache is a durable rendered cache. CacheKey includes the
// feed version, canonical parameters, output format and token auth context.
type RSSGeneratorFeedCache struct {
	ID               uint      `gorm:"primarykey" json:"id"`
	FeedID           uint      `gorm:"not null;index" json:"feed_id"`
	CacheKey         string    `gorm:"size:64;not null;uniqueIndex" json:"-"`
	Format           string    `gorm:"size:12;not null" json:"format"`
	AuthContext      string    `gorm:"size:96;not null" json:"-"`
	NormalizedParams string    `gorm:"type:text;not null" json:"-"`
	ContentType      string    `gorm:"size:128;not null" json:"content_type"`
	Body             []byte    `gorm:"type:blob;not null" json:"-"`
	ETag             string    `gorm:"column:etag;size:80;not null" json:"etag"`
	LastModified     time.Time `gorm:"not null" json:"last_modified"`
	GeneratedAt      time.Time `gorm:"not null" json:"generated_at"`
	ExpiresAt        time.Time `gorm:"not null;index" json:"expires_at"`
	StaleAt          time.Time `gorm:"not null;index" json:"stale_at"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (RSSGeneratorFeedCache) TableName() string { return "rss_generator_feed_caches" }
