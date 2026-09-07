package systemupdate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestReleaseComparisonRejectsCommitIDsAndHandlesPrereleases(t *testing.T) {
	for _, current := range []string{"a1f32d9c06b6", "123456789012", "(devel)", "", "1..0"} {
		if newer(current, "2.4.0") {
			t.Fatalf("unversioned build %q allowed an upgrade", current)
		}
	}
	for _, item := range []struct {
		current, latest string
		want            bool
	}{
		{"1.2.3-rc.1", "1.2.3", true}, {"1.2.3", "1.2.3-rc.1", false},
		{"1.2.3+build1", "1.2.3+build2", false}, {"v1.2.3", "v1.3.0", true},
	} {
		if newer(item.current, item.latest) != item.want {
			t.Fatalf("comparison: %+v", item)
		}
	}
	checker, err := NewChecker("example/project", "a1f32d9c06b6", &http.Client{Transport: releaseTransport{}})
	if err != nil {
		t.Fatal(err)
	}
	result := checker.Check(context.Background(), false)
	if !result.OK || result.ComparisonKnown || result.UpdateAvailable || result.ErrorCode != "update.version_uncomparable" {
		t.Fatalf("result=%+v", result)
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

func TestApplyRequiresExactConfirmedReleaseAndUnknownCannotReplay(t *testing.T) {
	for _, item := range []struct {
		body   string
		status int
	}{
		{`{}`, 400}, {`{"expected_target":"2.3.9"}`, 409},
		{`{"expected_target":"2.4.0","extra":true}`, 400},
		{`{"expected_target":"2.4.0"} {}`, 400}, {`{"expected_target":"2.4.0"}`, 202},
	} {
		store, err := Open(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		checker, err := NewChecker("example/project", "2.3.0", &http.Client{Transport: releaseTransport{}})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewHandler(checker, store)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/system/update/apply", strings.NewReader(item.body)))
		if response.Code != item.status {
			t.Fatalf("body=%s status=%d response=%s", item.body, response.Code, response.Body.String())
		}
		_, found, err := store.PendingRequest()
		if err != nil || found != (item.status == 202) {
			t.Fatalf("unexpected request: %v %v", found, err)
		}
		if found {
			status, err := store.Status()
			if err != nil {
				t.Fatal(err)
			}
			status.State = StateUnknown
			if err := store.SetStatus(status); err != nil {
				t.Fatal(err)
			}
			next := httptest.NewRecorder()
			handler.ServeHTTP(next, httptest.NewRequest(http.MethodPost, "/v1/system/update/apply", strings.NewReader(item.body)))
			if next.Code != 409 {
				t.Fatal("unknown update replayed")
			}
		}
	}
}
