package core

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"testing"
)

func TestHostModemIdleChecksOwnedLinesAndPendingCardOperations(t *testing.T) {
	topology := agentlink.TopologySnapshot{ReaderCondition: agentlink.ReaderReady, ModemCondition: agentlink.ModemDisabled,
		Readers: []agentlink.ReaderFact{{ReaderName: "reader", CardPresent: true, CardID: "card-a"}}}
	lines := []linecatalog.Line{{ID: "owned", CardID: "card-a"}, {ID: "other", CardID: "card-b"}}
	checks := []func(string) (bool, error){func(id string) (bool, error) { return id == "owned", nil }}
	if got := hostModemIdleCode(topology, lines, checks); got != "host_line_busy" {
		t.Fatal("active owned line admitted", got)
	}
	checks[0] = func(id string) (bool, error) { return id == "other", nil }
	if got := hostModemIdleCode(topology, lines, checks); got != "" {
		t.Fatal("unrelated line blocked host", got)
	}
	topology.Readers[0].EUICC = &agentlink.EUICCFact{Download: &agentlink.EUICCDownloadFact{Job: agentlink.EUICCDownloadJob{State: agentlink.EUICCDownloadRunning}}}
	if got := hostModemIdleCode(topology, lines, checks); got != "host_euicc_operation_active" {
		t.Fatal("active download admitted", got)
	}
	topology.Readers[0].EUICC.Download.Job.State = agentlink.EUICCDownloadCompleted
	if got := hostModemIdleCode(topology, lines, checks); got != "" {
		t.Fatal("completed download blocks forever", got)
	}
	topology.Readers[0].CardID = ""
	if got := hostModemIdleCode(topology, lines, checks); got != "host_card_identity_unconfirmed" {
		t.Fatal("unknown current card admitted", got)
	}
}
