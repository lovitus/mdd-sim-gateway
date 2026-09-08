package systemupdate

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/updatenetwork"
)

func TestCheckerBindsSuccessfulRouteAndInvalidatesChangedPolicy(t *testing.T) {
	key := "policy-one"
	available := true
	checker, err := NewChecker("example/project", "2.3.0", &http.Client{Transport: releaseTransport{}}, func() (string, []updatenetwork.Route, error) {
		if !available {
			return key, nil, nil
		}
		return key, []updatenetwork.Route{{Mode: "direct"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result := checker.Check(t.Context(), false)
	if !result.OK || result.Network == nil || result.Network.Mode != "direct" {
		t.Fatal(result)
	}
	available = false
	key = "policy-two"
	failed := checker.Check(t.Context(), false)
	if failed.OK || failed.Network != nil || failed.ErrorCode != "update.error.network" {
		t.Fatal("old success masked unavailable new policy", failed)
	}
}

func TestUpdateRequestPersistsVerifiedRouteIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{SchemaVersion: 1, OperationID: "update-one", Repository: "owner/repo", Target: "1.2.3", RequestedAt: time.Now().UTC(),
		Network: &updatenetwork.Route{Mode: "library", ProfileID: "proxy-a", ConfigRevision: 7}}
	if err := store.Request(request); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := reopened.PendingRequest()
	if err != nil || !found || loaded.Network == nil || loaded.Network.ProfileID != "proxy-a" || loaded.Network.ConfigRevision != 7 {
		t.Fatal(loaded, found, err)
	}
	if _, err := loaded.Network.Client(0); err == nil {
		t.Fatal("serialized identity supplied live credentials")
	}
}
