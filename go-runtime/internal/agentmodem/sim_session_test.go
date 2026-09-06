package agentmodem

import "testing"

func TestSIMInsertionTrackerFencesContinuityWithoutMutatingInput(t *testing.T) {
	tracker, err := NewSIMInsertionTracker()
	if err != nil {
		t.Fatal(err)
	}
	signal := uint32(61)
	source := []Fact{{
		AttachmentID: "attachment-a", EquipmentID: "equipment-a", ContinuityEpoch: "usb-epoch-1",
		SIM:     SIMFact{State: SIMReady, ICCID: "card-a", MSISDNs: []string{"+441"}},
		Network: NetworkFact{SignalPercent: &signal},
	}}
	first := tracker.Observe(source)
	second := tracker.Observe(source)
	if first[0].SIM.SessionGeneration == "" || first[0].SIM.SessionGeneration != second[0].SIM.SessionGeneration {
		t.Fatalf("generation was not stable: first=%q second=%q", first[0].SIM.SessionGeneration, second[0].SIM.SessionGeneration)
	}
	first[0].SIM.MSISDNs[0] = "+44999"
	*first[0].Network.SignalPercent = 1
	if source[0].SIM.SessionGeneration != "" || source[0].SIM.MSISDNs[0] != "+441" || *source[0].Network.SignalPercent != 61 {
		t.Fatalf("input was mutated: %+v", source[0])
	}

	changedEpoch := []Fact{source[0]}
	changedEpoch[0].ContinuityEpoch = "usb-epoch-2"
	afterEpoch := tracker.Observe(changedEpoch)
	if afterEpoch[0].SIM.SessionGeneration == second[0].SIM.SessionGeneration {
		t.Fatal("platform continuity change reused the old generation")
	}

	replaced := []Fact{source[0]}
	replaced[0].SIM.ICCID = "card-b"
	afterCard := tracker.Observe(replaced)
	if afterCard[0].SIM.SessionGeneration == afterEpoch[0].SIM.SessionGeneration {
		t.Fatal("replacement card reused the old generation")
	}
}

func TestSIMInsertionTrackerRetiresAuthoritativeAbsenceAndNonReady(t *testing.T) {
	tracker, err := NewSIMInsertionTracker()
	if err != nil {
		t.Fatal(err)
	}
	ready := []Fact{{AttachmentID: "attachment-a", EquipmentID: "equipment-a", ContinuityEpoch: "epoch-a",
		SIM: SIMFact{State: SIMReady, ICCID: "card-a"}}}
	first := tracker.Observe(ready)
	tracker.Observe(nil)
	reinserted := tracker.Observe(ready)
	if reinserted[0].SIM.SessionGeneration == first[0].SIM.SessionGeneration {
		t.Fatal("authoritative absence reused the old generation")
	}

	nonReady := []Fact{ready[0]}
	nonReady[0].SIM.State = SIMAbsent
	if observed := tracker.Observe(nonReady); observed[0].SIM.SessionGeneration != "" {
		t.Fatalf("non-ready SIM received generation %q", observed[0].SIM.SessionGeneration)
	}
	afterNonReady := tracker.Observe(ready)
	if afterNonReady[0].SIM.SessionGeneration == reinserted[0].SIM.SessionGeneration {
		t.Fatal("non-ready transition reused the old generation")
	}
}

func TestSIMInsertionTrackerFailsClosedWithoutEpochOrUniqueAttachment(t *testing.T) {
	tracker, err := NewSIMInsertionTracker()
	if err != nil {
		t.Fatal(err)
	}
	missingEpoch := tracker.Observe([]Fact{{AttachmentID: "attachment-a", EquipmentID: "equipment-a",
		SIM: SIMFact{State: SIMReady, ICCID: "card-a"}}})
	if missingEpoch[0].SIM.SessionGeneration != "" {
		t.Fatal("ready SIM without continuity epoch was admitted")
	}
	duplicate := tracker.Observe([]Fact{
		{AttachmentID: "attachment-a", EquipmentID: "equipment-a", ContinuityEpoch: "epoch-a", SIM: SIMFact{State: SIMReady, ICCID: "card-a"}},
		{AttachmentID: "attachment-a", EquipmentID: "equipment-b", ContinuityEpoch: "epoch-b", SIM: SIMFact{State: SIMReady, ICCID: "card-b"}},
	})
	if duplicate[0].SIM.SessionGeneration != "" || duplicate[1].SIM.SessionGeneration != "" {
		t.Fatal("duplicate attachment received a session generation")
	}
}

func TestSIMInsertionTrackerRotatesAfterPartialIdentityObservation(t *testing.T) {
	tracker, err := NewSIMInsertionTracker()
	if err != nil {
		t.Fatal(err)
	}
	ready := []Fact{{AttachmentID: "attachment-a", EquipmentID: "equipment-a", ContinuityEpoch: "epoch-a",
		SIM: SIMFact{State: SIMReady, ICCID: "card-a"}}}
	first := tracker.Observe(ready)
	partial := []Fact{ready[0]}
	partial[0].ContinuityEpoch = ""
	partial[0].Condition = DeviceDegraded
	unknown := tracker.Observe(partial)
	if unknown[0].SIM.SessionGeneration != "" || !unknown[0].SessionGenerationAuthority {
		t.Fatalf("partial identity was made operable: %+v", unknown[0])
	}
	recovered := tracker.Observe(ready)
	if recovered[0].SIM.SessionGeneration == "" || recovered[0].SIM.SessionGeneration == first[0].SIM.SessionGeneration {
		t.Fatalf("recovered identity reused old generation: first=%q recovered=%q",
			first[0].SIM.SessionGeneration, recovered[0].SIM.SessionGeneration)
	}
}

func TestSIMInsertionTrackerAuxiliaryProjectionCannotMutateContinuity(t *testing.T) {
	tracker, err := NewSIMInsertionTracker()
	if err != nil {
		t.Fatal(err)
	}
	ready := []Fact{{AttachmentID: "attachment-a", EquipmentID: "equipment-a", ContinuityEpoch: "epoch-a",
		SIM: SIMFact{State: SIMReady, ICCID: "card-a"}}}
	first := tracker.Observe(ready)
	if projected := tracker.Project(ready); projected[0].SIM.SessionGeneration != first[0].SIM.SessionGeneration {
		t.Fatalf("projection lost current generation: %+v", projected[0].SIM)
	}
	replaced := []Fact{ready[0]}
	replaced[0].SIM.ICCID = "card-b"
	if projected := tracker.Project(replaced); projected[0].SIM.SessionGeneration != "" {
		t.Fatalf("replacement card received projected generation: %+v", projected[0].SIM)
	}
	tracker.Project(nil)
	after := tracker.Observe(ready)
	if after[0].SIM.SessionGeneration != first[0].SIM.SessionGeneration {
		t.Fatal("auxiliary projections mutated the insertion tracker")
	}
}
