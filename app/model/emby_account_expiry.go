package model

import "time"

// EmbyAccountExpiry scopes a deadline to the actual Emby server, so changing the
// configured server cannot apply an old deadline to an unrelated account.
type EmbyAccountExpiry struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	ServerID      string     `gorm:"size:120;not null;uniqueIndex:uk_emby_account_expiry" json:"server_id"`
	EmbyUserID    string     `gorm:"size:120;not null;uniqueIndex:uk_emby_account_expiry" json:"emby_user_id"`
	ExpiresAt     *time.Time `gorm:"index" json:"expires_at"`
	DisabledAt    *time.Time `json:"disabled_at"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastError     string     `json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (EmbyAccountExpiry) TableName() string { return "emby_account_expiries" }
