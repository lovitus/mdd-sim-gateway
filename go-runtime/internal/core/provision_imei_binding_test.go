package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type provisionBindingRuntime struct {
	provisionRuntimeStub
	onResolve func()
}

func (stub *provisionBindingRuntime) ResolveModemTargetForAction(equipment, card string, action agentlink.ModemAction) (agentlink.ModemTarget, error) {
	if stub.onResolve != nil {
		stub.onResolve()
	}
	return stub.provisionRuntimeStub.ResolveModemTargetForAction(equipment, card, action)
}

func TestProvisionRetainsClaimedIMEIBinding(t *testing.T) {
	for _, scenario := range []string{"conflicting-command", "matching-command", "concurrent-rebind"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			const card = "89010000000000000001"
			const first = "356789012345678"
			const second = "356789012345679"
			for i, e := range []linecatalog.IMEIPoolEntry{{ID: "first", Name: "First", IMEI: first}, {ID: "second", Name: "Second", IMEI: second}} {
				if _, _, _, err := store.PutIMEIPoolEntryExpected(e, uint64(i+1)); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, _, _, err := store.BindIMEICardExpected("first", card, 3, 1); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			receipt := linecatalog.OperationReceipt{SchemaVersion: 1, OperationID: "claim", Kind: linecatalog.OperationClaim, State: linecatalog.OperationPrepared,
				CreatedAt: now, UpdatedAt: now, RequestDigest: strings.Repeat("a", 64), AttemptCount: 1, LineID: "line-1", CardID: card, ExpectedCatalogRevision: 2}
			line, _, err := store.CreateExpectedWithOperation(linecatalog.Line{SchemaVersion: 1, ID: "line-1", Name: "Fixture", CardID: card, HardwareProvisionState: "draft"}, 2, receipt)
			if err != nil || line.SIM.IMEI != first {
				t.Fatalf("claim=%+v err=%v", line, err)
			}
			stub := &provisionBindingRuntime{provisionRuntimeStub: provisionRuntimeStub{result: agentlink.ProvisionResponse{State: agentlink.ProvisionApplied}}}
			expectedIMEI := first
			if scenario == "concurrent-rebind" {
				expectedIMEI = second
				stub.onResolve = func() {
					// Happens after the initial Get, before the handler takes its catalog revision.
					if _, _, _, _, err := store.BindIMEIExpected("second", line.ID, card, 4, 3); err != nil {
						t.Fatal(err)
					}
				}
			}
			handler, err := NewProvisionHandler(stub, store)
			if err != nil {
				t.Fatal(err)
			}
			commandIMEI := first
			if scenario == "conflicting-command" {
				commandIMEI = second
			}
			command := agentlink.ProvisionCommand{OperationID: "provision-bound", LineID: line.ID, EquipmentID: "862547055201716", CardID: card,
				AttachmentID: "attach-1", SIMSessionGeneration: "session-1", IMSI: "460001234567890", MCC: "460", MNC: "01", IMEI: commandIMEI, SMSC: "+8613800138000"}
			wire, err := json.Marshal(command)
			if err != nil {
				t.Fatal(err)
			}
			payload := withProvisionPrecondition(t, store, string(wire))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/provision", strings.NewReader(payload)))
			if scenario == "matching-command" {
				if response.Code != http.StatusOK || stub.calls != 1 {
					t.Fatalf("matching provision: status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
				}
			} else if response.Code != http.StatusConflict || stub.calls != 0 || !strings.Contains(response.Body.String(), "provision_imei_binding_conflict") {
				t.Fatalf("binding conflict dispatched hardware: status=%d calls=%d body=%s", response.Code, stub.calls, response.Body.String())
			}
			got, err := store.Get(line.ID)
			if err != nil || got.SIM.IMEI != expectedIMEI || got.Enabled {
				t.Fatalf("provision lost binding or enabled line: %+v %v", got, err)
			}
			if scenario != "matching-command" {
				if got.HardwareProvisionState != "draft" {
					t.Fatalf("rejection changed draft: %+v", got)
				}
				if _, found, err := store.GetOperation(command.OperationID); err != nil || found {
					t.Fatalf("rejection created operation: %v %v", found, err)
				}
			}
		})
	}
}
