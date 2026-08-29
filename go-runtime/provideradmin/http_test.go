package provideradmin

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeService struct {
	status Status
	result ApplyResult
	rev    uint64
}

func (service *fakeService) Status(context.Context) (Status, error) { return service.status, nil }
func (service *fakeService) Apply(_ context.Context, revision uint64) (ApplyResult, error) {
	service.rev = revision
	return service.result, nil
}

func TestHandlerRequiresStrictApplyRequest(t *testing.T) {
	service := &fakeService{result: ApplyResult{SchemaVersion: 1, CatalogRevision: 7, State: "applied"}}
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(`{"schema_version":1,"catalog_revision":7}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.rev != 7 {
		t.Fatalf("status=%d revision=%d body=%s", response.Code, service.rev, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, Path, strings.NewReader(`{"schema_version":1,"catalog_revision":7,"extra":true}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthenticatedUnixClientRoundTrip(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "mdd-provideradmin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "apply.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	service := &fakeService{
		status: Status{SchemaVersion: 1, CatalogRevision: 9, AppliedRevision: 8, Pending: true},
		result: ApplyResult{SchemaVersion: 1, CatalogRevision: 9, ApplyID: "apply-1", State: "applied"},
	}
	handler, _ := NewHandler(service)
	authenticated, err := Authenticate(handler, "01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: authenticated}
	defer server.Close()
	go server.Serve(listener)

	client, err := NewClient(socket, "01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil || !status.Pending || status.CatalogRevision != 9 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	result, err := client.Apply(context.Background(), 9)
	if err != nil || result.ApplyID != "apply-1" || service.rev != 9 {
		t.Fatalf("result=%+v revision=%d err=%v", result, service.rev, err)
	}

	badClient, _ := NewClient(socket, "abcdefghijklmnopqrstuvwxyz123456")
	if _, err := badClient.Status(context.Background()); err == nil {
		t.Fatal("invalid local token was accepted")
	}
}
