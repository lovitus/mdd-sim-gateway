package core

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func TestOfflineDeviceHidePersistsAndHeartbeatRestoresWithoutLosingIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presentation.db")
	store, err := OpenDevicePresentation(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	now := time.Now().UTC()
	live := DeviceSnapshot{At: now, Devices: []DeviceProjection{{ID: "reader-a", Kind: "reader", AgentID: "agent-a", ProcessGeneration: "process-a", Condition: "ready",
		Reader:    &agentlink.ReaderFact{ReaderName: "reader", CardID: "8985200000000000001", CardPresent: true},
		Endpoints: []EndpointProjection{{ID: "endpoint-a", OperationCandidate: true}},
	}}}
	first, err := store.project(live)
	if err != nil || len(first.Devices) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	version := first.Devices[0].ObservationVersion
	if len(version) != 64 || first.Devices[0].ObservedOnly {
		t.Fatal("missing live observation identity")
	}
	if err := store.hide("reader-a", version); !errors.Is(err, ErrDevicePresentationChanged) {
		t.Fatalf("live hide=%v", err)
	}
	empty := DeviceSnapshot{At: now.Add(time.Second), Devices: []DeviceProjection{}}
	offline, err := store.project(empty)
	if err != nil || len(offline.Devices) != 1 {
		t.Fatalf("offline=%+v err=%v", offline, err)
	}
	device := offline.Devices[0]
	if !device.ObservedOnly || device.Condition != "offline" || device.ProcessGeneration != "" || device.Endpoints[0].OperationCandidate || device.Reader.CardID != live.Devices[0].Reader.CardID {
		t.Fatalf("unsafe offline view: %+v", device)
	}
	if err := store.hide("reader-a", "wrong"); !errors.Is(err, ErrDevicePresentationChanged) {
		t.Fatalf("stale hide=%v", err)
	}
	if err := store.hide("reader-a", version); err != nil {
		t.Fatal(err)
	}
	hidden, err := store.project(empty)
	if err != nil || len(hidden.Devices) != 0 {
		t.Fatalf("hidden=%+v err=%v", hidden, err)
	}
	if data, err := store.Backup(); err != nil || len(data) == 0 {
		t.Fatalf("backup bytes=%d err=%v", len(data), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDevicePresentation(path)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err = store.project(empty)
	if err != nil || len(hidden.Devices) != 0 {
		t.Fatal("hidden marker did not survive restart")
	}
	restored, err := store.project(live)
	if err != nil || len(restored.Devices) != 1 || restored.Devices[0].ObservedOnly || !restored.Devices[0].Endpoints[0].OperationCandidate || restored.Devices[0].Reader.CardID != live.Devices[0].Reader.CardID {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
}
