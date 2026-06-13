package embyplayback

import (
	"testing"
	"time"

	"film-fusion/app/store/embyproxylog"
)

func TestStoreTracksActiveRedirectAndStop(t *testing.T) {
	store := NewStore()
	now := time.Now()
	event := Event{
		ItemID:        "item-1",
		MediaSourceID: "media-1",
		PlaySessionID: "play-session-1",
		RemoteIP:      "127.0.0.1",
		UserAgent:     "test-player",
		Timestamp:     now,
	}
	store.MarkActive(event)
	store.AttachRedirect(embyproxylog.Entry{
		Timestamp:         now,
		ItemID:            event.ItemID,
		MediaSourceID:     event.MediaSourceID,
		RemoteIP:          event.RemoteIP,
		UserAgent:         event.UserAgent,
		ActualStorageID:   2,
		ActualStorageName: "target",
		AssignmentID:      10,
	})

	counts := store.ActiveCountsByStorageExcept("")
	if counts[2] != 1 {
		t.Fatalf("active count for storage 2 = %d, want 1", counts[2])
	}
	sessions := store.Snapshot()
	if len(sessions) != 1 {
		t.Fatalf("snapshot length = %d, want 1", len(sessions))
	}
	if sessions[0].AssignmentID != 10 {
		t.Fatalf("assignment id = %d, want 10", sessions[0].AssignmentID)
	}

	store.MarkStopped(event)
	counts = store.ActiveCountsByStorageExcept("")
	if counts[2] != 0 {
		t.Fatalf("active count after stopped = %d, want 0", counts[2])
	}
}
