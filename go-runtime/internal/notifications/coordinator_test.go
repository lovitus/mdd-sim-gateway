package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/allowance"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

type coordinatorSMS struct {
	sources []providermessages.NotificationSource
	acked   []string
}

func (source *coordinatorSMS) PendingNotificationSources(int) ([]providermessages.NotificationSource, error) {
	return append([]providermessages.NotificationSource(nil), source.sources...), nil
}

func (source *coordinatorSMS) AckNotificationSource(id string) error {
	source.acked = append(source.acked, id)
	return nil
}

type coordinatorCalls struct {
	sources []callhistory.NotificationSource
	acked   []string
}

func (source *coordinatorCalls) PendingNotificationSources(time.Time, int) ([]callhistory.NotificationSource, error) {
	return append([]callhistory.NotificationSource(nil), source.sources...), nil
}

func (source *coordinatorCalls) AckNotificationSource(id string) error {
	source.acked = append(source.acked, id)
	return nil
}

type coordinatorSystemStatus struct{ snapshot systemstatus.Snapshot }

func (source coordinatorSystemStatus) Snapshot(time.Time) systemstatus.Snapshot {
	return source.snapshot
}

type coordinatorCatalog struct {
	snapshot linecatalog.Snapshot
	lines    map[string]linecatalog.Line
}

func (source coordinatorCatalog) Snapshot() (linecatalog.Snapshot, error) {
	return source.snapshot, nil
}
func (source coordinatorCatalog) Get(id string) (linecatalog.Line, error) {
	line, found := source.lines[id]
	if !found {
		return linecatalog.Line{}, errors.New("line not found")
	}
	return line, nil
}

type coordinatorAllowance map[string]allowance.Snapshot

func (source coordinatorAllowance) Snapshot(id string) (allowance.Snapshot, error) {
	snapshot, found := source[id]
	if !found {
		return allowance.Snapshot{}, errors.New("allowance not found")
	}
	return snapshot, nil
}

