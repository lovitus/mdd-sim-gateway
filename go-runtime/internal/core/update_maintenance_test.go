package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestUpdateProviderCleanupKeepsExactGenerationCardAndLease(t *testing.T) {
	directory := mediaauth.NewProviderDirectory()
	var mu sync.Mutex
	lease := ""
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 32) {
			w.WriteHeader(401)
			return
		}
		var input vowifiipc.MaintenanceRequest
		if r.Method == http.MethodPost {
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				w.WriteHeader(400)
				return
			}
			switch r.URL.Path {
			case "/v1/maintenance/drain":
				lease = input.LeaseID
			case "/v1/maintenance/resume":
				if input.LeaseID != lease {
					w.WriteHeader(409)
					return
				}
				lease = ""
			default:
				w.WriteHeader(404)
				return
			}
		}
		status := vowifiipc.Snapshot{SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-a", ProviderID: "provider-a", ProcessGeneration: "generation-a", Sequence: 1, ObservedAt: time.Now(), Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeStopped}}
		status.Tunnel.Condition = vowifiipc.LayerStopped
		status.IMS.Condition = vowifiipc.LayerStopped
		status.Voice.Condition = vowifiipc.LayerStopped
		status.Messaging.Condition = vowifiipc.LayerStopped
		if lease != "" {
			status.Maintenance = vowifiipc.MaintenanceStatus{Draining: true, Code: "apply_drain", LeaseID: lease}
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(status)
			return
		}
		_ = json.NewEncoder(w).Encode(vowifiipc.MaintenanceResult{LeaseID: input.LeaseID, Draining: lease != "", Status: status})
	}))
	defer server.Close()
	provider := mediaauth.Provider{LineID: "line-a", ProviderID: "provider-a", Generation: "generation-a", CardID: "8944100000000000001", BaseURL: strings.Replace(server.URL, "http:", "ws:", 1), Token: strings.Repeat("a", 32)}
	if err := directory.Replace(provider); err != nil {
		t.Fatal(err)
	}
	runtime := UpdateMaintenanceRuntime{Providers: directory}
	for _, action := range []string{"begin", "verify", "release", "release"} {
		if err := runtime.Provider(t.Context(), provider.Fence(), "update-one", action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	mu.Lock()
	lease = "foreign-owner"
	mu.Unlock()
	if err := runtime.Provider(t.Context(), provider.Fence(), "update-one", "release"); err == nil {
		t.Fatal("FOREIGN_LEASE: unrelated drain released")
	}
	mu.Lock()
	if lease != "foreign-owner" {
		t.Fatal("foreign lease changed")
	}
	before := requests
	mu.Unlock()
	replacement := provider
	replacement.Generation = "generation-new"
	replacement.CardID = "8944100000000000002"
	if err := directory.Replace(replacement); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Provider(t.Context(), provider.Fence(), "update-one", "release"); err == nil {
		t.Fatal("PROVIDER_REPLACED: wrong process reached")
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != before {
		t.Fatal("stale operation reached replacement HTTP endpoint")
	}
}
