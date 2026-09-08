package provideradmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type webFixture struct {
	snapshot WebSnapshot
	saves    int
}

func (value *webFixture) WebSettings(context.Context) (WebSnapshot, error) {
	return value.snapshot, nil
}
func (value *webFixture) SaveWebSettings(_ context.Context, input WebRequest) (WebSnapshot, error) {
	value.saves++
	value.snapshot.Settings = input.Settings
	return value.snapshot, nil
}

func TestWebHandlerReportsSavedAndActiveSettingsSeparately(t *testing.T) {
	active := WebSettings{Listen: "127.0.0.1:8443", TLSCert: "/cert", TLSKey: "/key"}
	service := &webFixture{snapshot: WebSnapshot{SchemaVersion: 1, Revision: strings.Repeat("a", 64), Settings: WebSettings{Listen: "127.0.0.1:9443", TLSCert: "/cert", TLSKey: "/key"}}}
	handler, err := NewWebHandler(service, &active)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, WebPath, nil))
	var snapshot WebSnapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || !snapshot.RestartRequired || snapshot.Active == nil || snapshot.Active.Listen != active.Listen {
		t.Fatal(w.Code, w.Body.String())
	}
	if service.saves != 0 {
		t.Fatal("read changed startup configuration")
	}
	for _, body := range []string{`{}`, `{"schema_version":1,"expected_revision":"x","settings":{}}`, `{"command":"restart"}`} {
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, WebPath, strings.NewReader(body)))
		if w.Code != 400 || service.saves != 0 {
			t.Fatal("invalid input reached writer", w.Code)
		}
	}
}
