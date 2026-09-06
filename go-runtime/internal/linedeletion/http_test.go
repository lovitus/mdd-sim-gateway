package linedeletion

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type fakePurger struct {
	calls   int
	retains int
	fail    int
}

func (purger *fakePurger) RetainLine(string) error {
	purger.retains++
	return nil
}

func (purger *fakePurger) PurgeLine(string) error {
	purger.calls++
	if purger.fail > 0 {
		purger.fail--
		return errors.New("injected purge failure")
	}
	return nil
}

func TestHandlerCanRetainEndedMessageAndCallHistory(t *testing.T) {
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	line := linecatalog.Line{SchemaVersion: 1, ID: "line-retain", Enabled: false,
		CardID: "8944100000000000002", SIM: linecatalog.SIMConfig{IMSI: "234100000000002", MCC: "234", MNC: "10"}}
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.SetDeletedExpected(line.ID, true, 2); err != nil {
		t.Fatal(err)
	}
	generic, messages, calls := &fakePurger{}, &fakePurger{}, &fakePurger{}
	handler, err := NewHandler(Config{Catalog: catalog,
		Guard: linecatalog.LifecycleGuardFunc(func(lineID string) (bool, error) {
			_, err := catalog.Get(lineID)
			return false, err
		}),
		Notifications: generic, Events: generic, Allowance: generic, Messages: messages,
		SMSOperations: generic, Calls: calls})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/catalog/lines/line-retain/permanent-delete",
		strings.NewReader(`{"schema_version":1,"operation_id":"delete-retain-operation","delete_history":false}`))
	request.SetPathValue("lineID", line.ID)
	request.Header.Set("If-Match", `"3"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || messages.retains != 1 || calls.retains != 1 || messages.calls != 0 || calls.calls != 0 {
		t.Fatalf("status=%d messages=%+v calls=%+v body=%s", response.Code, messages, calls, response.Body.String())
	}
}

func TestHandlerResumesAtDurableFailedStage(t *testing.T) {
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	line := linecatalog.Line{SchemaVersion: 1, ID: "line-delete", Name: "delete", Enabled: false,
		CardID: "8944100000000000001", SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"}}
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.SetDeletedExpected(line.ID, true, 2); err != nil {
		t.Fatal(err)
	}
	notifications, eventState, allowance, messages := &fakePurger{}, &fakePurger{}, &fakePurger{}, &fakePurger{fail: 1}
	sms, calls := &fakePurger{}, &fakePurger{}
	handler, err := NewHandler(Config{Catalog: catalog,
		Guard: linecatalog.LifecycleGuardFunc(func(lineID string) (bool, error) {
			_, err := catalog.Get(lineID)
			return false, err
		}),
		Notifications: notifications, Events: eventState, Allowance: allowance, Messages: messages, SMSOperations: sms, Calls: calls})
	if err != nil {
		t.Fatal(err)
	}
	request := func(operationID string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/catalog/lines/line-delete/permanent-delete",
			strings.NewReader(`{"schema_version":1,"operation_id":"`+operationID+`"}`))
		r.SetPathValue("lineID", line.ID)
		r.Header.Set("If-Match", `"3"`)
		return r
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request("delete-operation-1"))
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	if _, err := catalog.Get(line.ID); err != nil {
		t.Fatalf("partial cleanup removed catalog line: %v", err)
	}
	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, request("delete-operation-new"))
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "delete-operation-1") {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request("delete-operation-1"))
	if second.Code != http.StatusOK || second.Header().Get("ETag") != `"4"` {
		t.Fatalf("second status=%d etag=%q body=%s", second.Code, second.Header().Get("ETag"), second.Body.String())
	}
	if notifications.calls != 1 || eventState.calls != 1 || allowance.calls != 1 || messages.calls != 2 || sms.calls != 1 || calls.calls != 1 {
		t.Fatalf("purge calls notifications=%d events=%d allowance=%d messages=%d sms=%d calls=%d",
			notifications.calls, eventState.calls, allowance.calls, messages.calls, sms.calls, calls.calls)
	}
	replayed := httptest.NewRecorder()
	handler.ServeHTTP(replayed, request("delete-operation-1"))
	if replayed.Code != http.StatusOK || notifications.calls != 1 || messages.calls != 2 {
		t.Fatalf("succeeded replay status=%d notifications=%d messages=%d body=%s",
			replayed.Code, notifications.calls, messages.calls, replayed.Body.String())
	}
}
