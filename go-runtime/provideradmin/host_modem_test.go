package provideradmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type hostSettingsStub struct {
	calls   int
	request HostModemRequest
}

func (*hostSettingsStub) HostModemSettings(context.Context) (HostModemSnapshot, error) {
	return HostModemSnapshot{Revision: strings.Repeat("a", 64), Settings: HostModemSettings{Backend: "auto"}, RuntimeState: "not_observed"}, nil
}
func (stub *hostSettingsStub) SaveHostModemSettings(_ context.Context, input HostModemRequest) (HostModemSnapshot, error) {
	stub.calls++
	stub.request = input
	if input.ExpectedRevision != strings.Repeat("a", 64) {
		return HostModemSnapshot{}, &Error{Status: http.StatusPreconditionFailed, Code: "host_configuration_changed"}
	}
	return HostModemSnapshot{Revision: strings.Repeat("b", 64), Settings: input.Settings, RuntimeState: "not_observed"}, nil
}

func TestHostModemHTTPPreservesSettingsAndUnconfirmedRuntime(t *testing.T) {
	stub := &hostSettingsStub{}
	handler := HostModemHandler(stub)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, HostModemPath, nil))
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid settings read")
	}
	input := HostModemRequest{ExpectedRevision: strings.Repeat("a", 64), Settings: HostModemSettings{Backend: "serial", Profiles: []HostModemProfile{{Name: "EC25", VID: "2c7c", PID: "0125"}}}}
	payload, _ := json.Marshal(input)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, HostModemPath, strings.NewReader(string(payload))))
	var result HostModemSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || stub.calls != 1 || result.RuntimeState != "not_observed" || len(result.Settings.Profiles) != 1 || result.Settings.Backend != "serial" {
		t.Fatal("settings contract lost data or invented applied state")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, HostModemPath, strings.NewReader(`{"expected_revision":"stale","settings":{"modem_backend":"auto"}}`)))
	if response.Code != 412 || !strings.Contains(response.Body.String(), "host_configuration_changed") {
		t.Fatal("revision conflict hidden")
	}
}

func TestHostModemHTTPRejectsExtraFieldsBeforeService(t *testing.T) {
	stub := &hostSettingsStub{}
	handler := HostModemHandler(stub)
	for _, body := range []string{`{"unit":"ssh.service"}`, `{} {}`, `{"settings":{"modem_backend":"serial","token":"fixture"}}`} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, HostModemPath, strings.NewReader(body)))
		if response.Code != 400 || stub.calls != 0 {
			t.Fatal("unexpected request reached service", response.Code)
		}
	}
}
