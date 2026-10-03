package model

import "time"

// APIToken stores only a digest; the credential is returned once on creation.
type APIToken struct {
	ID         uint       `json:"id" gorm:"primaryKey"`
	UserID     uint       `json:"-" gorm:"not null;index"`
	Name       string     `json:"name" gorm:"size:80;not null"`
	Prefix     string     `json:"prefix" gorm:"size:20;not null"`
	TokenHash  string     `json:"-" gorm:"size:64;not null;uniqueIndex"`
	Scopes     []string   `json:"scopes" gorm:"serializer:json;type:text;not null"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}
