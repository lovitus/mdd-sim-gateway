package rawmodem

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func TestBindingHandlerResolvesLiveExactCandidateAndDisablesByCAS(t *testing.T) {
	store, line := rawHandlerStore(t)
	defer store.Close()
	now := time.Now().UTC()
	agents := &bindingTestAgents{statuses: rawTestStatuses(now, linecatalog.RawModemBinding{
		SourceAgentID: "source-agent", ImporterAgentID: "importer-agent",
		EquipmentID: line.SIM.IMEI, CardID: line.CardID,
	})}
	wakes := 0
	handler, err := NewHandler(store, agents, func() { wakes++ }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	view := putBinding(t, handler, line.ID, bindingRequest{
		ExpectedRevision: 1, ExpectedEquipmentID: line.SIM.IMEI, ExpectedCardID: line.CardID,
		Enabled: true, SourceAgentID: "source-agent", ImporterAgentID: "importer-agent",
	}, http.StatusOK)
	if wakes != 1 || view.Revision != 2 || view.Binding == nil || !view.Binding.Enabled ||
		view.Binding.EquipmentID != line.SIM.IMEI || view.Binding.CardID != line.CardID || view.Binding.Epoch != 2 {
		t.Fatalf("view=%+v wakes=%d", view, wakes)
	}
	view = putBinding(t, handler, line.ID, bindingRequest{
		ExpectedRevision: 2, ExpectedEquipmentID: line.SIM.IMEI, ExpectedCardID: line.CardID,
		Enabled: true, SourceAgentID: "source-agent", ImporterAgentID: "importer-agent",
	}, http.StatusOK)
	if wakes != 1 || view.Revision != 2 || view.Binding == nil || view.Binding.Epoch != 2 {
		t.Fatalf("idempotent view=%+v wakes=%d", view, wakes)
	}
	view = putBinding(t, handler, line.ID, bindingRequest{
		ExpectedRevision: 2, ExpectedEquipmentID: line.SIM.IMEI, ExpectedCardID: line.CardID, Enabled: false,
	}, http.StatusOK)
	if wakes != 2 || view.Revision != 3 || view.Binding == nil || view.Binding.Enabled || view.Binding.Epoch != 3 {
		t.Fatalf("disabled view=%+v wakes=%d", view, wakes)
	}
}

func TestBindingHandlerRejectsSelectedAgentWithoutExactLiveIdentity(t *testing.T) {
	store, line := rawHandlerStore(t)
	defer store.Close()
	now := time.Now().UTC()
	statuses := rawTestStatuses(now, linecatalog.RawModemBinding{
		SourceAgentID: "source-agent", ImporterAgentID: "importer-agent",
		EquipmentID: "867530900000099", CardID: line.CardID,
	})
	handler, err := NewHandler(store, &bindingTestAgents{statuses: statuses}, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	putBinding(t, handler, line.ID, bindingRequest{
		ExpectedRevision: 1, ExpectedEquipmentID: line.SIM.IMEI, ExpectedCardID: line.CardID,
		Enabled: true, SourceAgentID: "source-agent", ImporterAgentID: "importer-agent",
	}, http.StatusConflict)
	snapshot, err := store.RawModemBindings()
	if err != nil || snapshot.Revision != 1 || len(snapshot.Bindings) != 0 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestBindingHandlerRejectsStaleEnableAndCanDisableStoredBindingAfterLineIdentityChanges(t *testing.T) {
	store, line := rawHandlerStore(t)
	defer store.Close()
	now := time.Now().UTC()
	agents := &bindingTestAgents{statuses: rawTestStatuses(now, linecatalog.RawModemBinding{
		SourceAgentID: "source-agent", ImporterAgentID: "importer-agent",
		EquipmentID: line.SIM.IMEI, CardID: line.CardID,
	})}
	wakes := 0
	handler, err := NewHandler(store, agents, func() { wakes++ }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	putBinding(t, handler, line.ID, bindingRequest{
		ExpectedRevision: 1, ExpectedEquipmentID: line.SIM.IMEI, ExpectedCardID: line.CardID,
		Enabled: true, SourceAgentID: "source-agent", ImporterAgentID: "importer-agent",
	}, http.StatusOK)

	previousEquipment, previousCard := line.SIM.IMEI, line.CardID
	line.SIM.IMEI, line.CardID = "867530900000002", "8944100000000000002"
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	putBinding(t, handler, line.ID, bindingRequest{
		ExpectedRevision: 2, ExpectedEquipmentID: previousEquipment, ExpectedCardID: previousCard,
		Enabled: true, SourceAgentID: "source-agent", ImporterAgentID: "importer-agent",
	}, http.StatusConflict)
	view := putBinding(t, handler, line.ID, bindingRequest{
		ExpectedRevision: 2, ExpectedEquipmentID: previousEquipment, ExpectedCardID: previousCard,
		Enabled: false,
	}, http.StatusOK)
	if wakes != 2 || view.Binding == nil || view.Binding.Enabled || view.Binding.EquipmentID != previousEquipment ||
		view.Binding.CardID != previousCard || view.EquipmentID != line.SIM.IMEI || view.CardID != line.CardID {
		t.Fatalf("view=%+v wakes=%d", view, wakes)
	}
}

type bindingTestAgents struct{ statuses []agentlink.ConnectionStatus }

func (agents *bindingTestAgents) Statuses() []agentlink.ConnectionStatus { return agents.statuses }

func rawHandlerStore(t *testing.T) (*linecatalog.Store, linecatalog.Line) {
	t.Helper()
	store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	line := linecatalog.Line{
		SchemaVersion: linecatalog.SchemaVersion, ID: "line-raw", Enabled: true,
		CardID: "8944100000000000001",
		SIM: linecatalog.SIMConfig{
			IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: "867530900000001",
		},
	}
	if _, err := store.Put(line); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, line
}

func putBinding(t *testing.T, handler *Handler, lineID string, input bindingRequest, wantStatus int) bindingView {
	t.Helper()
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/v1/lines/"+lineID+"/raw-modem", bytes.NewReader(payload))
	request.SetPathValue("lineID", lineID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if wantStatus != http.StatusOK {
		return bindingView{}
	}
	var view bindingView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}