func TestCoordinatorDrainsRealtimeFactsWhileHostBaselineIsUnavailable(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	enableWebhook(t, store, now)
	sms := &coordinatorSMS{sources: []providermessages.NotificationSource{{
		SchemaVersion: 1, SourceID: "vowifi-sms-source", LineID: "line-1", CardID: "12345678", Transport: "vowifi",
		Sender: "+100", Body: "hello", ReceivedAt: now,
	}}}
	calls := &coordinatorCalls{sources: []callhistory.NotificationSource{{
		SchemaVersion: 1, SourceID: "vowifi-call-source", LineID: "line-1", CardID: "12345678", Transport: "vowifi",
		Peer: "+200", ReceivedAt: now, NotBefore: now,
	}}}
	line := linecatalog.Line{SchemaVersion: 1, ID: "line-1", Name: "Line", CardID: "12345678"}
	engine, err := NewEngine(EngineConfig{
		Context: t.Context(), Store: store,
		Sender: senderFunc(func(context.Context, Delivery, Event, Config) Outcome {
			return Outcome{State: DeliveryDelivered, Code: "notification_delivered", Wrote: true}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Context: t.Context(), Store: store, Engine: engine, SMS: sms, Calls: calls,
		SystemStatus: coordinatorSystemStatus{snapshot: systemstatus.Snapshot{State: "partial", Stale: true}},
		Catalog:      coordinatorCatalog{snapshot: linecatalog.Snapshot{SchemaVersion: 1, Revision: 1, Lines: []linecatalog.Line{line}}, lines: map[string]linecatalog.Line{"line-1": line}},
		Allowance:    coordinatorAllowance{"line-1": {SchemaVersion: 1, LineID: "line-1", Revision: 1}},
		Now:          func() time.Time { return now }, Logf: func(string, ...any) {},
	})
	if err != nil || engine.BindVerifier(coordinator) != nil {
		t.Fatalf("coordinator err=%v", err)
	}
	defer coordinator.Close()
	if err := coordinator.cycle(); err == nil {
		t.Fatal("partial System Status unexpectedly seeded host baseline")
	}
	if len(sms.acked) != 1 || len(calls.acked) != 1 {
		t.Fatalf("sms ack=%v call ack=%v", sms.acked, calls.acked)
	}
	if _, _, created, err := store.Intake(Event{SourceID: sms.sources[0].SourceID, Type: EventIncomingSMS,
		LineID: "line-1", LineName: "Line", CardID: line.CardID, Transport: "vowifi", Title: "VoWiFi 短信 · Line",
		Text: "hello", Peer: "+100", OccurredAt: now}, now); err != nil || created {
		t.Fatalf("SMS destination receipt missing created=%t err=%v", created, err)
	}
}

func TestRealtimeNotificationCardIdentityDoesNotFollowCatalogReplacement(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	enableWebhook(t, store, now)
	sms := &coordinatorSMS{sources: []providermessages.NotificationSource{{
		SchemaVersion: 1, SourceID: "vowifi-sms-old-card", LineID: "line-1", CardID: "11112222",
		Transport: "vowifi", Sender: "+100", Body: "hello", ReceivedAt: now,
	}}}
	replacement := linecatalog.Line{SchemaVersion: 1, ID: "line-1", Name: "Replacement", CardID: "33334444",
		SIM: linecatalog.SIMConfig{MSISDN: "+1999"}}
	coordinator := &Coordinator{config: CoordinatorConfig{
		Store: store, SMS: sms,
		Catalog: coordinatorCatalog{lines: map[string]linecatalog.Line{"line-1": replacement}},
	}}
	if err := coordinator.drainSMS(now); err != nil {
		t.Fatal(err)
	}
	deliveries, err := store.Deliveries(10)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries=%+v err=%v", deliveries, err)
	}
	event, _, err := store.EventForDelivery(deliveries[0].DeliveryID)
	if err != nil || event.CardID != "11112222" || event.LineName != "line-1" || event.MSISDN != "" {
		t.Fatalf("event followed replacement catalog identity: event=%+v err=%v", event, err)
	}
}

func TestCoordinatorSeedSkipsLinesWithoutExactCardAndISODate(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	valid := linecatalog.Line{SchemaVersion: 1, ID: "valid", CardID: "12345678"}
	noCard := linecatalog.Line{SchemaVersion: 1, ID: "no-card"}
	badDate := linecatalog.Line{SchemaVersion: 1, ID: "bad-date", CardID: "87654321"}
	catalog := coordinatorCatalog{
		snapshot: linecatalog.Snapshot{SchemaVersion: 1, Revision: 1, Lines: []linecatalog.Line{valid, noCard, badDate}},
		lines:    map[string]linecatalog.Line{"valid": valid, "no-card": noCard, "bad-date": badDate},
	}
	allowances := coordinatorAllowance{
		"valid":    {SchemaVersion: 1, LineID: "valid", Revision: 1, Values: allowance.Values{ValidUntil: "2026-09-03"}},
		"no-card":  {SchemaVersion: 1, LineID: "no-card", Revision: 1, Values: allowance.Values{ValidUntil: "2026-09-03"}},
		"bad-date": {SchemaVersion: 1, LineID: "bad-date", Revision: 1, Values: allowance.Values{ValidUntil: "legacy-value"}},
	}
	candidate := reminderEvent(valid, allowances["valid"], "Asia/Shanghai", 2, now)
	candidate.SchemaVersion, candidate.EventID, candidate.Kind, candidate.IntakeRevision = 1, "candidate-event", KindEvent, 1
	if err := candidate.Validate(); err != nil {
		t.Fatalf("valid reminder candidate=%+v err=%v", candidate, err)
	}
	engine, _ := NewEngine(EngineConfig{Context: t.Context(), Store: store,
		Sender: senderFunc(func(context.Context, Delivery, Event, Config) Outcome {
			return Outcome{State: DeliveryDelivered, Code: "notification_delivered", Wrote: true}
		})})
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Context: t.Context(), Store: store, Engine: engine, SMS: &coordinatorSMS{}, Calls: &coordinatorCalls{},
		SystemStatus: coordinatorSystemStatus{snapshot: systemstatus.Snapshot{State: "complete", Stale: false, Alerts: []systemstatus.Alert{}}},
		Catalog:      catalog, Allowance: allowances, Now: func() time.Time { return now }, Logf: func(string, ...any) {},
	})
	if err != nil || engine.BindVerifier(coordinator) != nil {
		t.Fatalf("coordinator err=%v", err)
	}
	defer coordinator.Close()
	if err := coordinator.cycle(); err != nil {
		t.Fatal(err)
	}
	receipts := 0
	if err := store.db.View(func(tx *bolt.Tx) error {
		receipts = tx.Bucket(bucketReceipts).Stats().KeyN
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A line currently two days from expiry seeds the already crossed 3-day
	// and current 2-day thresholds. Invalid card/date facts seed nothing.
	if receipts != 2 {
		t.Fatalf("receipt count=%d", receipts)
	}
}

func TestPartialSystemStatusDoesNotClearAnActiveHostAlert(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	key := deterministicID("host-alert-key", "disk_usage_warning\x00host.disk")
	baseline := HostAlertInput{Key: key, Code: "disk_usage_warning", Scope: "host.disk", Severity: "warning", Title: "Disk", Text: "warning"}
	if err := store.SeedReceipts(nil, []HostAlertInput{baseline}); err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{config: CoordinatorConfig{
		Store: store, SystemStatus: coordinatorSystemStatus{snapshot: systemstatus.Snapshot{State: "partial", Stale: false}},
	}}
	if err := coordinator.reconcileHost(now); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err := store.db.View(func(tx *bolt.Tx) error {
		var value hostAlertState
		if err := json.Unmarshal(tx.Bucket(bucketHostState).Get([]byte(key)), &value); err != nil {
			return err
		}
		active = value.Active
		return nil
	}); err != nil || !active {
		t.Fatalf("active=%t err=%v", active, err)
	}
}

func TestActivationReminderVerifierFencesCardDateAndCalendarDay(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	line := linecatalog.Line{SchemaVersion: 1, ID: "line-1", CardID: "12345678"}
	catalog := coordinatorCatalog{lines: map[string]linecatalog.Line{"line-1": line}}
	allowances := coordinatorAllowance{"line-1": {
		SchemaVersion: 1, LineID: "line-1", Revision: 9, Values: allowance.Values{ValidUntil: "2026-09-03"},
	}}
	coordinator := &Coordinator{config: CoordinatorConfig{Catalog: catalog, Allowance: allowances, Now: func() time.Time { return now }}}
	event := reminderEvent(line, allowances["line-1"], "Asia/Shanghai", 2, now)
	if code := coordinator.ValidateNotificationEvent(context.Background(), event); code != "" {
		t.Fatalf("valid reminder code=%s", code)
	}
	changed := line
	changed.CardID = "87654321"
	catalog.lines["line-1"] = changed
	if code := coordinator.ValidateNotificationEvent(context.Background(), event); code != "activation_reminder_card_changed" {
		t.Fatalf("card code=%s", code)
	}
	catalog.lines["line-1"] = line
	changedAllowance := allowances["line-1"]
	changedAllowance.Values.ValidUntil = "2026-09-04"
	allowances["line-1"] = changedAllowance
	if code := coordinator.ValidateNotificationEvent(context.Background(), event); code != "activation_reminder_date_changed" {
		t.Fatalf("date code=%s", code)
	}
	allowances["line-1"] = allowance.Snapshot{SchemaVersion: 1, LineID: "line-1", Revision: 10, Values: allowance.Values{ValidUntil: "2026-09-03"}}
	coordinator.config.Now = func() time.Time { return now.Add(24 * time.Hour) }
	if code := coordinator.ValidateNotificationEvent(context.Background(), event); code != "activation_reminder_day_changed" {
		t.Fatalf("day code=%s", code)
	}
}

func TestActivationReminderIsIdempotentAcrossCyclesOnTheSameLocalDay(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	enableWebhook(t, store, now)
	if err := store.SeedReceipts(nil, nil); err != nil {
		t.Fatal(err)
	}
	line := linecatalog.Line{SchemaVersion: 1, ID: "line-1", Name: "Original", CardID: "12345678"}
	catalog := coordinatorCatalog{
		snapshot: linecatalog.Snapshot{SchemaVersion: 1, Revision: 1, Lines: []linecatalog.Line{line}},
		lines:    map[string]linecatalog.Line{"line-1": line},
	}
	allowances := coordinatorAllowance{"line-1": {
		SchemaVersion: 1, LineID: "line-1", Revision: 2, Values: allowance.Values{ValidUntil: "2026-09-04"},
	}}
	coordinator := &Coordinator{config: CoordinatorConfig{Store: store, Catalog: catalog, Allowance: allowances}}
	if err := coordinator.produceReminders(now); err != nil {
		t.Fatal(err)
	}
	line.Name, line.SIM.MSISDN = "Renamed", "+123"
	catalog.snapshot.Lines[0], catalog.lines["line-1"] = line, line
	if err := coordinator.produceReminders(now.Add(time.Minute)); err != nil {
		t.Fatalf("same-day replay failed after catalog enrichment changed: %v", err)
	}
	deliveries, err := store.Deliveries(10)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries=%+v err=%v", deliveries, err)
	}
}
