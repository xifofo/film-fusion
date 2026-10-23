// Package plutofish consumes authenticated PlutoFish result notifications. It
// never calls a storage provider or invents file locations from notifications.
package plutofish

import (
	"encoding/json"
	"fmt"
)

// Event is a transfer notification. IDs and cursors are deliberately strings;
// neither this package nor callers should derive counters or users from them.
type Event struct {
	ID                  string  `json:"id"`
	Type                string  `json:"type"`
	SchemaVersion       int     `json:"schemaVersion"`
	OccurredAt          string  `json:"occurredAt"`
	TransferID          string  `json:"transferId"`
	SubscriptionID      *string `json:"subscriptionId"`
	Status              string  `json:"status"`
	WorkID              string  `json:"workId"`
	WorkTitle           string  `json:"workTitle"`
	Category            string  `json:"category"`
	MediaType           string  `json:"mediaType"`
	TMDBID              string  `json:"tmdbId"`
	ReleaseID           string  `json:"releaseId"`
	VersionName         string  `json:"versionName"`
	SeasonNumber        *int    `json:"seasonNumber"`
	FileID              string  `json:"fileId"`
	FileName            string  `json:"fileName"`
	SizeBytes           int64   `json:"sizeBytes"`
	SHA1                string  `json:"sha1"`
	Provider            string  `json:"provider"`
	TargetAccountID     string  `json:"targetAccountId"`
	TargetDirectoryID   string  `json:"targetDirectoryId"`
	TargetDirectoryPath string  `json:"targetDirectoryPath"`
	TargetFileID        string  `json:"targetFileId"`
	TargetPickCode      string  `json:"targetPickCode"`
	// Raw preserves unknown fields for durable review and future schema support.
	Raw json.RawMessage `json:"-"`
}

func (e *Event) UnmarshalJSON(data []byte) error {
	type plain Event
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*e = Event(decoded)
	e.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// ReviewReason identifies notifications that must be stored for review instead
// of being dispatched to successful-transfer business logic.
func (e Event) ReviewReason() string {
	if e.SchemaVersion != 1 {
		return fmt.Sprintf("unsupported schemaVersion %d", e.SchemaVersion)
	}
	expected, ok := map[string]string{
		"transfer.succeeded": "succeeded",
		"transfer.failed":    "failed",
		"transfer.unknown":   "unknown",
	}[e.Type]
	if !ok {
		return "unrecognized event type"
	}
	if expected != e.Status {
		return "event type and status do not match"
	}
	if e.TransferID == "" {
		return "missing transferId"
	}
	return ""
}
