package core

import (
	"context"
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

type systemManagedPolicyRuntime struct{ saves int }

func (runtime *systemManagedPolicyRuntime) ExecuteModemPolicyCommand(_ context.Context,
	command agentlink.ModemPolicyCommand) (agentlink.ModemPolicyResponse, error) {
	if command.Action == agentlink.ModemPolicyProfileSave {
		runtime.saves++
	}
	return agentlink.ModemPolicyResponse{OperationID: command.OperationID,
		AttachmentID: "attachment-a", EquipmentID: command.EquipmentID, CardID: command.CardID,
		SIMSessionGeneration: "sim-session",
		Policy: &agentlink.ModemPolicyFact{SchemaVersion: 1, EquipmentID: command.EquipmentID,
			CardID: command.CardID, Revision: 1, Persisted: true,
			ProfileMode: "system_managed", State: "ready", Code: "policy_ready"},
		Profiles: []agentlink.ModemProfileView{},
	}, nil
}

func TestSystemManagedDeviceProfilesAreReadOnlyBeforeAgentMutation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	line := deviceTestLine("line-a", "8944100000000000001", "862547055201716")
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	status := deviceModemStatus(now, "agent-a", "process-a", "attachment-a", line.SIM.IMEI, line.CardID)
	status.Topology.Modems[0].Policy = &agentlink.ModemPolicyFact{
		SchemaVersion: 1, EquipmentID: line.SIM.IMEI, CardID: line.CardID,
		Revision: 1, Persisted: true, ProfileMode: "system_managed", State: "ready", Code: "policy_ready",
	}
	facts := fixedAgentFacts{statuses: []agentlink.ConnectionStatus{status}}
	runtime := &systemManagedPolicyRuntime{}
	server := NewServer(testReplay(t, now), func() time.Time { return now }, WithAgentFacts(facts),
		WithLineCatalog(catalog, linecatalog.NewHandler(catalog)), WithModemPolicies(runtime))

	devices := httptest.NewRecorder()
	server.ServeHTTP(devices, httptest.NewRequest(http.MethodGet, "/v1/devices", nil))
	var inventory DeviceSnapshot
	if devices.Code != http.StatusOK || json.Unmarshal(devices.Body.Bytes(), &inventory) != nil || len(inventory.Devices) != 1 {
		t.Fatalf("devices=%d %s", devices.Code, devices.Body.String())
	}
	path := "/v1/devices/" + inventory.Devices[0].ID + "/profiles"
	get := httptest.NewRecorder()
	server.ServeHTTP(get, httptest.NewRequest(http.MethodGet, path, nil))
	var view struct {
		Device devicePolicyView `json:"device"`
	}
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &view) != nil ||
		view.Device.Policy.ProfileMode != "system_managed" {
		t.Fatalf("GET profiles=%d %s", get.Code, get.Body.String())
	}
	putRequest := httptest.NewRequest(http.MethodPut, path, strings.NewReader(
		`{"operation_id":"profile-save","name":"carrier","apn":"internet","auth":"NONE","password_set":true,"password":"secret"}`))
	putRequest.Header.Set("If-Match", `"1"`)
	put := httptest.NewRecorder()
	server.ServeHTTP(put, putRequest)
	var failure map[string]string
	_ = json.Unmarshal(put.Body.Bytes(), &failure)
	if put.Code != http.StatusUnprocessableEntity || failure["code"] != "modem_profile_system_managed" || runtime.saves != 0 {
		t.Fatalf("PUT profiles=%d body=%s saves=%d", put.Code, put.Body.String(), runtime.saves)
	}
}
