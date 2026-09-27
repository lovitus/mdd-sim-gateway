package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestMobileStreamRequiresAuthAndRevokesSession(t *testing.T) {
	verifier := &toggleBrowserVerifier{}
	server := NewServer(testReplay(t, time.Now()), time.Now, WithBrowserControl(verifier))
	server.browserEvery = 10 * time.Millisecond
	host := httptest.NewServer(server)
	defer host.Close()
	url := "ws" + strings.TrimPrefix(host.URL, "http") + "/v1/mobile/ws"
	if conn, response, err := websocket.Dial(t.Context(), url, nil); err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		if conn != nil {
			conn.CloseNow()
		}
		t.Fatalf("unauthorized=%v %v", response, err)
	}
	verifier.allowed.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var first mobileEnvelope
	if err = wsjson.Read(ctx, conn, &first); err != nil {
		t.Fatal(err)
	}
	if first.Type != "mobile.snapshot" || first.Sequence != 1 || first.Data == nil || first.SchemaVersion != 1 {
		t.Fatalf("bad first snapshot: %+v", first)
	}
	verifier.allowed.Store(false)
	var next mobileEnvelope
	if err = wsjson.Read(ctx, conn, &next); websocket.CloseStatus(err) != browserAuthClose {
		t.Fatalf("revocation did not close: %v", err)
	}
}
func TestMobileStreamRejectsCrossOrigin(t *testing.T) {
	verifier := &toggleBrowserVerifier{}
	verifier.allowed.Store(true)
	host := httptest.NewServer(NewServer(testReplay(t, time.Now()), time.Now, WithBrowserControl(verifier)))
	defer host.Close()
	conn, response, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(host.URL, "http")+"/v1/mobile/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://other.invalid"}}})
	if conn != nil {
		conn.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross origin=%v %v", response, err)
	}
}

