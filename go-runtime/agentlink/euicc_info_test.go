package agentlink

import "testing"

func TestEUICCInformationAvailabilityAndClone(t *testing.T) {
	fact := &EUICCFact{EID: "89049032000000000000000000000001", ProfilesAvailable: true, Profiles: []EUICCProfileFact{}, Info: &EUICCInfoFact{AddressesAvailable: true, DefaultSMDPAddress: "rsp.example", MemoryAvailable: true}}
	if err := validateEUICC(fact); err != nil {
		t.Fatal(err)
	}
	copy := cloneEUICC(fact)
	copy.Info.DefaultSMDPAddress = "changed.example"
	if fact.Info.DefaultSMDPAddress != "rsp.example" {
		t.Fatal("clone aliases metadata")
	}
	fact.Info.AddressesAvailable = false
	if validateEUICC(fact) == nil {
		t.Fatal("unavailable address carried a value")
	}
	fact.Info = &EUICCInfoFact{FreeNVMBytes: 1}
	if validateEUICC(fact) == nil {
		t.Fatal("unavailable memory carried a value")
	}
	fact.Info = nil
	if err := validateEUICC(fact); err != nil {
		t.Fatal("old Agent metadata compatibility lost", err)
	}
}
