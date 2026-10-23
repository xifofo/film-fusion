package model

import "time"

// PlutoFishCursor is scoped by the canonical service URL and explicitly configured
// platform user. Cursor is opaque; neither keys nor cursor components identify a scope.
type PlutoFishCursor struct {
	Scope     string `gorm:"primaryKey;size:64"`
	Cursor    string
	UpdatedAt time.Time
}

// PlutoFishEvent is both the event-id dedup ledger and durable business inbox.
type PlutoFishEvent struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Scope         string    `gorm:"uniqueIndex:idx_pf_event;index;size:64" json:"-"`
	EventID       string    `gorm:"uniqueIndex:idx_pf_event" json:"eventID"`
	TransferID    string    `gorm:"index" json:"transferID"`
	Type          string    `json:"type"`
	SchemaVersion int       `json:"schemaVersion"`
	Status        string    `json:"status"`
	WorkTitle     string    `json:"workTitle"`
	FileName      string    `json:"fileName"`
	OccurredAt    string    `json:"occurredAt"`
	Payload       string    `gorm:"type:text" json:"-"`
	State         string    `gorm:"index" json:"state"`
	Reason        string    `json:"reason"`
	OutputPath    string    `json:"outputPath,omitempty"`
	CreatedAt     time.Time `json:"receivedAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Only successfully completed business creates a receipt. A failed/unknown event
// must never claim a transfer ID and suppress a later success.
type PlutoFishReceipt struct {
	Scope      string `gorm:"primaryKey;size:64"`
	TransferID string `gorm:"primaryKey"`
	EventID    string
	OutputPath string
	CreatedAt  time.Time
}

// PlutoFishBusinessPlan pins the first validated local side effect before any
// file is written. A crash followed by a mapping change must not publish the
// same transfer to a different directory before its receipt can be committed.
type PlutoFishBusinessPlan struct {
	Scope        string `gorm:"primaryKey;size:64"`
	TransferID   string `gorm:"primaryKey"`
	RootPath     string
	RelativePath string
	Content      string `gorm:"type:text"`
	CreatedAt    time.Time
}
