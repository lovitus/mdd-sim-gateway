package egressprobe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandlerListsSanitizedExitFactsAndRunsExplicitProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-status.json")
	payload := `{"exits":{"GB":{"ready":true,"mode":"manual","node":"London","host_proxy_host":"127.0.0.1","proxy_port":22157},"fr":{"ready":false,"error":"node unavailable"}}}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var gotURL string
	handler.probe = func(_ context.Context, proxyURL string) (Result, error) {
		gotURL = proxyURL
		return Result{LatencyMS: 7, Target: "8.8.8.8", AttemptedTargets: []string{"1.1.1.1", "8.8.8.8"}}, nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/egress/exits", handler)
	mux.Handle("POST /v1/egress/exits/{country}/test", handler)

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/egress/exits", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var listed struct {
		Exits []ExitStatus `json:"exits"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Exits) != 2 || listed.Exits[0].Country != "fr" || listed.Exits[0].Testable ||
		listed.Exits[1].Country != "gb" || !listed.Exits[1].Testable || listed.Exits[1].Node != "London" {
		t.Fatalf("exits=%+v", listed.Exits)
	}

	test := httptest.NewRecorder()
	mux.ServeHTTP(test, httptest.NewRequest(http.MethodPost, "/v1/egress/exits/gb/test", nil))
	if test.Code != http.StatusOK || gotURL != "socks5://127.0.0.1:22157" {
		t.Fatalf("test status=%d url=%q body=%s", test.Code, gotURL, test.Body.String())
	}
}

func TestHandlerPreservesProbeFailureLayer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-status.json")
	if err := os.WriteFile(path, []byte(`{"exits":{"gb":{"ready":true,"host_proxy_host":"127.0.0.1","proxy_port":22157}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(path, time.Second)
	handler.probe = func(context.Context, string) (Result, error) { return Result{}, errors.New("both targets timed out") }
	mux := http.NewServeMux()
	mux.Handle("POST /v1/egress/exits/{country}/test", handler)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/egress/exits/gb/test", nil))
	if response.Code != http.StatusServiceUnavailable || !stringsContainsAll(response.Body.String(), "egress_udp_probe_failed", "country_egress_udp", "both targets timed out") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func stringsContainsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
