package mediaauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProviderRegistrationRefreshExpiryAndGenerationFencing(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	directory, err := NewProviderDirectoryWithClock(func() time.Time { return now }, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewRegistrationHandler(directory, testProviderToken)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := RegistrationClient{URL: server.URL + "/v1/media/providers", Token: testProviderToken}
	provider := Provider{LineID: "line-1", Generation: "one", BaseURL: "ws://127.0.0.1:9000", Token: testProviderToken}
	if err := client.Register(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	now = now.Add(29 * time.Second)
	if _, err := directory.ResolveMedia(context.Background(), "line-1", "one", "session"); err != nil {
		t.Fatal(err)
	}
	if err := client.Register(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	now = now.Add(29 * time.Second)
	if _, err := directory.ResolveMedia(context.Background(), "line-1", "one", "session"); err != nil {
		t.Fatal("refresh did not extend route lease")
	}
	now = now.Add(31 * time.Second)
	if _, err := directory.ResolveMedia(context.Background(), "line-1", "one", "session"); err == nil {
		t.Fatal("expired provider route remained available")
	}
	now = now.Add(time.Second)
	second := Provider{LineID: "line-1", Generation: "two", BaseURL: "ws://127.0.0.1:9001", Token: testProviderToken}
	if err := client.Register(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := client.Register(context.Background(), provider); err == nil {
		t.Fatal("replaced provider generation registered again")
	}
	if err := client.Remove(context.Background(), "line-1", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.ResolveMedia(context.Background(), "line-1", "two", "session"); err != nil {
		t.Fatal("late remove deleted current provider")
	}
}

func TestRegistrationRejectsRemoteWrongTokenAndUnknownFields(t *testing.T) {
	directory := NewProviderDirectory()
	handler, _ := NewRegistrationHandler(directory, testProviderToken)
	request := httptest.NewRequest(http.MethodPut, "/v1/media/providers", strings.NewReader(`{"line_id":"line","generation":"one","base_url":"ws://127.0.0.1:9000","token":"0123456789abcdef0123456789abcdef"}`))
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Authorization", "Bearer "+testProviderToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPut, "/v1/media/providers", strings.NewReader(`{"line_id":"line","generation":"one","base_url":"ws://127.0.0.1:9000","token":"0123456789abcdef0123456789abcdef"}`))
	request.RemoteAddr = "127.0.0.1:1234"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("token status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPut, "/v1/media/providers", strings.NewReader(`{"line_id":"line","generation":"one","base_url":"ws://127.0.0.1:9000","token":"0123456789abcdef0123456789abcdef","health":"ready"}`))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer "+testProviderToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPut, "/v1/media/providers", strings.NewReader(`{"line_id":"line","generation":"one","base_url":"ws://127.0.0.1:9000","token":"0123456789abcdef0123456789abcdef"}`))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer "+testProviderToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("wire contract status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRegistrationClientRejectsRemoteURL(t *testing.T) {
	client := RegistrationClient{URL: "http://192.0.2.1:9000/v1/media/providers", Token: testProviderToken}
	if err := client.Register(context.Background(), Provider{}); err == nil {
		t.Fatal("remote registration URL was accepted")
	}
}
