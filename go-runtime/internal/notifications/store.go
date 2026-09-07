package notifications

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const storeSchemaVersion uint64 = 1

var (
	bucketMeta        = []byte("metadata")
	bucketConfig      = []byte("config")
	bucketEvents      = []byte("events")
	bucketReceipts    = []byte("source_receipts")
	bucketDeliveries  = []byte("deliveries")
	bucketOperations  = []byte("test_operations")
	bucketHostState   = []byte("host_alert_state")
	bucketPurgedLines = []byte("purged_lines_v1")
	keySchema         = []byte("schema_version")
	keyConfig         = []byte("current")
	keySeeded         = []byte("producer_baseline_seeded")

	ErrRevision = errors.New("notification config revision changed")
	ErrConflict = errors.New("notification identity conflict")
	ErrNotFound = errors.New("notification record not found")
	ErrNotReady = errors.New("notification delivery is not ready")
)

type Store struct {
	db        *bolt.DB
	closeOnce sync.Once
	closeErr  error
}

type sourceReceipt struct {
	SourceID    string    `json:"source_id"`
	EventID     string    `json:"event_id"`
	OccurredAt  time.Time `json:"occurred_at"`
	Type        string    `json:"type"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

type testOperation struct {
	OperationID string `json:"operation_id"`
	Channel     string `json:"channel"`
	EventID     string `json:"event_id"`
}

type hostAlertState struct {
	Active       bool      `json:"active"`
	Code         string    `json:"code"`
	Scope        string    `json:"scope"`
	Family       string    `json:"family"`
	Severity     string    `json:"severity"`
	Occurrence   uint64    `json:"occurrence"`
	Acknowledged bool      `json:"acknowledged,omitempty"`
	MissingSince time.Time `json:"missing_since,omitempty"`
	LastObserved time.Time `json:"last_observed,omitempty"`
	LastNotified time.Time `json:"last_notified,omitempty"`
}

const hostAlertClearAfter = 30 * time.Minute
const hostAlertRepeatAfter = 6 * time.Hour

func Open(path string, timeout time.Duration) (*Store, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid notification store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	created := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		created = true
	} else if err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.initialize(time.Now().UTC()); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if created {
		parent, err := os.Open(filepath.Dir(path))
		if err != nil {
			return nil, errors.Join(err, db.Close())
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr, db.Close())
		}
	}
	return store, nil
}

func (store *Store) initialize(now time.Time) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketConfig, bucketEvents, bucketReceipts,
			bucketDeliveries, bucketOperations, bucketHostState, bucketPurgedLines} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		meta := tx.Bucket(bucketMeta)
		storedSchema := meta.Get(keySchema)
		if storedSchema == nil {
			var encoded [8]byte
			binary.BigEndian.PutUint64(encoded[:], storeSchemaVersion)
			if err := meta.Put(keySchema, encoded[:]); err != nil {
				return err
			}
		} else if len(storedSchema) != 8 || binary.BigEndian.Uint64(storedSchema) != storeSchemaVersion {
			return errors.New("unsupported notification store schema")
		}
		if tx.Bucket(bucketConfig).Get(keyConfig) == nil {
			if err := putJSON(tx.Bucket(bucketConfig), keyConfig, DefaultConfig()); err != nil {
				return err
			}
		}
		var config Config
		if json.Unmarshal(tx.Bucket(bucketConfig).Get(keyConfig), &config) != nil || config.Validate() != nil {
			return errors.New("stored notification config is invalid")
		}
		return recoverSendingTx(tx, now)
	})
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.closeOnce.Do(func() { store.closeErr = store.db.Close() })
	return store.closeErr
}

func (store *Store) Config() (Config, error) {
	var config Config
	err := store.db.View(func(tx *bolt.Tx) error {
		wire := tx.Bucket(bucketConfig).Get(keyConfig)
		if json.Unmarshal(wire, &config) != nil || config.Validate() != nil {
			return errors.New("stored notification config is invalid")
		}
		return nil
	})
	return cloneConfig(config), err
}

func (store *Store) PutConfigExpected(expected uint64, next Config, now time.Time) (Config, bool, error) {
	now = now.UTC()
	if now.IsZero() {
		return Config{}, false, errors.New("invalid notification config time")
	}
	var result Config
	changed := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		current, err := configFromTx(tx)
		if err != nil {
			return err
		}
		result = current
		if current.Revision != expected {
			return ErrRevision
		}
		next.SchemaVersion, next.Revision = SchemaVersion, current.Revision
		next.Imported = current.Imported
		next.Webhook.ConfiguredAt = current.Webhook.ConfiguredAt
		next.Telegram.ConfiguredAt = current.Telegram.ConfiguredAt
		next.PushPlus.ConfiguredAt = current.PushPlus.ConfiguredAt
		if sameConfig(current, next) {
			return nil
		}
		next.Revision++
		if !sameWebhook(current.Webhook, next.Webhook) {
			next.Webhook.ConfiguredAt = now
		}
		if !sameTelegram(current.Telegram, next.Telegram) {
			next.Telegram.ConfiguredAt = now
		}
		if !samePushPlus(current.PushPlus, next.PushPlus) {
			next.PushPlus.ConfiguredAt = now
		}
		if err := next.Validate(); err != nil {
			return err
		}
		if err := putJSON(tx.Bucket(bucketConfig), keyConfig, next); err != nil {
			return err
		}
		if err := cancelStalePendingTx(tx, next, now); err != nil {
			return err
		}
		result, changed = next, true
		return nil
	})
	return cloneConfig(result), changed, err
}

func (store *Store) Intake(input Event, now time.Time) (Event, []Delivery, bool, error) {
	now = now.UTC()
	if now.IsZero() || !validEventType(input.Type) || !identifier(input.SourceID, 256) {
		return Event{}, nil, false, errors.New("invalid notification intake")
	}
	var event Event
	var deliveries []Delivery
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		if input.LineID != "" && tx.Bucket(bucketPurgedLines).Get([]byte(input.LineID)) != nil {
			return errors.New("notification line was permanently deleted")
		}
		var err error
		event, deliveries, created, err = intakeTx(tx, input, now)
		return err
	})
	return cloneEvent(event), cloneDeliveries(deliveries), created, err
}

func intakeTx(tx *bolt.Tx, input Event, now time.Time) (Event, []Delivery, bool, error) {
	input.OccurredAt = input.OccurredAt.UTC()
	fingerprint := eventSourceFingerprint(input)
	if receiptWire := tx.Bucket(bucketReceipts).Get([]byte(input.SourceID)); receiptWire != nil {
		var receipt sourceReceipt
		if json.Unmarshal(receiptWire, &receipt) != nil || receipt.SourceID != input.SourceID ||
			!validEventType(receipt.Type) || receipt.OccurredAt.IsZero() ||
			receipt.Fingerprint != "" && !hexSHA256.MatchString(receipt.Fingerprint) {
			return Event{}, nil, false, errors.New("stored notification receipt is invalid")
		}
		if receipt.Fingerprint != "" && receipt.Fingerprint != fingerprint {
			return Event{}, nil, false, ErrConflict
		}
		event, eventErr := eventFromTx(tx, receipt.EventID)
		if errors.Is(eventErr, ErrNotFound) {
			return Event{SchemaVersion: SchemaVersion, EventID: receipt.EventID,
				SourceID: receipt.SourceID, Type: receipt.Type, OccurredAt: receipt.OccurredAt, PayloadCleared: true}, nil, false, nil
		}
		if eventErr != nil {
			return Event{}, nil, false, eventErr
		}
		deliveries, err := deliveriesForEventTx(tx, receipt.EventID)
		return event, deliveries, false, err
	}
	config, err := configFromTx(tx)
	if err != nil {
		return Event{}, nil, false, err
	}
	input.SchemaVersion = SchemaVersion
	input.EventID = deterministicID("event", input.SourceID)
	input.Kind = KindEvent
	input.IntakeRevision = config.Revision
	input.Targets = sortedTargets(config.Targets(input.Type))
	if err := input.Validate(); err != nil {
		return Event{}, nil, false, err
	}
	receipt := sourceReceipt{SourceID: input.SourceID, EventID: input.EventID, OccurredAt: input.OccurredAt,
		Type: input.Type, Fingerprint: fingerprint}
	if err := putJSON(tx.Bucket(bucketReceipts), []byte(input.SourceID), receipt); err != nil {
		return Event{}, nil, false, err
	}
	if len(input.Targets) == 0 {
		input.PayloadCleared = true
		input.LineName, input.CardID, input.MSISDN = "", "", ""
		input.Title, input.Text, input.Peer, input.Reminder = "", "", "", nil
		return input, nil, true, nil
	}
	if err := putJSON(tx.Bucket(bucketEvents), []byte(input.EventID), input); err != nil {
		return Event{}, nil, false, err
	}
	deliveries := make([]Delivery, 0, len(input.Targets))
	for _, channel := range input.Targets {
		delivery := Delivery{
			SchemaVersion: SchemaVersion, DeliveryID: deterministicID("delivery", input.EventID+"\x00"+channel),
			EventID: input.EventID, Kind: KindEvent, EventType: input.Type, LineID: input.LineID,
			Channel: channel, ConfigRevision: config.Revision, State: DeliveryPending,
			NotBefore: now, CreatedAt: now, UpdatedAt: now,
		}
		if delivery.Validate() != nil {
			return Event{}, nil, false, errors.New("invalid notification delivery")
		}
		if err := putJSON(tx.Bucket(bucketDeliveries), []byte(delivery.DeliveryID), delivery); err != nil {
			return Event{}, nil, false, err
		}
		deliveries = append(deliveries, delivery)
	}
	return input, deliveries, true, nil
}

func (store *Store) SeedReceipts(events []Event, hostAlerts []HostAlertInput) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		if bytes.Equal(tx.Bucket(bucketMeta).Get(keySeeded), []byte{1}) {
			return nil
		}
		for _, event := range events {
			if !identifier(event.SourceID, 256) || !validEventType(event.Type) || event.OccurredAt.IsZero() {
				return errors.New("invalid notification baseline event")
			}
			receipt := sourceReceipt{SourceID: event.SourceID, EventID: deterministicID("event", event.SourceID),
				OccurredAt: event.OccurredAt.UTC(), Type: event.Type}
			if wire := tx.Bucket(bucketReceipts).Get([]byte(event.SourceID)); wire != nil {
				var prior sourceReceipt
				if json.Unmarshal(wire, &prior) != nil || prior.SourceID != receipt.SourceID ||
					prior.Type != receipt.Type || !prior.OccurredAt.Equal(receipt.OccurredAt) {
					return ErrConflict
				}
				continue
			}
			if err := putJSON(tx.Bucket(bucketReceipts), []byte(event.SourceID), receipt); err != nil {
				return err
			}
		}
		for _, alert := range hostAlerts {
			if alert.Validate() != nil {
				return errors.New("invalid notification host baseline")
			}
			family := hostAlertFamily(alert.Code)
			if family == "" {
				return errors.New("invalid notification host baseline family")
			}
			if err := putJSON(tx.Bucket(bucketHostState), []byte(alert.Key), hostAlertState{
				Active: true, Code: alert.Code, Scope: alert.Scope, Family: family, Severity: alert.Severity,
			}); err != nil {
				return err
			}
		}
		return tx.Bucket(bucketMeta).Put(keySeeded, []byte{1})
	})
}

func (store *Store) ReconcileHostAlerts(alerts []HostAlertInput, authoritativeFamilies map[string]bool, now time.Time) ([]Event, error) {
	now = now.UTC()
	if now.IsZero() {
		return nil, errors.New("invalid host alert reconciliation time")
	}
	current := make(map[string]HostAlertInput, len(alerts))
	for family := range authoritativeFamilies {
		if !oneOf(family, "disk", "temperature", "systemd", "swap", "power", "route") {
			return nil, errors.New("invalid host alert authority family")
		}
	}
	for _, alert := range alerts {
		family := hostAlertFamily(alert.Code)
		if alert.Validate() != nil || family == "" || !authoritativeFamilies[family] {
			return nil, errors.New("invalid host alert reconciliation")
		}
		if _, duplicate := current[alert.Key]; duplicate {
			return nil, errors.New("duplicate host alert reconciliation")
		}
		current[alert.Key] = alert
	}
	created := []Event{}
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketHostState)
		states := map[string]hostAlertState{}
		if err := bucket.ForEach(func(key, wire []byte) error {
			var state hostAlertState
			if json.Unmarshal(wire, &state) != nil || state.Occurrence > 1<<62 || !machineCode(state.Code) ||
				!identifier(state.Scope, 256) || !oneOf(state.Family, "disk", "temperature", "systemd", "swap", "power", "route") ||
				state.Severity != "" && !oneOf(state.Severity, "warning", "critical") {
				return errors.New("stored host alert state is invalid")
			}
			states[string(key)] = state
			return nil
		}); err != nil {
			return err
		}
		for key, state := range states {
			if _, found := current[key]; !found && state.Active && now.After(state.LastObserved) {
				if !authoritativeFamilies[state.Family] {
					state.MissingSince = time.Time{}
				} else {
					if state.MissingSince.IsZero() || !state.LastObserved.IsZero() && now.Sub(state.LastObserved) > 2*time.Minute {
						state.MissingSince = now
					}
					if now.Sub(state.MissingSince) >= hostAlertClearAfter {
						state.Active = false
						state.Acknowledged = false
						state.MissingSince = time.Time{}
					}
				}
				state.LastObserved = now
				if err := putJSON(bucket, []byte(key), state); err != nil {
					return err
				}
			}
		}
		for key, alert := range current {
			state := states[key]
			if !now.After(state.LastObserved) {
				continue
			}
			state.MissingSince = time.Time{}
			state.LastObserved = now
			if state.Active && state.Severity == alert.Severity {
				// Old seeded baselines start the repeat interval without sending
				// a duplicate initial notification after a Core upgrade.
				if state.LastNotified.IsZero() {
					state.LastNotified = now
				}
				if state.Acknowledged || now.Sub(state.LastNotified) < hostAlertRepeatAfter {
					if err := putJSON(bucket, []byte(key), state); err != nil {
						return err
					}
					continue
				}
			} else {
				state.Acknowledged = false
			}
			state.Active, state.Code, state.Scope = true, alert.Code, alert.Scope
			state.Family, state.Severity, state.Occurrence = hostAlertFamily(alert.Code), alert.Severity, state.Occurrence+1
			state.LastNotified = now
			if err := putJSON(bucket, []byte(key), state); err != nil {
				return err
			}
			event := Event{
				SourceID: "host-alert-" + deterministicID("transition", key+"\x00"+alert.Severity+"\x00"+uintString(state.Occurrence)),
				Type:     EventHostAlert, Title: alert.Title, Text: alert.Text, Peer: alert.Scope, OccurredAt: now,
			}
			stored, _, wasCreated, err := intakeTx(tx, event, now)
			if err != nil {
				return err
			}
			if wasCreated {
				created = append(created, stored)
			}
		}
		return nil
	})
	return created, err
}

func hostAlertFamily(code string) string {
	switch code {
	case "swap_pressure":
		return "swap"
	case "undervoltage_now", "undervoltage_seen", "throttled_now":
		return "power"
	case "default_route_changed":
		return "route"
	case "disk_usage_warning", "disk_usage_critical":
		return "disk"
	case "temperature_warning", "temperature_critical":
		return "temperature"
	case "systemd_unit_failed":
		return "systemd"
	default:
		return ""
	}
}

func (store *Store) Seeded() (bool, error) {
	seeded := false
	err := store.db.View(func(tx *bolt.Tx) error {
		seeded = bytes.Equal(tx.Bucket(bucketMeta).Get(keySeeded), []byte{1})
		return nil
	})
	return seeded, err
}

func (store *Store) Pending(channel string, now time.Time) (Delivery, bool, error) {
	now = now.UTC()
	if !oneOf(channel, ChannelWebhook, ChannelTelegram, ChannelPushPlus) || now.IsZero() {
		return Delivery{}, false, errors.New("invalid notification pending query")
	}
	var result Delivery
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDeliveries).ForEach(func(_, wire []byte) error {
			var delivery Delivery
			if json.Unmarshal(wire, &delivery) != nil || delivery.Validate() != nil {
				return errors.New("stored notification delivery is invalid")
			}
			if delivery.Channel != channel || delivery.State != DeliveryPending || delivery.NotBefore.After(now) {
				return nil
			}
			if !found || delivery.NotBefore.Before(result.NotBefore) ||
				delivery.NotBefore.Equal(result.NotBefore) && delivery.DeliveryID < result.DeliveryID {
				result, found = delivery, true
			}
			return nil
		})
	})
	return result, found, err
}

func (store *Store) EventForDelivery(deliveryID string) (Event, Delivery, error) {
	var event Event
	var delivery Delivery
	err := store.db.View(func(tx *bolt.Tx) error {
		var err error
		delivery, err = deliveryFromTx(tx, deliveryID)
		if err != nil {
			return err
		}
		event, err = eventFromTx(tx, delivery.EventID)
		return err
	})
	return cloneEvent(event), delivery, err
}

func (store *Store) Claim(deliveryID string, now time.Time) (Delivery, Event, Config, bool, error) {
	now = now.UTC()
	var delivery Delivery
	var event Event
	var config Config
	claimed := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		var err error
		delivery, err = deliveryFromTx(tx, deliveryID)
		if err != nil {
			return err
		}
		if delivery.State != DeliveryPending || delivery.NotBefore.After(now) {
			return ErrNotReady
		}
		config, err = configFromTx(tx)
		if err != nil {
			return err
		}
		if delivery.ConfigRevision != config.Revision || !config.ChannelEnabled(delivery.Channel, delivery.EventType) {
			delivery.State, delivery.Code, delivery.UpdatedAt, delivery.FinishedAt =
				DeliveryCanceled, "notification_config_changed", now, now
			if err := putJSON(tx.Bucket(bucketDeliveries), []byte(delivery.DeliveryID), delivery); err != nil {
				return err
			}
			return finalizeEventTx(tx, delivery.EventID)
		}
		event, err = eventFromTx(tx, delivery.EventID)
		if err != nil {
			return err
		}
		delivery.State, delivery.Attempts, delivery.UpdatedAt = DeliverySending, delivery.Attempts+1, now
		if delivery.Attempts > 3 {
			return errors.New("notification delivery attempts exceeded")
		}
		if err := putJSON(tx.Bucket(bucketDeliveries), []byte(delivery.DeliveryID), delivery); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return delivery, cloneEvent(event), cloneConfig(config), claimed, err
}

func (store *Store) Complete(deliveryID, state, code string, httpStatus int, next time.Time, now time.Time) (Delivery, error) {
	now, next = now.UTC(), next.UTC()
	if !oneOf(state, DeliveryPending, DeliveryDelivered, DeliveryFailed, DeliveryUncertain, DeliveryCanceled) ||
		state != DeliveryPending && !machineCode(code) || httpStatus < 0 || httpStatus > 999 || now.IsZero() {
		return Delivery{}, errors.New("invalid notification delivery completion")
	}
	var delivery Delivery
	err := store.db.Update(func(tx *bolt.Tx) error {
		var err error
		delivery, err = deliveryFromTx(tx, deliveryID)
		if err != nil {
			return err
		}
		if delivery.State != DeliverySending {
			return ErrConflict
		}
		if state == DeliveryPending {
			if next.IsZero() || !next.After(now) || delivery.Attempts >= 3 {
				return errors.New("invalid notification retry")
			}
			delivery.State, delivery.Code, delivery.HTTPStatus = DeliveryPending, "", 0
			delivery.NotBefore, delivery.UpdatedAt = next, now
		} else {
			delivery.State, delivery.Code, delivery.HTTPStatus = state, code, httpStatus
			delivery.UpdatedAt, delivery.FinishedAt = now, now
		}
		if err := putJSON(tx.Bucket(bucketDeliveries), []byte(delivery.DeliveryID), delivery); err != nil {
			return err
		}
		if delivery.Terminal() {
			return finalizeEventTx(tx, delivery.EventID)
		}
		return nil
	})
	return delivery, err
}

func (store *Store) Cancel(deliveryID, code string, now time.Time) (Delivery, error) {
	now = now.UTC()
	if !machineCode(code) || now.IsZero() {
		return Delivery{}, errors.New("invalid notification cancellation")
	}
	var delivery Delivery
	err := store.db.Update(func(tx *bolt.Tx) error {
		var err error
		delivery, err = deliveryFromTx(tx, deliveryID)
		if err != nil {
			return err
		}
		if delivery.State != DeliveryPending && delivery.State != DeliverySending {
			return nil
		}
		if delivery.State == DeliverySending {
			return ErrConflict
		}
		delivery.State, delivery.Code, delivery.UpdatedAt, delivery.FinishedAt = DeliveryCanceled, code, now, now
		if err := putJSON(tx.Bucket(bucketDeliveries), []byte(delivery.DeliveryID), delivery); err != nil {
			return err
		}
		return finalizeEventTx(tx, delivery.EventID)
	})
	return delivery, err
}

func (store *Store) Deliveries(limit int) ([]Delivery, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("notification delivery limit must be between 1 and 500")
	}
	result := make([]Delivery, 0, limit)
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDeliveries).ForEach(func(_, wire []byte) error {
			var delivery Delivery
			if json.Unmarshal(wire, &delivery) != nil || delivery.Validate() != nil {
				return errors.New("stored notification delivery is invalid")
			}
			result = append(result, delivery)
			return nil
		})
	})
	sort.SliceStable(result, func(left, right int) bool { return result[left].UpdatedAt.After(result[right].UpdatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, err
}

func (store *Store) ClearTerminal() (int, error) {
	deleted := 0
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketDeliveries)
		var keys [][]byte
		eventIDs := map[string]struct{}{}
		if err := bucket.ForEach(func(key, wire []byte) error {
			var delivery Delivery
			if json.Unmarshal(wire, &delivery) != nil || delivery.Validate() != nil {
				return errors.New("stored notification delivery is invalid")
			}
			if delivery.Terminal() && delivery.Kind != KindTest {
				keys = append(keys, append([]byte(nil), key...))
				eventIDs[delivery.EventID] = struct{}{}
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range keys {
			if err := bucket.Delete(key); err != nil {
				return err
			}
			deleted++
		}
		for eventID := range eventIDs {
			remaining, err := deliveriesForEventTx(tx, eventID)
			if err != nil {
				return err
			}
			if len(remaining) == 0 {
				if err := tx.Bucket(bucketEvents).Delete([]byte(eventID)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return deleted, err
}

func (store *Store) ActiveLine(lineID string) (bool, error) {
	lineID = strings.TrimSpace(lineID)
	if !identifier(lineID, 128) {
		return false, errors.New("invalid notification line identity")
	}
	active := false
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDeliveries).ForEach(func(_, wire []byte) error {
			var delivery Delivery
			if json.Unmarshal(wire, &delivery) != nil || delivery.Validate() != nil {
				return errors.New("stored notification delivery is invalid")
			}
			if delivery.LineID == lineID && !delivery.Terminal() {
				active = true
			}
			return nil
		})
	})
	return active, err
}

// PurgeLine erases notification payload and delivery history while retaining
// source receipts as replay tombstones. A claimed delivery is never erased
// underneath an in-flight external request.
func (store *Store) PurgeLine(lineID string) error {
	lineID = strings.TrimSpace(lineID)
	if !identifier(lineID, 128) {
		return errors.New("invalid notification line identity")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketPurgedLines).Put([]byte(lineID), []byte{1}); err != nil {
			return err
		}
		var deliveryKeys, eventKeys [][]byte
		if err := tx.Bucket(bucketDeliveries).ForEach(func(key, wire []byte) error {
			var delivery Delivery
			if json.Unmarshal(wire, &delivery) != nil || delivery.Validate() != nil {
				return errors.New("stored notification delivery is invalid")
			}
			if delivery.LineID != lineID {
				return nil
			}
			if delivery.State == DeliverySending {
				return errors.New("sending notification cannot be purged")
			}
			deliveryKeys = append(deliveryKeys, append([]byte(nil), key...))
			eventKeys = append(eventKeys, []byte(delivery.EventID))
			return nil
		}); err != nil {
			return err
		}
		if err := tx.Bucket(bucketEvents).ForEach(func(key, wire []byte) error {
			var event Event
			if json.Unmarshal(wire, &event) != nil || event.Validate() != nil {
				return errors.New("stored notification event is invalid")
			}
			if event.LineID == lineID {
				eventKeys = append(eventKeys, append([]byte(nil), key...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range deliveryKeys {
			if err := tx.Bucket(bucketDeliveries).Delete(key); err != nil {
				return err
			}
		}
		for _, key := range eventKeys {
			if err := tx.Bucket(bucketEvents).Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *Store) EnqueueTest(operationID, channel string, now time.Time) (Delivery, bool, error) {
	operationID, now = strings.TrimSpace(operationID), now.UTC()
	if !identifier(operationID, 200) || !oneOf(channel, ChannelWebhook, ChannelTelegram, ChannelPushPlus) || now.IsZero() {
		return Delivery{}, false, errors.New("invalid notification test operation")
	}
	var delivery Delivery
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		if wire := tx.Bucket(bucketOperations).Get([]byte(operationID)); wire != nil {
			var operation testOperation
			if json.Unmarshal(wire, &operation) != nil || operation.OperationID != operationID || operation.Channel != channel {
				return ErrConflict
			}
			items, err := deliveriesForEventTx(tx, operation.EventID)
			if err != nil || len(items) != 1 {
				return errors.Join(err, errors.New("stored notification test is invalid"))
			}
			delivery = items[0]
			return nil
		}
		config, err := configFromTx(tx)
		if err != nil {
			return err
		}
		if !config.ChannelEnabled(channel, EventTest) {
			return errors.New("notification test channel is disabled")
		}
		event := Event{
			SchemaVersion: SchemaVersion, EventID: deterministicID("event", "test:"+operationID),
			SourceID: "test:" + operationID, Kind: KindTest, Type: EventTest,
			Title: "MDD notification test", Text: "MDD notification channel test",
			OccurredAt: now, IntakeRevision: config.Revision, Targets: []string{channel},
		}
		if event.Validate() != nil {
			return errors.New("invalid notification test event")
		}
		if err := putJSON(tx.Bucket(bucketEvents), []byte(event.EventID), event); err != nil {
			return err
		}
		delivery = Delivery{
			SchemaVersion: SchemaVersion, DeliveryID: deterministicID("delivery", event.EventID+"\x00"+channel),
			EventID: event.EventID, OperationID: operationID, Kind: KindTest, EventType: EventTest,
			Channel: channel, ConfigRevision: config.Revision, State: DeliveryPending,
			NotBefore: now, CreatedAt: now, UpdatedAt: now,
		}
		if err := putJSON(tx.Bucket(bucketDeliveries), []byte(delivery.DeliveryID), delivery); err != nil {
			return err
		}
		operation := testOperation{OperationID: operationID, Channel: channel, EventID: event.EventID}
		if err := putJSON(tx.Bucket(bucketOperations), []byte(operationID), operation); err != nil {
			return err
		}
		created = true
		return nil
	})
	return delivery, created, err
}

func configFromTx(tx *bolt.Tx) (Config, error) {
	var config Config
	wire := tx.Bucket(bucketConfig).Get(keyConfig)
	if json.Unmarshal(wire, &config) != nil || config.Validate() != nil {
		return Config{}, errors.New("stored notification config is invalid")
	}
	return config, nil
}

func deliveryFromTx(tx *bolt.Tx, deliveryID string) (Delivery, error) {
	var delivery Delivery
	wire := tx.Bucket(bucketDeliveries).Get([]byte(strings.TrimSpace(deliveryID)))
	if wire == nil {
		return delivery, ErrNotFound
	}
	if json.Unmarshal(wire, &delivery) != nil || delivery.Validate() != nil {
		return Delivery{}, errors.New("stored notification delivery is invalid")
	}
	return delivery, nil
}

func eventFromTx(tx *bolt.Tx, eventID string) (Event, error) {
	var event Event
	wire := tx.Bucket(bucketEvents).Get([]byte(strings.TrimSpace(eventID)))
	if wire == nil {
		return event, ErrNotFound
	}
	if json.Unmarshal(wire, &event) != nil || event.Validate() != nil {
		return Event{}, errors.New("stored notification event is invalid")
	}
	return event, nil
}

func deliveriesForEventTx(tx *bolt.Tx, eventID string) ([]Delivery, error) {
	result := []Delivery{}
	err := tx.Bucket(bucketDeliveries).ForEach(func(_, wire []byte) error {
		var delivery Delivery
		if json.Unmarshal(wire, &delivery) != nil || delivery.Validate() != nil {
			return errors.New("stored notification delivery is invalid")
		}
		if delivery.EventID == eventID {
			result = append(result, delivery)
		}
		return nil
	})
	return result, err
}

func finalizeEventTx(tx *bolt.Tx, eventID string) error {
	deliveries, err := deliveriesForEventTx(tx, eventID)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		if !delivery.Terminal() {
			return nil
		}
	}
	event, err := eventFromTx(tx, eventID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	event.LineName, event.CardID, event.MSISDN = "", "", ""
	event.Title, event.Text, event.Peer, event.Reminder = "", "", "", nil
	event.PayloadCleared = true
	return putJSON(tx.Bucket(bucketEvents), []byte(event.EventID), event)
}

func cancelStalePendingTx(tx *bolt.Tx, config Config, now time.Time) error {
	bucket := tx.Bucket(bucketDeliveries)
	var changed []Delivery
	if err := bucket.ForEach(func(_, wire []byte) error {
		var delivery Delivery
		if json.Unmarshal(wire, &delivery) != nil || delivery.Validate() != nil {
			return errors.New("stored notification delivery is invalid")
		}
		if delivery.State == DeliveryPending &&
			(delivery.ConfigRevision != config.Revision || !config.ChannelEnabled(delivery.Channel, delivery.EventType)) {
			delivery.State, delivery.Code = DeliveryCanceled, "notification_config_changed"
			delivery.UpdatedAt, delivery.FinishedAt = now, now
			changed = append(changed, delivery)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, delivery := range changed {
		if err := putJSON(bucket, []byte(delivery.DeliveryID), delivery); err != nil {
			return err
		}
		if err := finalizeEventTx(tx, delivery.EventID); err != nil {
			return err
		}
	}
	return nil
}

func recoverSendingTx(tx *bolt.Tx, now time.Time) error {
	bucket := tx.Bucket(bucketDeliveries)
	var changed []Delivery
	if err := bucket.ForEach(func(_, wire []byte) error {
		var delivery Delivery
		if json.Unmarshal(wire, &delivery) != nil || delivery.Validate() != nil {
			return errors.New("stored notification delivery is invalid")
		}
		if delivery.State == DeliverySending {
			delivery.State, delivery.Code = DeliveryUncertain, "notification_process_restarted"
			delivery.UpdatedAt, delivery.FinishedAt = now, now
			changed = append(changed, delivery)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, delivery := range changed {
		if err := putJSON(bucket, []byte(delivery.DeliveryID), delivery); err != nil {
			return err
		}
		if err := finalizeEventTx(tx, delivery.EventID); err != nil {
			return err
		}
	}
	return nil
}

func deterministicID(prefix, input string) string {
	digest := sha256.Sum256([]byte(input))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func eventSourceFingerprint(event Event) string {
	type stableReminder struct {
		ExpectedCardID   string `json:"expected_card_id"`
		ValidUntil       string `json:"valid_until"`
		Timezone         string `json:"timezone"`
		DaysBeforeExpiry int    `json:"days_before_expiry"`
	}
	type stableSource struct {
		SourceID   string          `json:"source_id"`
		Type       string          `json:"type"`
		LineID     string          `json:"line_id,omitempty"`
		CardID     string          `json:"card_id,omitempty"`
		Transport  string          `json:"transport,omitempty"`
		Peer       string          `json:"peer,omitempty"`
		Text       string          `json:"text,omitempty"`
		OccurredAt time.Time       `json:"occurred_at,omitempty"`
		Reminder   *stableReminder `json:"reminder,omitempty"`
	}
	stable := stableSource{SourceID: event.SourceID, Type: event.Type, LineID: event.LineID}
	switch event.Type {
	case EventIncomingSMS, EventIncomingCall:
		stable.CardID, stable.Transport, stable.Peer, stable.Text = event.CardID, event.Transport, event.Peer, event.Text
		stable.OccurredAt = event.OccurredAt.UTC()
	case EventActivationReminder:
		if event.Reminder != nil {
			stable.Reminder = &stableReminder{
				ExpectedCardID: event.Reminder.ExpectedCardID, ValidUntil: event.Reminder.ValidUntil,
				Timezone: event.Reminder.Timezone, DaysBeforeExpiry: event.Reminder.DaysBeforeExpiry,
			}
		}
	}
	digest := sha256.Sum256(mustJSON(stable))
	return hex.EncodeToString(digest[:])
}

func uintString(value uint64) string {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	return hex.EncodeToString(encoded[:])
}

func putJSON(bucket *bolt.Bucket, key []byte, value any) error {
	wire, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(wire) > 128<<10 {
		return errors.New("notification record exceeds 128 KiB")
	}
	return bucket.Put(key, wire)
}

func sameConfig(left, right Config) bool {
	left.Revision, right.Revision = 0, 0
	left.SchemaVersion, right.SchemaVersion = 0, 0
	return bytes.Equal(mustJSON(left), mustJSON(right))
}

func sameWebhook(left, right WebhookConfig) bool {
	left.ConfiguredAt, right.ConfiguredAt = time.Time{}, time.Time{}
	return bytes.Equal(mustJSON(left), mustJSON(right))
}

func sameTelegram(left, right TelegramConfig) bool {
	left.ConfiguredAt, right.ConfiguredAt = time.Time{}, time.Time{}
	return bytes.Equal(mustJSON(left), mustJSON(right))
}

func samePushPlus(left, right PushPlusConfig) bool {
	left.ConfiguredAt, right.ConfiguredAt = time.Time{}, time.Time{}
	return bytes.Equal(mustJSON(left), mustJSON(right))
}

func mustJSON(value any) []byte {
	wire, _ := json.Marshal(value)
	return wire
}

func cloneConfig(config Config) Config {
	config.Webhook.Headers = cloneStringMap(config.Webhook.Headers)
	if config.Imported != nil {
		proof := *config.Imported
		proof.Warnings = append([]string(nil), config.Imported.Warnings...)
		config.Imported = &proof
	}
	return config
}

func cloneEvent(event Event) Event {
	event.Targets = append([]string(nil), event.Targets...)
	if event.Reminder != nil {
		fence := *event.Reminder
		event.Reminder = &fence
	}
	return event
}

func cloneDeliveries(source []Delivery) []Delivery {
	return append([]Delivery(nil), source...)
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
