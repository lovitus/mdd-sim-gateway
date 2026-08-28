package providerapply

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type maintenanceProvider struct {
	mu          sync.Mutex
	lineID      string
	generation  string
	lease       string
	blockDrain  bool
	resumeCount int
}

func (provider *maintenanceProvider) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	var input vowifiipc.MaintenanceRequest
	if request.Header.Get("Authorization") != "Bearer "+providerToken || json.NewDecoder(request.Body).Decode(&input) != nil {
		http.Error(response, "rejected", http.StatusUnauthorized)
		return
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if request.URL.Path == "/v1/maintenance/drain" && provider.blockDrain {
		response.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(response).Encode(vowifiipc.OperationError{
			Kind: vowifiipc.ErrorConflict, Code: "active_call", Layer: "maintenance",
		})
		return
	}
	switch request.URL.Path {
	case "/v1/maintenance/drain":
		provider.lease = input.LeaseID
	case "/v1/maintenance/resume":
		if provider.lease != input.LeaseID {
			response.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(response).Encode(vowifiipc.OperationError{
				Kind: vowifiipc.ErrorConflict, Code: "maintenance_lease_mismatch", Layer: "maintenance",
			})
			return
		}
		provider.lease = ""
		provider.resumeCount++
	default:
		http.NotFound(response, request)
		return
	}
	draining := provider.lease != ""
	_ = json.NewEncoder(response).Encode(vowifiipc.MaintenanceResult{
		LeaseID: input.LeaseID, Draining: draining,
		Status: maintenanceSnapshot(provider.lineID, provider.generation, draining),
	})
}

func TestMaintenanceRollsBackPartialDrainAndRequiresExactRevision(t *testing.T) {
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	for _, line := range []linecatalog.Line{testLine("line-1", "8944100000000000001"), testLine("line-2", "8944100000000000002")} {
		if _, err := catalog.Put(line); err != nil {
			t.Fatal(err)
		}
	}
	directory := mediaauth.NewProviderDirectory()
	first := &maintenanceProvider{lineID: "line-1", generation: "generation-1"}
	second := &maintenanceProvider{lineID: "line-2", generation: "generation-2", blockDrain: true}
	firstServer, secondServer := httptest.NewServer(first), httptest.NewServer(second)
	defer firstServer.Close()
	defer secondServer.Close()
	for _, item := range []struct {
		line, generation, endpoint string
	}{{"line-1", "generation-1", firstServer.URL}, {"line-2", "generation-2", secondServer.URL}} {
		if err := directory.Replace(mediaauth.Provider{
			LineID: item.line, ProviderID: "native", Generation: item.generation,
			BaseURL: "ws" + strings.TrimPrefix(item.endpoint, "http"), Token: providerToken,
		}); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := NewHandler(catalog, directory, preflightToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := DrainRequest{SchemaVersion: 1, CatalogRevision: 2, LeaseID: "apply-lease-1", LineIDs: []string{"line-2", "line-1"}}
	result, err := handler.Maintenance(t.Context(), request, true)
	if err == nil || result.Ready || len(result.Lines) != 2 || result.Lines[0].Code != "drain_rolled_back" ||
		result.Lines[1].Code != "active_call" {
		t.Fatalf("partial result=%+v err=%v", result, err)
	}
	first.mu.Lock()
	lease, resumes := first.lease, first.resumeCount
	first.mu.Unlock()
	if lease != "" || resumes != 1 {
		t.Fatalf("partial drain lease=%q resumes=%d", lease, resumes)
	}
	request.CatalogRevision = 1
	if _, err := handler.Maintenance(t.Context(), request, true); err == nil {
		t.Fatal("stale catalog revision was accepted")
	}
}

func TestMaintenanceDrainsAndResumesCurrentGeneration(t *testing.T) {
	catalog, _ := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	defer catalog.Close()
	_, _ = catalog.Put(testLine("line-1", "8944100000000000001"))
	directory := mediaauth.NewProviderDirectory()
	provider := &maintenanceProvider{lineID: "line-1", generation: "generation-1"}
	server := httptest.NewServer(provider)
	defer server.Close()
	if err := directory.Replace(mediaauth.Provider{
		LineID: "line-1", ProviderID: "native", Generation: "generation-1",
		BaseURL: "ws" + strings.TrimPrefix(server.URL, "http"), Token: providerToken,
	}); err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(catalog, directory, preflightToken, nil)
	request := DrainRequest{SchemaVersion: 1, CatalogRevision: 1, LeaseID: "apply-lease-1", LineIDs: []string{"line-1"}}
	drained, err := handler.Maintenance(context.Background(), request, true)
	if err != nil || !drained.Ready || drained.Lines[0].Code != "drained" ||
		drained.Lines[0].ProcessGeneration != "generation-1" {
		t.Fatalf("drained=%+v err=%v", drained, err)
	}
	resumed, err := handler.Maintenance(context.Background(), request, false)
	if err != nil || !resumed.Ready || resumed.Lines[0].Code != "resumed" {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
}

func maintenanceSnapshot(lineID, generation string, draining bool) vowifiipc.Snapshot {
	stopped := vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped, Code: "stopped"}
	status := vowifiipc.MaintenanceStatus{}
	if draining {
		status = vowifiipc.MaintenanceStatus{Draining: true, Code: "apply_drain"}
	}
	return vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: lineID, ProviderID: "native",
		ProcessGeneration: generation, Sequence: 1, ObservedAt: time.Now().UTC(),
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeStopped},
		Tunnel:  stopped, IMS: stopped, Voice: stopped, Messaging: stopped, Maintenance: status,
	}
}
