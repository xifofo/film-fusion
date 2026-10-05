package organize

import (
	"context"
	"errors"
	"testing"

	"film-fusion/app/service"
)

func TestCancelledOrganizeScopeDoesNotMutateOrCancelSharedService(t *testing.T) {
	e := newUpgradeTestEnv(t, false, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.h.OrganizeAutomation(ctx, service.AutomationOrganizeRequest{UserID: 1, CloudDirectoryID: 2})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled automation continued: %v", err)
	}
	_, err = e.h.WithContext(ctx).buildOrganize115CookieResult(1, Organize115CookieRequest{CloudDirectoryID: 2, DryRun: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preview continued: %v", err)
	}
	if e.mutations != 0 || e.downloads != 0 || e.h.requestContext().Err() != nil {
		t.Fatal("cancelled scope affected shared service or files")
	}
}
