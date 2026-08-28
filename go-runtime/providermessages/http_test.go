package providermessages

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
)

const handlerTestToken = "0123456789abcdef0123456789abcdef"

func TestHandlerRequiresLoopbackTokenAndCurrentProviderGeneration(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	providers := mediaauth.NewProviderDirectory()
	if err := providers.Replace(mediaauth.Provider{
		LineID: "line-1", ProviderID: "provider-1", Generation: "generation-1",
		BaseURL: "ws://127.0.0.1:9000", Token: handlerTestToken,
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(providers, store, handlerTestToken)
	if err != nil {
		t.Fatal(err)
	}
	event := validEvent()

	request := messageRequest(t, event, handlerTestToken, "127.0.0.1:1234")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("accepted status=%d body=%s", response.Code, response.Body.String())
	}

	stale := event
	stale.EventID = "generation-2:received:call:1"
	stale.ProcessGeneration = "generation-2"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, messageRequest(t, stale, handlerTestToken, "127.0.0.1:1234"))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale status=%d body=%s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, messageRequest(t, event, "wrong-wrong-wrong-wrong-wrong-wrong", "127.0.0.1:1234"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status=%d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, messageRequest(t, event, handlerTestToken, "192.0.2.10:1234"))
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d", response.Code)
	}
}

func TestPublicHandlerReadsPersistedMessages(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.Accept(validEvent(), time.Now()); err != nil {
		t.Fatal(err)
	}
	handler, _ := NewPublicHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/messages?line_id=line-1&limit=10", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Messages []Record `json:"messages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Messages) != 1 || payload.Messages[0].Body != "hello" {
		t.Fatalf("messages=%+v err=%v", payload.Messages, err)
	}
}

func messageRequest(t *testing.T, event Event, token, remote string) *http.Request {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/provider/messages", bytes.NewReader(payload))
	request.RemoteAddr = remote
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	return request
}
