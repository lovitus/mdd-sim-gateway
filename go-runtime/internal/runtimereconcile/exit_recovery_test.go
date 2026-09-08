package runtimereconcile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type peerRuntimeEvidence struct {
	RuntimeControl
	status vowifiipc.Snapshot
	fence  mediaauth.ProviderFence
	err    error
}

func (runtime *peerRuntimeEvidence) Observe(context.Context, string) (vowifiipc.Snapshot, mediaauth.ProviderFence, error) {
	return runtime.status, runtime.fence, runtime.err
}

func exitObserverFixture(t *testing.T, pinned bool) (*Reconciler, linecatalog.Snapshot, linecatalog.Line, lineObservation) {
	t.Helper()
	r, catalog, runtime, _, _, clock := testReconciler(t, vowifiipc.RuntimeFailed, oneCard())
	line, err := catalog.Get("line-1")
	if err != nil {
		t.Fatal(err)
	}
	line.Network.EgressCountry = "gb"
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := catalog.SetRuntimeIntent(line.ID, true); err != nil {
		t.Fatal(err)
	}
	lines, err := catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	line, err = catalog.Get(line.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	exits, err := egressconfig.Open(filepath.Join(root, "egress.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exits.Close() })
	exit := egressconfig.Exit{Enabled: true, ProfileID: "feed"}
	if pinned {
		exit.PinnedNode, exit.PinMode = "node-a", "lock"
	}
	err = exits.ImportEmpty(egressconfig.Config{SchemaVersion: egressconfig.SchemaVersion, Enabled: true,
		Profiles: map[string]egressconfig.Profile{"feed": {Name: "fixture", Type: "subscription", URL: "https://example.invalid/feed"}},
		Exits:    map[string]egressconfig.Exit{"gb": exit}}, egressconfig.ImportReceipt{SourceSHA256: strings.Repeat("a", 64), ImportedAt: clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := exits.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	desired, err := egressdesired.Render(saved, lines, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	desiredPath, statusPath := filepath.Join(root, "desired.json"), filepath.Join(root, "status.json")
	if _, err := egressdesired.Publish(desiredPath, desired); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(egressstatus.Snapshot{DesiredGeneration: desired.Generation, Exits: map[string]egressstatus.Exit{
		"gb": {Ready: true, Mode: "subscription", Node: "node-a", Candidates: []string{"node-a", "node-b"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	r.exitRecovery = &ExitRecoveryConfig{Store: r.store.(*events.BoltStore), Config: exits, DesiredPath: desiredPath, StatusPath: statusPath}
	snapshot := vowifiipc.Snapshot{SchemaVersion: vowifiipc.SchemaVersion, LineID: line.ID, ProviderID: runtime.fence.ProviderID,
		ProcessGeneration: runtime.fence.Generation, Sequence: 1, ObservedAt: clock.Now(),
		Runtime: vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeFailed, Code: "swu_open_failed", FailureID: strings.Repeat("a", 64),
			IKE: &vowifiipc.IKEExchangeEvidence{RequestsSent: 2, ResponseTimeouts: 2}},
		Tunnel: vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: "swu_open_failed"},
		IMS:    vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped}, Voice: vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped},
		Messaging: vowifiipc.LayerStatus{Condition: vowifiipc.LayerStopped}}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	return r, lines, line, lineObservation{intentFound: true, intentEnabled: true, providerReady: true, cardMatches: 1, fence: runtime.fence, status: snapshot}
}

func TestExitObserverPersistsOnceAndClosesHealedCampaign(t *testing.T) {
	r, catalog, line, observation := exitObserverFixture(t, false)
	for i := 0; i < 3; i++ {
		observation.status.Sequence = uint64(i + 1)
		observation.status.Runtime.FailureID = strings.Repeat(string(rune('a'+i)), 64)
		if err := r.observeExitRecovery(t.Context(), catalog, line, observation); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := r.exitRecovery.Store.ExitRecovery(line.ID)
	if err != nil || stored.Ledger.Failures != 3 || stored.Ledger.LastDecision != recovery.ExitSwitch {
		t.Fatal(stored, err)
	}
	resumed, err := New(Config{Context: t.Context(), Catalog: r.catalog, Agents: r.agents, Runtime: r.runtime, Store: r.store,
		Replay: r.replay, Now: r.now, ExitRecovery: r.exitRecovery})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Close)
	if err := resumed.observeExitRecovery(t.Context(), catalog, line, observation); err != nil {
		t.Fatal(err)
	}
	duplicate, _ := r.exitRecovery.Store.ExitRecovery(line.ID)
	if duplicate.Revision != stored.Revision {
		t.Fatal("new Core recounted the same failure")
	}
	old := observation
	old.status.Sequence = 2
	old.status.Runtime.FailureID = strings.Repeat("b", 64)
	if err := r.observeExitRecovery(t.Context(), catalog, line, old); err != nil {
		t.Fatal(err)
	}
	duplicate, _ = r.exitRecovery.Store.ExitRecovery(line.ID)
	if duplicate.Revision != stored.Revision {
		t.Fatal("old distinct failure was counted")
	}
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	healthy := observation
	healthy.status.Sequence = 4
	healthy.status.Runtime = vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning, Code: "ready"}
	healthy.status.Tunnel, healthy.status.IMS, healthy.status.Voice, healthy.status.Messaging = ready, ready, ready, ready
	r.mu.Lock()
	state := r.lineLocked(line.ID)
	state.healthySince, state.healthyFence = r.now().Add(-2*time.Minute), observation.fence
	r.mu.Unlock()
	if err := r.observeExitRecovery(t.Context(), catalog, line, healthy); err != nil {
		t.Fatal(err)
	}
	healed, _ := r.exitRecovery.Store.ExitRecovery(line.ID)
	if healed.Ledger.Failures != 0 || healed.Ledger.LastDecision != "" || healed.Ledger.LastSequence != 4 {
		t.Fatal(healed)
	}
	if err := r.observeExitRecovery(t.Context(), catalog, line, old); err != nil {
		t.Fatal(err)
	}
	duplicate, _ = r.exitRecovery.Store.ExitRecovery(line.ID)
	if duplicate.Revision != healed.Revision {
		t.Fatal("old outage revived after healing")
	}
}

func TestExitObserverRespectsPinnedAndUnknownRuntime(t *testing.T) {
	r, catalog, line, observation := exitObserverFixture(t, true)
	for i := 0; i < 3; i++ {
		observation.status.Sequence = uint64(i + 1)
		observation.status.Runtime.FailureID = strings.Repeat(string(rune('a'+i)), 64)
		if err := r.observeExitRecovery(t.Context(), catalog, line, observation); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := r.exitRecovery.Store.ExitRecovery(line.ID)
	if err != nil || stored.Ledger.LastDecision != recovery.ExitGiveUp {
		t.Fatal(stored, err)
	}
	observation.status.Sequence = 4
	observation.status.Runtime.FailureID = strings.Repeat("d", 64)
	if err := os.WriteFile(r.exitRecovery.StatusPath, []byte(`{"desired_generation":"other","exits":{"gb":{"ready":true,"node":"node-a"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.observeExitRecovery(t.Context(), catalog, line, observation); err == nil {
		t.Fatal("unconfirmed egress generation accepted")
	}
	unchanged, _ := r.exitRecovery.Store.ExitRecovery(line.ID)
	if unchanged.Revision != stored.Revision {
		t.Fatal("unconfirmed exit changed ledger")
	}
}

func TestExitObserverProtectsHealthyPeerAndRejectsUnknownPeer(t *testing.T) {
	r, _, line, observation := exitObserverFixture(t, false)
	peer := line
	peer.ID, peer.CardID = "peer-line", "8944100000000000002"
	store := r.catalog.(*linecatalog.Store)
	if _, err := store.Put(peer); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.SetRuntimeIntent(peer.ID, true); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	desired, err := egressdesired.Read(r.exitRecovery.DesiredPath)
	if err != nil {
		t.Fatal(err)
	}
	desired.CatalogRevision = catalog.Revision
	if _, err := egressdesired.Publish(r.exitRecovery.DesiredPath, desired); err != nil {
		t.Fatal(err)
	}
	status := observation.status
	status.LineID, status.ProviderID, status.ProcessGeneration = peer.ID, "peer-provider", "peer-generation"
	status.Runtime = vowifiipc.RuntimeStatus{Condition: vowifiipc.RuntimeRunning, Code: "ready"}
	ready := vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true, Code: "ready"}
	status.Tunnel, status.IMS, status.Voice, status.Messaging = ready, ready, ready, ready
	runtime := &peerRuntimeEvidence{RuntimeControl: r.runtime, status: status,
		fence: mediaauth.ProviderFence{LineID: peer.ID, ProviderID: status.ProviderID, Generation: status.ProcessGeneration, CardID: peer.CardID}}
	r.runtime = runtime
	for i := 0; i < 6; i++ {
		observation.status.Sequence = uint64(i + 1)
		observation.status.Runtime.FailureID = strings.Repeat(string(rune('a'+i)), 64)
		if err := r.observeExitRecovery(t.Context(), catalog, line, observation); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := r.exitRecovery.Store.ExitRecovery(line.ID)
	if err != nil || stored.Ledger.Failures != 6 || stored.Ledger.LastDecision != recovery.ExitReport || !stored.Ledger.HeldForPeer || len(stored.Ledger.Tried) != 0 {
		t.Fatal("healthy peer did not protect shared exit", stored, err)
	}
	runtime.err = errors.New("peer observation unavailable")
	observation.status.Sequence = 7
	observation.status.Runtime.FailureID = strings.Repeat("a", 64)
	if err := r.observeExitRecovery(t.Context(), catalog, line, observation); err == nil {
		t.Fatal("unknown peer permitted decision")
	}
	unchanged, _ := r.exitRecovery.Store.ExitRecovery(line.ID)
	if unchanged.Revision != stored.Revision {
		t.Fatal("unknown peer changed recovery ledger")
	}
}
