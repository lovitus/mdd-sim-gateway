package linecatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotIPCReadsLiveCatalogWithBearer(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(Line{
		ID: "line-1", Enabled: true, CardID: "8944100000000000001",
		SIM: SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"},
	}); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("t", 32)
	handler, err := NewSnapshotHandler(store, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	if _, err := FetchSnapshot(context.Background(), server.URL, "wrong-token-that-is-still-long-enough", server.Client()); err == nil {
		t.Fatal("wrong snapshot bearer was accepted")
	}
	snapshot, err := FetchSnapshot(context.Background(), server.URL, token, server.Client())
	if err != nil || snapshot.Revision != 2 || len(snapshot.Lines) != 1 || snapshot.Lines[0].ID != "line-1" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(request)
	if err != nil || response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("response=%v err=%v", response, err)
	}
	_ = response.Body.Close()
}
