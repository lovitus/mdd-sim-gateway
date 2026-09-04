package systemupdate

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type releaseTransport struct{}

func (releaseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v2.4.0","html_url":"https://github.com/example/release","published_at":"2026-09-05T00:00:00Z","body":"notes"}`)), Header: make(http.Header)}, nil
}

func TestCheckerUsesReleaseMetadataAndCaches(t *testing.T) {
	checker, err := NewChecker("example/project", "2.3.0", &http.Client{Transport: releaseTransport{}})
	if err != nil {
		t.Fatal(err)
	}
	result := checker.Check(context.Background(), false)
	if !result.OK || !result.UpdateAvailable || result.Latest != "2.4.0" || result.ReleaseURL == "" {
		t.Fatalf("result=%+v", result)
	}
	cached := checker.Check(context.Background(), false)
	if cached.Latest != result.Latest || cached.CheckedAt != result.CheckedAt {
		t.Fatalf("cache=%+v result=%+v", cached, result)
	}
}

func TestCheckerRejectsInvalidRepositoryAndVersions(t *testing.T) {
	if _, err := NewChecker("not a repo", "1.0.0", nil); err == nil {
		t.Fatal("invalid repository accepted")
	}
	if newer("1.9.0", "1.10.0") != true || newer("2.0.0", "1.99.0") {
		t.Fatal("version comparison incorrect")
	}
}

func TestUpdateStorePersistsRequestAndRejectsConcurrentApply(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "update-state"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := Request{SchemaVersion: 1, OperationID: "update-operation-1", Repository: "example/project", Target: "2.0.0", RequestedAt: now}
	if err := store.Request(request); err != nil {
		t.Fatal(err)
	}
	status, err := store.Status()
	if err != nil || status.State != StateRequested || status.OperationID != request.OperationID {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if err := store.Request(request); err == nil {
		t.Fatal("concurrent update request accepted")
	}
}
