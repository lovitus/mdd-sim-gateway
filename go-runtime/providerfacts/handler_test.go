package providerfacts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestSnapshotFactsAreDurableAndAppendOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "facts.db")
	store, err := events.OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	replay, _ := events.NewReplay(30 * time.Second)
	providers := mediaauth.NewProviderDirectory()
	registerTestProvider(t, providers, "generation-1")
	now := time.Unix(1_800_300_000, 0).UTC()
	handler, err := newHandlerWithClock(providers, store, replay, testToken, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	client := Client{URL: server.URL + "/v1/provider/facts", Token: testToken}

	first := readySnapshot("generation-1", 1, now.Add(-time.Second))
	if err := client.Report(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if count, _ := store.Count(); count != 5 {
		t.Fatalf("initial event count=%d", count)
	}
	if fact := projectionFact(t, replay, now, state.LayerVoWiFiRuntime); fact.Detail != "pdn_family=dual;idr=ims.apn.epc.mnc015.mcc234.pub.3gppnetwork.org" {
		t.Fatalf("runtime network detail=%+v", fact)
	}

	now = now.Add(10 * time.Second)
	second := first
	second.Sequence = 2
	second.ObservedAt = now.Add(-time.Second)
	if err := client.Report(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if count, _ := store.Count(); count != 5 {
		t.Fatalf("unchanged snapshot appended events: %d", count)
	}
	if fact := projectionFact(t, replay, now.Add(20*time.Second), state.LayerIMS); !fact.Fresh || fact.ReceivedAt != now {
		t.Fatalf("checkpoint did not refresh unchanged fact: %+v", fact)
	}

	now = now.Add(10 * time.Second)
	changed := second
	changed.Sequence = 3
	changed.ObservedAt = now.Add(-time.Second)
	changed.IMS = vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "temporal_failure"}
	if err := client.Report(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	if count, _ := store.Count(); count != 6 {
		t.Fatalf("single changed layer event count=%d", count)
	}
	if fact := projectionFact(t, replay, now, state.LayerIMS); fact.Condition != state.ConditionBlocked || fact.Available {
		t.Fatalf("changed IMS fact=%+v", fact)
	}

	server.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = events.OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted, _ := events.NewReplay(30 * time.Second)
	if err := store.ReplayInto(restarted); err != nil {
		t.Fatal(err)
	}
	if fact := projectionFact(t, restarted, now.Add(20*time.Second), state.LayerTunnel); !fact.Fresh || fact.ReceivedAt != now {
		t.Fatalf("restart lost latest full-snapshot checkpoint: %+v", fact)
	}
}

func TestSnapshotFactsRejectOldGenerationAndInvalidRequests(t *testing.T) {
	store, err := events.OpenBoltStore(filepath.Join(t.TempDir(), "facts.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	replay, _ := events.NewReplay(30 * time.Second)
	providers := mediaauth.NewProviderDirectory()
	registerTestProvider(t, providers, "generation-2")
	handler, _ := newHandlerWithClock(providers, store, replay, testToken, time.Now)

	payload, _ := json.Marshal(readySnapshot("generation-1", 1, time.Now().UTC()))
	request := httptest.NewRequest(http.MethodPut, "/v1/provider/facts", bytes.NewReader(payload))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("old generation status=%d body=%s", response.Code, response.Body.String())
	}

	for name, test := range map[string]struct {
		remote, token, body string
		want                int
	}{
		"remote":        {"192.0.2.1:1234", testToken, string(payload), http.StatusForbidden},
		"wrong token":   {"127.0.0.1:1234", "wrong", string(payload), http.StatusUnauthorized},
		"unknown field": {"127.0.0.1:1234", testToken, `{"schema_version":1,"extra":true}`, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/v1/provider/facts", bytes.NewBufferString(test.body))
			request.RemoteAddr = test.remote
			request.Header.Set("Authorization", "Bearer "+test.token)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func readySnapshot(generation string, sequence uint64, at time.Time) vowifiipc.Snapshot {
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	return vowifiipc.Snapshot{
		SchemaVersion: vowifiipc.SchemaVersion, LineID: "line-1", ProviderID: "provider-1",
		ProcessGeneration: generation, Sequence: sequence, ObservedAt: at,
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning, Code: "ready",
			PDNFamily: "dual", ResponderID: "ims.apn.epc.mnc015.mcc234.pub.3gppnetwork.org"},
		Tunnel: ready, IMS: ready, Voice: ready, Messaging: ready,
	}
}

func registerTestProvider(t *testing.T, providers *mediaauth.ProviderDirectory, generation string) {
	t.Helper()
	if err := providers.Replace(mediaauth.Provider{
		LineID: "line-1", ProviderID: "provider-1", Generation: generation,
		BaseURL: "ws://127.0.0.1:39000", Token: testToken,
	}); err != nil {
		t.Fatal(err)
	}
}

func projectionFact(t *testing.T, replay *events.Replay, at time.Time, layer state.Layer) state.FactView {
	t.Helper()
	for _, projection := range replay.Projections(at) {
		if projection.LineID != "line-1" {
			continue
		}
		for _, fact := range projection.Facts {
			if fact.Layer == layer {
				return fact
			}
		}
	}
	t.Fatalf("missing %s fact", layer)
	return state.FactView{}
}
