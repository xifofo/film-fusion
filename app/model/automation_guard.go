package model

import "time"

const (
	AutomationDedupReserved  = "reserved"
	AutomationDedupCommitted = "committed"
)

// AutomationDedupRecord is the durable reservation/commit ledger used by the
// automation deduplicate node. The composite identity keeps claims isolated by
// source/workflow/global scope without persisting the potentially sensitive
// raw deduplication key.
type AutomationDedupRecord struct {
	ID          uint       `gorm:"primarykey" json:"id"`
	ScopeType   string     `gorm:"size:16;not null;uniqueIndex:idx_automation_dedup_identity,priority:1" json:"scope"`
	ScopeID     uint       `gorm:"not null;uniqueIndex:idx_automation_dedup_identity,priority:2" json:"scope_id"`
	Namespace   string     `gorm:"size:80;not null;uniqueIndex:idx_automation_dedup_identity,priority:3" json:"namespace"`
	KeyHash     string     `gorm:"size:64;not null;uniqueIndex:idx_automation_dedup_identity,priority:4" json:"key_hash"`
	State       string     `gorm:"size:16;not null;index" json:"state"`
	OwnerRunID  uint       `gorm:"not null;index" json:"owner_run_id"`
	OwnerNodeID string     `gorm:"size:80;not null" json:"owner_node_id"`
	TTLSeconds  int64      `gorm:"not null" json:"ttl_seconds"`
	FirstSeenAt time.Time  `gorm:"not null;index" json:"first_seen_at"`
	LastSeenAt  time.Time  `gorm:"not null;index" json:"last_seen_at"`
	ExpiresAt   *time.Time `gorm:"index" json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (AutomationDedupRecord) TableName() string { return "automation_dedup_records" }

// AutomationRateLimitBucket stores one fixed UTC window counter. A separate
// claim table makes token consumption idempotent for one run/node even when a
// worker is retried after the counter was incremented.
type AutomationRateLimitBucket struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	ScopeType     string    `gorm:"size:16;not null;uniqueIndex:idx_automation_rate_bucket_identity,priority:1" json:"scope"`
	ScopeID       uint      `gorm:"not null;uniqueIndex:idx_automation_rate_bucket_identity,priority:2" json:"scope_id"`
	Namespace     string    `gorm:"size:80;not null;uniqueIndex:idx_automation_rate_bucket_identity,priority:3" json:"namespace"`
	KeyHash       string    `gorm:"size:64;not null;uniqueIndex:idx_automation_rate_bucket_identity,priority:4" json:"key_hash"`
	PolicyHash    string    `gorm:"size:64;not null;uniqueIndex:idx_automation_rate_bucket_identity,priority:5" json:"policy_hash"`
	Algorithm     string    `gorm:"size:24;not null" json:"algorithm"`
	Limit         int       `gorm:"not null" json:"limit"`
	WindowSeconds int       `gorm:"not null" json:"window_seconds"`
	WindowStart   time.Time `gorm:"not null;index" json:"window_start"`
	WindowEnd     time.Time `gorm:"not null;index" json:"window_end"`
	Used          int       `gorm:"not null;default:0" json:"used"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (AutomationRateLimitBucket) TableName() string { return "automation_rate_limit_buckets" }

type AutomationRateLimitClaim struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	RunID         uint      `gorm:"not null;uniqueIndex:idx_automation_rate_claim_run_node,priority:1;index" json:"run_id"`
	NodeID        string    `gorm:"size:80;not null;uniqueIndex:idx_automation_rate_claim_run_node,priority:2" json:"node_id"`
	BucketID      uint      `gorm:"not null;index" json:"bucket_id"`
	ScopeType     string    `gorm:"size:16;not null" json:"scope"`
	ScopeID       uint      `gorm:"not null" json:"scope_id"`
	Namespace     string    `gorm:"size:80;not null" json:"namespace"`
	KeyHash       string    `gorm:"size:64;not null" json:"key_hash"`
	PolicyHash    string    `gorm:"size:64;not null" json:"policy_hash"`
	Algorithm     string    `gorm:"size:24;not null" json:"algorithm"`
	Limit         int       `gorm:"not null" json:"limit"`
	WindowSeconds int       `gorm:"not null" json:"window_seconds"`
	Used          int       `gorm:"not null" json:"used"`
	WindowStart   time.Time `gorm:"not null" json:"window_start"`
	WindowEnd     time.Time `gorm:"not null" json:"window_end"`
	ClaimedAt     time.Time `gorm:"not null;index" json:"claimed_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (AutomationRateLimitClaim) TableName() string { return "automation_rate_limit_claims" }
