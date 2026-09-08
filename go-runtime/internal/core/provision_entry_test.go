package core

import (
	"context"
	"net/http"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func TestInternalProvisionEntryKeepsInputGateBeforeRuntime(t *testing.T) {
	handler := &ProvisionHandler{}
	for _, ctx := range []context.Context{nil, context.Background()} {
		status, payload := handler.executeProvision(ctx, provisionAPIRequest{})
		if status != http.StatusBadRequest || payload.(map[string]string)["code"] != "invalid_provision_request" {
			t.Fatal("internal entry bypassed provision validation")
		}
	}
}

func TestInternalReadbackEntryKeepsInputGateBeforeRuntime(t *testing.T) {
	handler := &ProvisionReadbackHandler{}
	for _, ctx := range []context.Context{nil, context.Background()} {
		status, payload := handler.executeReadback(ctx, agentlink.ProvisionCommand{})
		if status != http.StatusBadRequest || payload.(map[string]string)["code"] != "invalid_provision_readback_request" {
			t.Fatal("internal entry bypassed readback validation")
		}
	}
}