func TestMobileStreamSuppressesConfirmedClocksButPublishesExpiry(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	var clock atomic.Int64
	clock.Store(at.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	replay := testReplay(t, at)
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "lines.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	if _, err = catalog.Put(linecatalog.Line{ID: "line-1", Enabled: true, CardID: "8944100000000000001",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}}); err != nil {
		t.Fatal(err)
	}
	verifier := &toggleBrowserVerifier{}
	verifier.allowed.Store(true)
	server := NewServer(replay, now, WithBrowserControl(verifier), WithLineCatalog(catalog, nil))
	server.browserEvery = 10 * time.Millisecond
	host := httptest.NewServer(server)
	defer host.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(host.URL, "http")+"/v1/mobile/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var first mobileEnvelope
	if err = wsjson.Read(ctx, conn, &first); err != nil || first.Data == nil || len(first.Data.Lines) != 1 {
		t.Fatalf("initial snapshot=%+v err=%v", first, err)
	}
	confirmed := at.Add(2 * time.Second)
	if err = replay.Confirm(events.ProducerCheckpoint{LineID: "line-1", ProducerRole: events.RoleCore,
		ProducerID: "core-1", Generation: "config-1", Layers: []state.Layer{state.LayerIntent},
		Sequence: 2, ObservedAt: confirmed, ReceivedAt: confirmed}); err != nil {
		t.Fatal(err)
	}
	invalidate := func(at time.Time) {
		server.mobileMu.Lock()
		defer server.mobileMu.Unlock()
		clock.Store(at.UnixNano())
		server.mobileAt = time.Time{}
	}
	invalidate(confirmed)
	refreshed := server.mobileSnapshot(ctx)
	beforeDigest, err := json.Marshal(refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mobileDigest(refreshed); err != nil {
		t.Fatal(err)
	}
	afterDigest, err := json.Marshal(refreshed)
	if err != nil || string(beforeDigest) != string(afterDigest) {
		t.Fatal("digest mutated the shared snapshot")
	}
	type received struct {
		frame mobileEnvelope
		err   error
	}
	next := make(chan received, 1)
	go func() {
		var frame mobileEnvelope
		err := wsjson.Read(ctx, conn, &frame)
		next <- received{frame, err}
	}()
	select {
	case got := <-next:
		t.Fatalf("health confirmation emitted a redundant frame: type=%s err=%v", got.frame.Type, got.err)
	case <-time.After(200 * time.Millisecond):
	}
	invalidate(confirmed.Add(11 * time.Second))
	select {
	case got := <-next:
		if got.err != nil || got.frame.Type != "mobile.snapshot" || got.frame.Sequence != 2 || got.frame.Data == nil {
			t.Fatalf("expiry update=%+v err=%v", got.frame, got.err)
		}
		found := false
		for _, operation := range got.frame.Data.Lines[0].Operations {
			for _, fact := range operation.Facts {
				if fact.Layer != state.LayerIntent {
					continue
				}
				found = true
				if fact.Fresh || fact.Available || fact.Code != "stale" ||
					!fact.ReceivedAt.Equal(confirmed) || !fact.ExpiresAt.Equal(confirmed.Add(10*time.Second)) {
					t.Fatalf("expiry or original clocks lost: %+v", fact)
				}
			}
		}
		if !found {
			t.Fatal("expired intent fact absent")
		}
	case <-ctx.Done():
		t.Fatal("real expiry was not delivered")
	}
}

func TestMobileDigestRetainsIdentityAndBusinessChanges(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	data := mobileData{Lines: []mobileLine{{ID: "line-1", CardID: "card-1", Operations: map[string]state.Readiness{
		"vowifi_call": {Ready: true, Facts: []state.FactView{{Layer: state.LayerIMS, Condition: state.ConditionReady,
			Available: true, Fresh: true, Generation: "provider-1", Epoch: 1, Sequence: 7,
			ObservedAt: at, ReceivedAt: at, ExpiresAt: at.Add(10 * time.Second)}}},
	}}}}
	data.Lines[0].Incoming = &vowifiipc.PendingIncomingCall{CallID: "incoming-1", ReceivedAt: at}
	data.Messages = []providermessages.Record{{ReceivedAt: at}}
	data.CellularCalls = []cellularmedia.IncomingCallView{{Actionable: true}}
	data.CellularCalls[0].IncomingEventID = "ring-1"
	initial, err := mobileDigest(data)
	if err != nil {
		t.Fatal(err)
	}
	data.Lines[0].Operations["vowifi_call"].Facts[0].ReceivedAt = at.Add(time.Second)
	data.Lines[0].Operations["vowifi_call"].Facts[0].ExpiresAt = at.Add(11 * time.Second)
	confirmed, err := mobileDigest(data)
	if err != nil || initial != confirmed {
		t.Fatal("receipt-clock-only confirmation was considered a business change")
	}
	baseline, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*mobileData)
	}{
		{"card", func(d *mobileData) { d.Lines[0].CardID = "card-2" }},
		{"generation", func(d *mobileData) { d.Lines[0].Operations["vowifi_call"].Facts[0].Generation = "provider-2" }},
		{"epoch", func(d *mobileData) { d.Lines[0].Operations["vowifi_call"].Facts[0].Epoch++ }},
		{"sequence", func(d *mobileData) { d.Lines[0].Operations["vowifi_call"].Facts[0].Sequence++ }},
		{"observation", func(d *mobileData) { d.Lines[0].Operations["vowifi_call"].Facts[0].ObservedAt = at.Add(time.Second) }},
		{"freshness", func(d *mobileData) { d.Lines[0].Operations["vowifi_call"].Facts[0].Fresh = false }},
		{"blocked", func(d *mobileData) {
			d.Lines[0].Operations["vowifi_call"] = state.Readiness{Blocked: []state.Layer{state.LayerIMS}}
		}},
		{"incomplete", func(d *mobileData) { d.Incomplete = true }},
		{"incoming time", func(d *mobileData) { d.Lines[0].Incoming.ReceivedAt = at.Add(time.Second) }},
		{"incoming removed", func(d *mobileData) { d.Lines[0].Incoming = nil }},
		{"active", func(d *mobileData) { d.Lines[0].Active = &vowifiipc.ActiveCall{CallID: "call-1"} }},
		{"cellular incoming", func(d *mobileData) { d.CellularCalls[0].IncomingEventID = "ring-2" }},
		{"cellular claim", func(d *mobileData) { d.CellularCalls[0].Actionable = false }},
		{"message time", func(d *mobileData) { d.Messages[0].ReceivedAt = at.Add(time.Second) }},
	} {
		var changed mobileData
		if err := json.Unmarshal(baseline, &changed); err != nil {
			t.Fatal(err)
		}
		test.change(&changed)
		digest, err := mobileDigest(changed)
		if err != nil || digest == confirmed {
			t.Fatalf("%s change suppressed: err=%v", test.name, err)
		}
	}
}
