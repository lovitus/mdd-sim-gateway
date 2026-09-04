package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

type maintenanceStub struct {
	begin   bool
	request providerapply.DrainRequest
}

func (stub *maintenanceStub) Snapshot(context.Context) (providerapply.Snapshot, error) {
	return providerapply.Snapshot{SchemaVersion: 1, CatalogRevision: 7}, nil
}

func (stub *maintenanceStub) Request(_ context.Context, request providerapply.DrainRequest, begin bool) (providerapply.DrainResult, error) {
	stub.begin, stub.request = begin, request
	return providerapply.DrainResult{SchemaVersion: 1, CatalogRevision: request.CatalogRevision, LeaseID: request.LeaseID, Ready: true, Code: "drained"}, nil
}

func TestSystemMaintenanceHandlerForwardsTypedDrainRequest(t *testing.T) {
	stub := &maintenanceStub{}
	handler, err := NewSystemMaintenanceHandler(stub)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"action":"begin","request":{"schema_version":1,"catalog_revision":7,"lease_id":"maintenance-lease-1","line_ids":["line-1"]}}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/system/maintenance", strings.NewReader(payload)))
	if response.Code != http.StatusOK || !stub.begin || stub.request.CatalogRevision != 7 || stub.request.LeaseID != "maintenance-lease-1" {
		t.Fatalf("status=%d body=%s request=%+v", response.Code, response.Body.String(), stub.request)
	}
}

func TestSystemMaintenanceHandlerRejectsUnknownAction(t *testing.T) {
	handler, _ := NewSystemMaintenanceHandler(&maintenanceStub{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/system/maintenance", strings.NewReader(`{"action":"restart","request":{}}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_maintenance_request") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSystemMaintenanceHandlerReadsTypedStatus(t *testing.T) {
	handler, _ := NewSystemMaintenanceHandler(&maintenanceStub{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/system/maintenance", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"catalog_revision":7`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
