package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

func TestSetupApplyBlockersKeepActualReason(t *testing.T) {
	for _, code := range []string{"unrelated_provider_changes_pending", "active_call", "provider_unreachable", "catalog_revision_changed"} {
		t.Run(code, func(t *testing.T) {
			plan := providerapply.Plan{Blockers: []providerapply.Blocker{{LineID: "private-line", Code: code}}}
			err := providerFailure(http.StatusConflict, "provider_apply_blocked", providerApplyBlockedError{plan: plan})
			var failure *provideradmin.Error
			if !errors.As(err, &failure) || failure.Code != "provider_apply_blocked" || failure.Detail != "provider apply plan is blocked: "+code {
				t.Fatalf("SETUP_BLOCKER: lost real blocker: %v", err)
			}
		})
	}
}
