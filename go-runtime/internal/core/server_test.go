package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

func testReplay(t *testing.T, receivedAt time.Time) *events.Replay {
	t.Helper()
	replay, err := events.NewReplay(10 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	record := events.Record{ReceivedAt: receivedAt, Epoch: 1, Event: events.Event{
		SchemaVersion: events.SchemaVersion, EventID: "intent-1", LineID: "line-1",
		ProducerRole: events.RoleCore, ProducerID: "core-1", Layer: state.LayerIntent,
		Condition: state.ConditionReady, Available: true, Code: "line_enabled",
		Generation: "config-1", Sequence: 1, ObservedAt: receivedAt,
	}}
	if _, err := replay.Apply(record); err != nil {
		t.Fatal(err)
	}
	return replay
}

func TestLinesAreProjectedAtRequestTime(t *testing.T) {
	receivedAt := time.Unix(1_800_000_000, 0).UTC()
	now := receivedAt.Add(5 * time.Second)
	server := NewServer(testReplay(t, receivedAt), func() time.Time { return now })
	request := httptest.NewRequest(http.MethodGet, "/v1/lines/line-1", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var fresh events.LineProjection
	if err := json.Unmarshal(response.Body.Bytes(), &fresh); err != nil {
		t.Fatal(err)
	}
	if fact(t, fresh, state.LayerIntent).Condition != state.ConditionReady {
		t.Fatalf("fresh projection = %+v", fresh)
	}

	now = receivedAt.Add(11 * time.Second)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var stale events.LineProjection
	if err := json.Unmarshal(response.Body.Bytes(), &stale); err != nil {
		t.Fatal(err)
	}
	if got := fact(t, stale, state.LayerIntent); got.Condition != state.ConditionUnknown || got.Code != "stale" {
		t.Fatalf("stale fact = %+v", got)
	}
}

func TestReadOnlyServerRejectsMutationMethods(t *testing.T) {
	server := NewServer(testReplay(t, time.Now().UTC()), time.Now)
	request := httptest.NewRequest(http.MethodPost, "/v1/lines", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
}

func TestMissingLineUsesMachineErrorCode(t *testing.T) {
	server := NewServer(testReplay(t, time.Now().UTC()), time.Now)
	request := httptest.NewRequest(http.MethodGet, "/v1/lines/missing", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || response.Body.String() != "{\"code\":\"line_not_found\"}\n" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestOnlyLoopbackListenAddressesAreAccepted(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:8443", "localhost:9000"} {
		if !ValidateListenAddress(address) {
			t.Errorf("loopback address rejected: %s", address)
		}
	}
	for _, address := range []string{"0.0.0.0:8443", "[::]:8443", "10.44.0.23:8443", "bad"} {
		if ValidateListenAddress(address) {
			t.Errorf("non-loopback address accepted: %s", address)
		}
	}
}

func fact(t *testing.T, projection events.LineProjection, layer state.Layer) state.FactView {
	t.Helper()
	for _, item := range projection.Facts {
		if item.Layer == layer {
			return item
		}
	}
	t.Fatalf("missing layer %s", layer)
	return state.FactView{}
}
