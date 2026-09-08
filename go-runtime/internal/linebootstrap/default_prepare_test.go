package linebootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type preparingFacts struct {
	mutableFacts
	calls int
	fail  bool
}

func TestDraftIdentityCompletionPreservesUserFieldsAndStoppedState(t *testing.T) {
	line := linecatalog.Line{ID: "draft", CardID: "8944100000000000001", HardwareProvisionState: "draft",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", SMSC: "+441234567890"}}
	observed := ObservedIdentity{IMSI: line.SIM.IMSI, MCC: "234", MNC: "10", IMEI: "862547055201716", SMSC: "+449999999999"}
	completed, changed := completeDraftIdentity(line, observed)
	if !changed || completed.Enabled || completed.HardwareProvisionState != "draft" || completed.SIM.MNC != "10" || completed.SIM.SMSC != line.SIM.SMSC {
		t.Fatal("draft completion overwrote user fields or activated line")
	}
	if _, changed := completeDraftIdentity(completed, observed); changed {
		t.Fatal("unchanged observation causes repeated saves")
	}
	observed.IMSI = "234200000000002"
	if _, changed := completeDraftIdentity(line, observed); changed {
		t.Fatal("different IMSI completed draft")
	}
	line.Enabled = true
	observed.IMSI = line.SIM.IMSI
	if _, changed := completeDraftIdentity(line, observed); changed {
		t.Fatal("enabled line modified by automatic completion")
	}
}

func (facts *preparingFacts) ExecuteModemPolicyCommand(_ context.Context, command agentlink.ModemPolicyCommand) (agentlink.ModemPolicyResponse, error) {
	facts.calls++
	if facts.fail {
		return agentlink.ModemPolicyResponse{}, errors.New("lost response")
	}
	ready := true
	return agentlink.ModemPolicyResponse{OperationID: command.OperationID, SIMAPDUReady: &ready}, nil
}

func TestAutomaticPreparationIsNotRepeatedAfterRestartOrUnknownResult(t *testing.T) {
	for _, fail := range []bool{false, true} {
		now := time.Now().UTC()
		key := []byte("01234567890123456789012345678901")
		template, err := (agentlink.DeviceDefaults{Authority: "core-a", Revision: 1, VoWiFiEnabled: true}).Authorize(key)
		if err != nil {
			t.Fatal(err)
		}
		status := modemStatus(now, "agent-a", "process-a", "attachment-a", "862547055201716", "8944100000000000001", "session-a")
		status.Topology.Modems[0].Policy = &agentlink.ModemPolicyFact{Revision: 1, Enrollment: &agentlink.DeviceEnrollment{FirstSeen: now, Initialized: true, Template: &template}}
		facts := &preparingFacts{mutableFacts: mutableFacts{statuses: []agentlink.ConnectionStatus{status}}, fail: fail}
		catalog := testCatalog(t)
		service, _ := New(catalog, facts, func() time.Time { return now })
		for range 3 {
			if err := service.ReconcileDefaultDrafts(context.Background(), "core-a", key); err != nil {
				t.Fatal(err)
			}
		}
		if facts.calls != 1 {
			t.Fatal("preparation repeated", facts.calls)
		}
		restarted, _ := New(catalog, facts, func() time.Time { return now })
		if err := restarted.ReconcileDefaultDrafts(context.Background(), "core-a", key); err != nil {
			t.Fatal(err)
		}
		if facts.calls != 1 {
			t.Fatal("recreated coordinator repeated preparation")
		}
	}
}
