package notifications

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestStoreFreezesTargetsAndCancelsOnlyPendingOnConfigChange(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	config := enableWebhook(t, store, now)
	event, deliveries, created, err := store.Intake(Event{
		SourceID: "source-sms-1", Type: EventIncomingSMS, LineID: "line-1", CardID: "12345678",
		Transport: "vowifi", Title: "SMS", Text: "hello", Peer: "+100", OccurredAt: now,
	}, now)
	if err != nil || !created || len(deliveries) != 1 || event.Targets[0] != ChannelWebhook {
		t.Fatalf("event=%+v deliveries=%+v created=%t err=%v", event, deliveries, created, err)
	}
	if _, replay, created, err := store.Intake(Event{
		SourceID: "source-sms-1", Type: EventIncomingSMS, LineID: "line-1", CardID: "12345678",
		Transport: "vowifi", Title: "SMS", Text: "hello", Peer: "+100", OccurredAt: now,
	}, now); err != nil || created || len(replay) != 1 {
		t.Fatalf("replay=%+v created=%t err=%v", replay, created, err)
	}
	if _, _, _, err := store.Intake(Event{
		SourceID: "source-sms-1", Type: EventIncomingSMS, LineID: "line-1", CardID: "12345678",
		Transport: "vowifi", Title: "SMS", Text: "different", Peer: "+100", OccurredAt: now,
	}, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed source replay err=%v", err)
	}
	if _, _, created, err := store.Intake(Event{
		SourceID: "source-sms-1", Type: EventIncomingSMS, LineID: "line-1", LineName: "Renamed line",
		CardID: "12345678", MSISDN: "+1999", Transport: "vowifi", Title: "Renamed title",
		Text: "hello", Peer: "+100", OccurredAt: now,
	}, now); err != nil || created {
		t.Fatalf("catalog enrichment changed source identity: created=%t err=%v", created, err)
	}
	if _, _, _, err := store.Intake(Event{
		SourceID: "source-sms-1", Type: EventIncomingSMS, LineID: "line-1", CardID: "87654321",
		Transport: "vowifi", Title: "SMS", Text: "hello", Peer: "+100", OccurredAt: now,
	}, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed source CardID err=%v", err)
	}
	claimed, _, _, ok, err := store.Claim(deliveries[0].DeliveryID, now)
	if err != nil || !ok || claimed.State != DeliverySending {
		t.Fatalf("claimed=%+v ok=%t err=%v", claimed, ok, err)
	}
	config.Webhook.Enabled = false
	if _, changed, err := store.PutConfigExpected(config.Revision, config, now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("config changed=%t err=%v", changed, err)
	}
	history, err := store.Deliveries(10)
	if err != nil || len(history) != 1 || history[0].State != DeliverySending {
		t.Fatalf("sending delivery was rewritten on config change: %+v err=%v", history, err)
	}
	if _, err := store.Complete(claimed.DeliveryID, DeliveryUncertain,
		"notification_config_changed_after_write", 0, time.Time{}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeLineRejectsSendingAndErasesPendingPayload(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Now().UTC()
	enableWebhook(t, store, now)
	_, deliveries, _, err := store.Intake(Event{SourceID: "purge-source", Type: EventIncomingSMS,
		LineID: "line-purge", CardID: "12345678", Transport: "vowifi", Title: "SMS", Text: "secret",
		Peer: "+100", OccurredAt: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PurgeLine("line-purge"); err != nil {
		t.Fatal(err)
	}
	if history, _ := store.Deliveries(10); len(history) != 0 {
		t.Fatalf("purged delivery history=%+v", history)
	}
	if _, _, _, err := store.Intake(Event{SourceID: "purge-late", Type: EventIncomingSMS,
		LineID: "line-purge", CardID: "12345678", Transport: "vowifi", Title: "SMS", Text: "late",
		Peer: "+100", OccurredAt: now.Add(time.Second)}, now.Add(time.Second)); err == nil {
		t.Fatal("purged line accepted a late notification")
	}
	_, deliveries, _, err = store.Intake(Event{SourceID: "sending-source", Type: EventIncomingSMS,
		LineID: "line-sending", CardID: "12345678", Transport: "vowifi", Title: "SMS", Text: "secret",
		Peer: "+100", OccurredAt: now.Add(time.Second)}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, claimed, err := store.Claim(deliveries[0].DeliveryID, now.Add(time.Second)); err != nil || !claimed {
		t.Fatalf("claim=%t err=%v", claimed, err)
	}
	if err := store.PurgeLine("line-sending"); err == nil {
		t.Fatal("sending notification was purged")
	}
}

func TestStoreRecoversSendingAsUncertainAndClearsSensitivePayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("notification store mode=%v", info.Mode())
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	enableWebhook(t, store, now)
	_, deliveries, _, err := store.Intake(Event{SourceID: "source-call-1", Type: EventIncomingCall,
		LineID: "line-1", LineName: "Private line", CardID: "8944100000000000001", MSISDN: "+44999",
		Transport: "vowifi", Title: "Call", Peer: "+441", OccurredAt: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, claimed, err := store.Claim(deliveries[0].DeliveryID, now); err != nil || !claimed {
		t.Fatalf("claimed=%t err=%v", claimed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	history, err := store.Deliveries(10)
	if err != nil || len(history) != 1 || history[0].State != DeliveryUncertain ||
		history[0].Code != "notification_process_restarted" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	event, _, err := store.EventForDelivery(history[0].DeliveryID)
	if err != nil || !event.PayloadCleared || event.Title != "" || event.Peer != "" ||
		event.LineName != "" || event.CardID != "" || event.MSISDN != "" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	if deleted, err := store.ClearTerminal(); err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketEvents).Get([]byte(event.EventID)) != nil {
			t.Fatal("cleared terminal event payload record remained")
		}
		if tx.Bucket(bucketReceipts).Get([]byte(event.SourceID)) == nil {
			t.Fatal("source idempotency receipt was deleted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostAlertBaselineDoesNotReplayAndFreshTransitionDoes(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	enableWebhook(t, store, now)
	key := deterministicID("host-alert-key", "disk_usage_warning\x00disk")
	alert := HostAlertInput{Key: key, Code: "disk_usage_warning", Scope: "disk", Severity: "warning", Title: "Disk", Text: "warning"}
	if err := store.SeedReceipts(nil, []HostAlertInput{alert}); err != nil {
		t.Fatal(err)
	}
	authoritative := map[string]bool{"disk": true, "temperature": true, "systemd": true}
	if created, err := store.ReconcileHostAlerts([]HostAlertInput{alert}, authoritative, now); err != nil || len(created) != 0 {
		t.Fatalf("baseline replay created=%+v err=%v", created, err)
	}
	for i := 0; i <= 30; i++ {
		if _, err := store.ReconcileHostAlerts(nil, authoritative, now.Add(time.Second+time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	created, err := store.ReconcileHostAlerts([]HostAlertInput{alert}, authoritative, now.Add(31*time.Minute))
	if err != nil || len(created) != 1 {
		t.Fatalf("transition created=%+v err=%v", created, err)
	}
	deliveries, _ := store.Deliveries(10)
	if len(deliveries) != 1 || deliveries[0].EventType != EventHostAlert {
		t.Fatalf("deliveries=%+v", deliveries)
	}
}

func TestAcknowledgedAlertSurvivesUnknownAndRearmsAfterSustainedRecovery(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1800000000, 0).UTC()
	enableWebhook(t, store, now)
	alert := HostAlertInput{Key: deterministicID("ack-alert", "disk"), Code: "disk_usage_warning", Scope: "host.disk", Severity: "warning", Title: "Disk", Text: "warning"}
	authority := map[string]bool{"disk": true}
	if created, err := store.ReconcileHostAlerts([]HostAlertInput{alert}, authority, now); err != nil || len(created) != 1 {
		t.Fatalf("initial=%v %v", created, err)
	}
	items, err := store.HostAlerts()
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v %v", items, err)
	}
	occurrence := items[0].Occurrence
	if err := store.AcknowledgeHostAlert(alert.Key, occurrence+1); err != ErrConflict {
		t.Fatal("stale confirmation accepted")
	}
	if err := store.AcknowledgeHostAlert(alert.Key, occurrence); err != nil {
		t.Fatal(err)
	}
	if created, err := store.ReconcileHostAlerts([]HostAlertInput{alert}, authority, now.Add(7*time.Hour)); err != nil || len(created) != 0 {
		t.Fatalf("ack repeated=%v %v", created, err)
	}
	for i := 0; i < 20; i++ {
		if _, err := store.ReconcileHostAlerts(nil, authority, now.Add(8*time.Hour+time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ReconcileHostAlerts(nil, map[string]bool{"disk": false}, now.Add(9*time.Hour)); err != nil {
		t.Fatal(err)
	}
	items, err = store.HostAlerts()
	if err != nil || len(items) != 1 || !items[0].Acknowledged {
		t.Fatalf("unknown reset ack=%v %v", items, err)
	}
	for i := 0; i <= 30; i++ {
		if _, err := store.ReconcileHostAlerts(nil, authority, now.Add(10*time.Hour+time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if created, err := store.ReconcileHostAlerts([]HostAlertInput{alert}, authority, now.Add(11*time.Hour)); err != nil || len(created) != 1 {
		t.Fatalf("rearm=%v %v", created, err)
	}
	if err := store.AcknowledgeHostAlert(alert.Key, occurrence); err != ErrConflict {
		t.Fatal("old occurrence acknowledged new condition")
	}
}

func TestUnavailableTemperatureFamilyCannotClearAndReplayAnActiveAlert(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	enableWebhook(t, store, now)
	alert := HostAlertInput{
		Key:  deterministicID("host-alert-key", "temperature_warning\x00host.temperature:cpu"),
		Code: "temperature_warning", Scope: "host.temperature:cpu", Severity: "warning",
		Title: "Temperature", Text: "warning",
	}
	if err := store.SeedReceipts(nil, []HostAlertInput{alert}); err != nil {
		t.Fatal(err)
	}
	if created, err := store.ReconcileHostAlerts(nil,
		map[string]bool{"disk": true, "temperature": false, "systemd": true}, now); err != nil || len(created) != 0 {
		t.Fatalf("unavailable temperature created=%+v err=%v", created, err)
	}
	created, err := store.ReconcileHostAlerts([]HostAlertInput{alert},
		map[string]bool{"disk": true, "temperature": true, "systemd": true}, now.Add(time.Minute))
	if err != nil || len(created) != 0 {
		t.Fatalf("same hot alert replayed after sampler recovery: created=%+v err=%v", created, err)
	}
}

func TestNotificationTestOperationIsPersistentAndConflictsAcrossChannels(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	enableWebhook(t, store, now)
	first, created, err := store.EnqueueTest("test-operation-1", ChannelWebhook, now)
	if err != nil || !created {
		t.Fatalf("first=%+v created=%t err=%v", first, created, err)
	}
	replay, created, err := store.EnqueueTest("test-operation-1", ChannelWebhook, now.Add(time.Second))
	if err != nil || created || replay.DeliveryID != first.DeliveryID {
		t.Fatalf("replay=%+v created=%t err=%v", replay, created, err)
	}
	if _, _, err := store.EnqueueTest("test-operation-1", ChannelTelegram, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-channel operation err=%v", err)
	}
}

func openNotificationStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "notifications.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func enableWebhook(t *testing.T, store *Store, now time.Time) Config {
	t.Helper()
	config, err := store.Config()
	if err != nil {
		t.Fatal(err)
	}
	config.Webhook.Enabled, config.Webhook.URL = true, "http://127.0.0.1:18080/notify"
	updated, changed, err := store.PutConfigExpected(config.Revision, config, now)
	if err != nil || !changed {
		t.Fatalf("enable webhook changed=%t err=%v", changed, err)
	}
	return updated
}
