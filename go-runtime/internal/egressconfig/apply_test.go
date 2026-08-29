package egressconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordingApplyService struct {
	configRevision  uint64
	catalogRevision uint64
}

func (service *recordingApplyService) EgressStatus(context.Context) (ApplyStatus, error) {
	return ApplyStatus{SchemaVersion: SchemaVersion, ConfigRevision: 4, CatalogRevision: 9}, nil
}

func (service *recordingApplyService) ApplyEgress(_ context.Context, configRevision, catalogRevision uint64) (ApplyResult, error) {
	service.configRevision, service.catalogRevision = configRevision, catalogRevision
	return ApplyResult{SchemaVersion: SchemaVersion, ConfigRevision: configRevision, CatalogRevision: catalogRevision,
		Generation: strings.Repeat("a", 64), State: "applied", Code: "runtime_confirmed"}, nil
}

func TestApplyRequestAcceptsOnlyTwoRevisions(t *testing.T) {
	service := &recordingApplyService{}
	handler, err := NewApplyHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{`"path":"/tmp/owned"`, `"command":"restart"`, `"document":{"proxy":{}}`} {
		body := `{"schema_version":2,"config_revision":4,"catalog_revision":9,` + extra + `}`
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, ApplyPath, strings.NewReader(body)))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_egress_apply_request") {
			t.Fatalf("extra field %s response=%d %s", extra, response.Code, response.Body.String())
		}
	}
	valid := httptest.NewRecorder()
	handler.ServeHTTP(valid, httptest.NewRequest(http.MethodPost, ApplyPath,
		strings.NewReader(`{"schema_version":2,"config_revision":4,"catalog_revision":9}`)))
	if valid.Code != http.StatusOK || service.configRevision != 4 || service.catalogRevision != 9 {
		t.Fatalf("valid apply=%d body=%s revisions=%d/%d", valid.Code, valid.Body.String(), service.configRevision, service.catalogRevision)
	}
}

func TestApplyClientWirePayloadContainsNoPrivilegedInput(t *testing.T) {
	var keys map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get(applyTokenHeader) != strings.Repeat("t", 32) {
			t.Fatal("missing helper authentication token")
		}
		if err := json.NewDecoder(request.Body).Decode(&keys); err != nil {
			t.Fatal(err)
		}
		writeJSON(response, http.StatusOK, ApplyResult{SchemaVersion: SchemaVersion, ConfigRevision: 7,
			CatalogRevision: 11, Generation: strings.Repeat("b", 64), State: "applied", Code: "runtime_confirmed"})
	}))
	defer server.Close()
	client := &ApplyClient{token: strings.Repeat("t", 32), http: server.Client()}
	// The production client uses a Unix socket; replace only its HTTP destination for this
	// wire-contract test while exercising the same request encoder.
	original := client.http.Transport
	client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		request.URL.Scheme, request.URL.Host = "http", strings.TrimPrefix(server.URL, "http://")
		return original.RoundTrip(request)
	})
	if _, err := client.ApplyEgress(context.Background(), 7, 11); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"schema_version": true, "config_revision": true, "catalog_revision": true}
	if len(keys) != len(want) {
		t.Fatalf("helper payload keys=%v", keys)
	}
	for key := range keys {
		if !want[key] {
			t.Fatalf("unexpected helper payload field %q", key)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	if request.Body != nil {
		payload := new(bytes.Buffer)
		_, _ = payload.ReadFrom(request.Body)
		clone.Body = ioNopCloser{bytes.NewReader(payload.Bytes())}
	}
	return function(clone)
}

type ioNopCloser struct{ *bytes.Reader }

func (ioNopCloser) Close() error { return nil }
