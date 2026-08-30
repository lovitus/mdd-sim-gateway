package mediaauth

import (
	"context"
	"errors"
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
	provider := Provider{LineID: "line-1", ProviderID: "vowifi-1", Generation: "one", BaseURL: "ws://127.0.0.1:9000", Token: testProviderToken}
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
	second := Provider{LineID: "line-1", ProviderID: "vowifi-1", Generation: "two", BaseURL: "ws://127.0.0.1:9001", Token: testProviderToken}
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
	request := httptest.NewRequest(http.MethodPut, "/v1/media/providers", strings.NewReader(`{"line_id":"line","provider_id":"vowifi-1","generation":"one","base_url":"ws://127.0.0.1:9000","token":"0123456789abcdef0123456789abcdef"}`))
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Authorization", "Bearer "+testProviderToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPut, "/v1/media/providers", strings.NewReader(`{"line_id":"line","provider_id":"vowifi-1","generation":"one","base_url":"ws://127.0.0.1:9000","token":"0123456789abcdef0123456789abcdef"}`))
	request.RemoteAddr = "127.0.0.1:1234"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("token status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPut, "/v1/media/providers", strings.NewReader(`{"line_id":"line","provider_id":"vowifi-1","generation":"one","base_url":"ws://127.0.0.1:9000","token":"0123456789abcdef0123456789abcdef","health":"ready"}`))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer "+testProviderToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPut, "/v1/media/providers", strings.NewReader(`{"line_id":"line","provider_id":"vowifi-1","generation":"one","base_url":"ws://127.0.0.1:9000","token":"0123456789abcdef0123456789abcdef"}`))
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

func TestUseCurrentSerializesOnlySameLineReplacement(t *testing.T) {
	directory := NewProviderDirectory()
	first := Provider{LineID: "line-1", ProviderID: "vowifi-1", Generation: "one", BaseURL: "ws://127.0.0.1:9000", Token: testProviderToken}
	if err := directory.Replace(first); err != nil {
		t.Fatal(err)
	}
	entered, release, used := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		used <- directory.UseCurrent(context.Background(), "line-1", func(provider Provider) error {
			if provider != first {
				t.Errorf("provider=%+v", provider)
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	replaced := make(chan error, 1)
	go func() {
		replaced <- directory.Replace(Provider{
			LineID: "line-1", ProviderID: "vowifi-1", Generation: "two", BaseURL: "ws://127.0.0.1:9001", Token: testProviderToken,
		})
	}()
	select {
	case err := <-replaced:
		t.Fatalf("same-line replacement passed active operation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	other := make(chan error, 1)
	go func() {
		other <- directory.Replace(Provider{
			LineID: "line-2", ProviderID: "vowifi-2", Generation: "one", BaseURL: "ws://127.0.0.1:9002", Token: testProviderToken,
		})
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated line replacement was blocked")
	}

	if err := directory.Replace(first); err != nil {
		t.Fatalf("same-generation heartbeat blocked or failed: %v", err)
	}
	close(release)
	if err := <-used; err != nil {
		t.Fatal(err)
	}
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	if generation, ok := directory.CurrentGeneration("line-1"); !ok || generation != "two" {
		t.Fatalf("current generation=%q ok=%v", generation, ok)
	}
}

func TestUseExpectedRejectsReplacementAndExpiredRoute(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	directory, err := NewProviderDirectoryWithClock(func() time.Time { return now }, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	first := Provider{
		LineID: "line-1", ProviderID: "vowifi-1", Generation: "one", CardID: "8944100000000000001",
		BaseURL: "ws://127.0.0.1:9000", Token: testProviderToken,
	}
	if err := directory.Replace(first); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := directory.UseExpected(t.Context(), first.Fence(), func(provider Provider) error {
		called = true
		if provider != first {
			t.Fatalf("provider=%+v", provider)
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("exact fence err=%v called=%v", err, called)
	}
	second := Provider{
		LineID: "line-1", ProviderID: "vowifi-2", Generation: "two", CardID: "8944100000000000999",
		BaseURL: "ws://127.0.0.1:9001", Token: testProviderToken,
	}
	if err := directory.Replace(second); err != nil {
		t.Fatal(err)
	}
	called = false
	if err := directory.UseExpected(t.Context(), first.Fence(), func(Provider) error {
		called = true
		return nil
	}); !errors.Is(err, ErrProviderFenceConflict) || called {
		t.Fatalf("replacement err=%v called=%v", err, called)
	}
	now = now.Add(31 * time.Second)
	if err := directory.UseExpected(t.Context(), second.Fence(), func(Provider) error {
		called = true
		return nil
	}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("expired route err=%v", err)
	}
}
