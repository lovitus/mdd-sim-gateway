package systemstatus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSystemStatusHTTPReturnsExplicitUnavailableAndRejectsMutation(t *testing.T) {
	sampler := newTestSampler(t, collectorFunc(func(context.Context) Snapshot { return completeTestSnapshot() }),
		time.Minute, time.Second, time.Now)
	handler, err := NewHandler(sampler, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/system/status", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(response.Body.String(), `"state":"unavailable"`) ||
		!strings.Contains(response.Body.String(), `"code":"status_unavailable"`) {
		t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/v1/system/status", strings.NewReader("{}")),
		httptest.NewRequest(http.MethodGet, "/v1/system/status?refresh=true", nil),
	} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			t.Fatalf("mutation/query accepted: %s", response.Body.String())
		}
	}
}
