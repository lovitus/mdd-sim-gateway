package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPermanentDeletionRouteOverridesCatalogLifecycleWildcard(t *testing.T) {
	catalogCalled, deletionCalled := false, false
	server := NewServer(testReplay(t, time.Now().UTC()), time.Now,
		WithLineCatalog(nil, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			catalogCalled = true
			response.WriteHeader(http.StatusTeapot)
		})),
		WithLineDeletion(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			deletionCalled = true
			response.WriteHeader(http.StatusNoContent)
		})))
	request := httptest.NewRequest(http.MethodPost, "/v1/catalog/lines/line-1/permanent-delete", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !deletionCalled || catalogCalled {
		t.Fatalf("status=%d deletion=%t catalog=%t", response.Code, deletionCalled, catalogCalled)
	}
}
