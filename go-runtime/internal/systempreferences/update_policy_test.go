package systempreferences

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/updatenetwork"
)

func TestQueuedUpdateChecksCurrentPreferenceRevision(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "preferences.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token := strings.Repeat("t", 32)
	handler, err := NewUpdatePolicyHandler(store, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	if err := updatenetwork.CheckPolicy(t.Context(), server.URL, token, 1, server.Client()); err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("policy version is not protected", response.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || strings.Contains(string(data), "audit_enabled") || strings.Contains(string(data), "trusted_proxies") || strings.Contains(string(data), "updates") {
		t.Fatal("private policy check exposed unrelated preferences")
	}
	current, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	current.Preferences.Updates = &updatenetwork.Selection{Mode: "auto"}
	if _, err := store.PutExpected(current.Preferences, current.Revision); err != nil {
		t.Fatal(err)
	}
	if err := updatenetwork.CheckPolicy(t.Context(), server.URL, token, 1, server.Client()); err == nil {
		t.Fatal("queued request ignored changed settings")
	}
}
