package notifications

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHostAlertHTTPUsesExactOccurrenceAndNoRawStoreErrors(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1800000000, 0).UTC()
	alert := HostAlertInput{Key: deterministicID("http-alert", "disk"), Code: "disk_usage_warning", Scope: "host.disk", Severity: "warning", Title: "Disk", Text: "warning"}
	if _, err := store.ReconcileHostAlerts([]HostAlertInput{alert}, map[string]bool{"disk": true}, now); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store, func() time.Time { return now }, func() {}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	response := request(http.MethodGet, "/v1/system/alerts", "")
	var result struct {
		Alerts []HostAlertView `json:"alerts"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Alerts) != 1 {
		t.Fatal(response.Code, response.Body.String())
	}
	if result.Alerts[0].LastObserved != now {
		t.Fatal("missing sample time")
	}
	for _, item := range []struct {
		body   string
		status int
	}{
		{`{}`, 400}, {fmt.Sprintf(`{"key":%q,"occurrence":99}`, alert.Key), 409},
		{fmt.Sprintf(`{"key":%q,"occurrence":1}`, alert.Key), 200},
		{fmt.Sprintf(`{"key":%q,"occurrence":1,"ignored":true}`, alert.Key), 400},
	} {
		response := request(http.MethodPost, "/v1/system/alerts/acknowledge", item.body)
		if response.Code != item.status {
			t.Fatalf("%d %s", response.Code, response.Body.String())
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if response := request(http.MethodGet, "/v1/system/alerts", ""); response.Code != 503 {
		t.Fatal(response.Code)
	}
}

func TestUnacknowledgedHostAlertRepeatsOnlyAfterSixHours(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1800000000, 0).UTC()
	enableWebhook(t, store, now)
	alert := HostAlertInput{Key: deterministicID("repeat-alert", "disk"), Code: "disk_usage_warning", Scope: "host.disk", Severity: "warning", Title: "Disk", Text: "warning"}
	for _, item := range []struct {
		offset time.Duration
		count  int
	}{{0, 1}, {time.Minute, 0}, {6*time.Hour - time.Second, 0}, {6 * time.Hour, 1}, {6 * time.Hour, 0}} {
		created, err := store.ReconcileHostAlerts([]HostAlertInput{alert}, map[string]bool{"disk": true}, now.Add(item.offset))
		if err != nil || len(created) != item.count {
			t.Fatalf("%s count=%d err=%v", item.offset, len(created), err)
		}
	}
}

func TestAcknowledgementPersistsAcrossStoreReopen(t *testing.T) {
	store := openNotificationStore(t)
	path := store.db.Path()
	now := time.Unix(1800000000, 0).UTC()
	alert := HostAlertInput{Key: deterministicID("persistent-ack", "swap"), Code: "swap_pressure", Scope: "host.swap", Severity: "warning", Title: "Swap", Text: "pressure"}
	if _, err := store.ReconcileHostAlerts([]HostAlertInput{alert}, map[string]bool{"swap": true}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeHostAlert(alert.Key, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err := reopened.HostAlerts()
	if err != nil || len(items) != 1 || !items[0].Acknowledged {
		t.Fatalf("lost ack: %v %v", items, err)
	}
	if created, err := reopened.ReconcileHostAlerts([]HostAlertInput{alert}, map[string]bool{"swap": true}, now.Add(7*time.Hour)); err != nil || len(created) != 0 {
		t.Fatalf("repeated after restart: %v %v", created, err)
	}
}

func TestNewHostFamiliesUseExistingNotificationIntake(t *testing.T) {
	for _, item := range []struct{ code, family string }{{"swap_pressure", "swap"}, {"undervoltage_now", "power"}, {"undervoltage_seen", "power"}, {"throttled_now", "power"}, {"default_route_changed", "route"}} {
		store := openNotificationStore(t)
		now := time.Unix(1800000000, 0).UTC()
		enableWebhook(t, store, now)
		alert := HostAlertInput{Key: deterministicID("family", item.code), Code: item.code, Scope: "host.test", Severity: "warning", Title: item.code, Text: "fixture"}
		created, err := store.ReconcileHostAlerts([]HostAlertInput{alert}, map[string]bool{item.family: true}, now)
		if err != nil || len(created) != 1 || created[0].Type != EventHostAlert {
			t.Fatalf("family %s: %v %v", item.family, created, err)
		}
	}
}
