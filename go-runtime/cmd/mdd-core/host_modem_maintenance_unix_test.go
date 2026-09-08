//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHostMaintenanceIPCValidatesIdentityAndLeaseEcho(t *testing.T) {
	agent := "host-agent"
	lease := "lease-fixture"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-MDD-Provider-Apply-Token") != strings.Repeat("x", 32) {
			t.Error("missing helper authentication")
		}
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(hostIdleSnapshot{Ready: true, AgentID: agent, Generation: "generation", Revision: 1, ObservedAt: time.Now(), LineIDs: []string{"line"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"agent_id": agent, "lease_id": lease, "held": true})
	}))
	defer server.Close()
	service := &providerApplyService{}
	service.settings.Local.Listen = strings.TrimPrefix(server.URL, "http://")
	service.settings.Local.Token = strings.Repeat("x", 32)
	if _, err := service.hostIdle(context.Background(), "host-agent"); err != nil {
		t.Fatal(err)
	}
	if err := service.hostAgentLease(context.Background(), "host-agent", "generation", "lease-fixture", true); err != nil {
		t.Fatal(err)
	}
	if err := service.hostAgentLease(context.Background(), "host-agent", "generation", "wrong-lease", true); err == nil {
		t.Fatal("wrong lease echo accepted")
	}
	agent = "other-agent"
	if _, err := service.hostIdle(context.Background(), "host-agent"); err == nil {
		t.Fatal("different Agent accepted")
	}
}
