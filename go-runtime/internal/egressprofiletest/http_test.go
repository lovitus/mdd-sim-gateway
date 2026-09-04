package egressprofiletest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressexec"
)

type testStore struct{ snapshot egressconfig.Snapshot }

func (store testStore) Snapshot() (egressconfig.Snapshot, error) { return store.snapshot, nil }

func TestHandlerTestsExactSavedProfileWithoutApplying(t *testing.T) {
	store := testStore{snapshot: egressconfig.Snapshot{SchemaVersion: 2, Revision: 7,
		Config: egressconfig.Config{Profiles: map[string]egressconfig.Profile{
			"node-a": {Name: "Node A", Type: "node", Value: "ss://fixture"},
		}}}}
	handler, err := NewHandler(store, "/usr/local/bin/sing-box", filepath.Join(t.TempDir(), "tests"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	handler.probe = func(_ context.Context, binary, root string, profile egressconfig.Profile) (egressexec.ProfileProbeResult, error) {
		calls++
		if binary != "/usr/local/bin/sing-box" || !filepath.IsAbs(root) || profile.Name != "Node A" {
			t.Fatalf("unexpected probe binary=%q root=%q profile=%+v", binary, root, profile)
		}
		return egressexec.ProfileProbeResult{Node: profile.Name, LatencyMS: 12, Target: "1.1.1.1"}, nil
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/egress/profiles/node-a/test", nil)
	request.SetPathValue("profileID", "node-a")
	request.Header.Set("If-Match", `"7"`)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	if store.snapshot.Revision != 7 {
		t.Fatal("profile test mutated desired revision")
	}
}
