package providermessages

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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
	bucketMeta           = []byte("metadata")
	bucketRecords        = []byte("records")
	bucketIDs            = []byte("event_ids")
	bucketLinks          = []byte("delivery_links")
	bucketNotify         = []byte("notification_source_outbox")
	bucketCellularNotify = []byte("cellular_notification_source_outbox_v1")
	bucketPurgedLines    = []byte("purged_lines_v1")
	keySchema            = []byte("schema_version")
	ErrConflict          = errors.New("message event ID conflict")
	ErrWindowTooLarge    = errors.New("message receive window is too large")
)

type Store struct {
	db        *bolt.DB
	closeOnce sync.Once
	closeErr  error
}

func OpenStore(path string, timeout time.Duration) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" || timeout <= 0 {
		return nil, errors.New("invalid message store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, statErr
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if created {
		if err := syncMessageParent(filepath.Dir(path)); err != nil {
			return nil, errors.Join(err, db.Close())
		}
	}
	return store, nil
}

func (store *Store) initialize() error {
	return store.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketRecords, bucketIDs, bucketLinks, bucketNotify, bucketCellularNotify, bucketPurgedLines} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		meta := tx.Bucket(bucketMeta)
		stored := meta.Get(keySchema)
		if stored == nil {
			var wire [8]byte
			binary.BigEndian.PutUint64(wire[:], storeSchemaVersion)
			return meta.Put(keySchema, wire[:])
		}
		if len(stored) != 8 || binary.BigEndian.Uint64(stored) != storeSchemaVersion {
			return errors.New("unsupported message store schema")
		}
		return nil
	})
}

func (store *Store) Accept(event Event, receivedAt time.Time) (Record, bool, error) {
	return store.accept(event, receivedAt, false, "", inferTransport(event))
}

// AcceptWithNotification is reserved for the real-time VoWiFi Provider
// ingress. Cellular list imports deliberately use Accept so an old SMS first
// discovered on a modem after an upgrade cannot be misreported as newly
// received.
func (store *Store) AcceptWithNotification(event Event, cardID string, receivedAt time.Time) (Record, bool, error) {
	return store.AcceptWithNotificationTransport(event, cardID, "vowifi", receivedAt)
}

func (store *Store) AcceptWithNotificationTransport(event Event, cardID, transport string, receivedAt time.Time) (Record, bool, error) {
	cardID = strings.TrimSpace(cardID)
	transport = strings.TrimSpace(transport)
	if !validCardID(cardID) || transport != "vowifi" && transport != "cellular" {
		return Record{}, false, errors.New("invalid message notification card identity")
	}
	return store.accept(event, receivedAt, true, cardID, transport)
}

func (store *Store) accept(event Event, receivedAt time.Time, enqueueNotification bool, notificationCardID, notificationTransport string) (Record, bool, error) {
	if err := event.Validate(); err != nil || receivedAt.IsZero() {
		if err != nil {
			return Record{}, false, err
		}
		return Record{}, false, errors.New("invalid message receive time")
	}
	event.ObservedAt = event.ObservedAt.UTC()
	receivedAt = receivedAt.UTC()
	fingerprintEvent := event
	// ProcessGeneration authorizes the submitting process but is not part of
	// the durable business identity. A new singleton provider process may adopt
	// an event left in its 0600 outbox after Core committed but before the old
	// process deleted it.
	fingerprintEvent.ProcessGeneration = ""
	rawFingerprint, err := json.Marshal(fingerprintEvent)
	if err != nil {
		return Record{}, false, err
	}
	var record Record
	stored := false
	err = store.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketPurgedLines).Get([]byte(event.LineID)) != nil {
			return errors.New("message line was permanently deleted")
		}
		if event.Kind == KindDelivery && strings.TrimSpace(event.MessageID) == "" {
			if messageID, part, found := resolveDeliveryLink(tx.Bucket(bucketLinks), event, notificationTransport); found {
				event.MessageID, event.Part = messageID, part
			}
		}
		record = Record{Event: event, Transport: notificationTransport, ReceivedAt: receivedAt}
		wire, err := json.Marshal(record)
		if err != nil {
			return err
		}
		ids := tx.Bucket(bucketIDs)
		key := eventIdentity(event)
		if prior := ids.Get(key); prior != nil {
			if !bytes.Equal(prior, rawFingerprint) {
				return ErrConflict
			}
			if enqueueNotification && event.Kind == KindReceived {
				return putNotificationSource(tx, event, notificationCardID, notificationTransport, receivedAt)
			}
			return nil
		}
		sequence, err := tx.Bucket(bucketRecords).NextSequence()
		if err != nil {
			return err
		}
		var recordKey [8]byte
		binary.BigEndian.PutUint64(recordKey[:], sequence)
		if err := tx.Bucket(bucketRecords).Put(recordKey[:], wire); err != nil {
			return err
		}
		if err := ids.Put(key, rawFingerprint); err != nil {
			return err
		}
		if enqueueNotification && event.Kind == KindReceived {
			if err := putNotificationSource(tx, event, notificationCardID, notificationTransport, receivedAt); err != nil {
				return err
			}
		}
		if event.Kind == KindSubmitted {
			if err := storeDeliveryLinks(tx.Bucket(bucketLinks), event, notificationTransport); err != nil {
				return err
			}
		}
		stored = true
		return nil
	})
	return record, stored, err
}

func putNotificationSource(tx *bolt.Tx, event Event, cardID, transport string, receivedAt time.Time) error {
	source := NotificationSource{
		SchemaVersion: NotificationSourceSchemaVersion,
		SourceID:      notificationSourceID(event, transport),
		LineID:        event.LineID,
		CardID:        cardID,
		Transport:     transport,
		Sender:        event.Sender,
		Body:          event.Body,
		ReceivedAt:    receivedAt,
	}
	if err := source.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return err
	}
	bucket := tx.Bucket(bucketNotify)
	if transport == "cellular" {
		bucket = tx.Bucket(bucketCellularNotify)
	}
	if prior := bucket.Get([]byte(source.SourceID)); prior != nil {
		var existing NotificationSource
		if json.Unmarshal(prior, &existing) != nil || existing.Validate() != nil {
			return ErrConflict
		}
		if existing.Acked {
			return nil
		}
		if existing.SourceID != source.SourceID || existing.LineID != source.LineID || existing.CardID != source.CardID ||
			existing.Transport != source.Transport || existing.Sender != source.Sender || existing.Body != source.Body {
			return ErrConflict
		}
		return nil
	}
	return bucket.Put([]byte(source.SourceID), encoded)
}

// PendingNotificationSources returns only new received-message facts written
// transactionally with their durable business record. Existing history is
// never scanned or retroactively enqueued during an upgrade.
func (store *Store) PendingNotificationSources(limit int) ([]NotificationSource, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("notification source limit must be between 1 and 500")
	}
	result := make([]NotificationSource, 0, limit)
	err := store.db.View(func(tx *bolt.Tx) error {
		for _, bucketName := range [][]byte{bucketNotify, bucketCellularNotify} {
			if err := tx.Bucket(bucketName).ForEach(func(_, value []byte) error {
				if len(result) >= limit {
					return nil
				}
				var source NotificationSource
				if json.Unmarshal(value, &source) != nil || source.Validate() != nil {
					return errors.New("stored notification source is invalid")
				}
				if !source.Acked {
					result = append(result, source)
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].ReceivedAt.Equal(result[right].ReceivedAt) {
			return result[left].SourceID < result[right].SourceID
		}
		return result[left].ReceivedAt.Before(result[right].ReceivedAt)
	})
	return result, err
}

func (store *Store) AckNotificationSource(sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if !identifier(sourceID) {
		return errors.New("invalid notification source identity")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		if strings.HasPrefix(sourceID, "cellular-sms-") {
			bucket := tx.Bucket(bucketCellularNotify)
			wire := bucket.Get([]byte(sourceID))
			if wire == nil {
				return nil
			}
			var source NotificationSource
			if json.Unmarshal(wire, &source) != nil || source.Validate() != nil {
				return errors.New("stored notification source is invalid")
			}
			if source.Acked {
				return nil
			}
			source.Acked, source.CardID, source.Sender, source.Body = true, "", "", ""
			encoded, err := json.Marshal(source)
			if err != nil {
				return err
			}
			return bucket.Put([]byte(sourceID), encoded)
		}
		if err := tx.Bucket(bucketNotify).Delete([]byte(sourceID)); err != nil {
			return err
		}
		return tx.Bucket(bucketCellularNotify).Delete([]byte(sourceID))
	})
}

type deliveryLink struct {
	MessageID string `json:"message_id"`
	Part      int    `json:"part"`
}

func storeDeliveryLinks(bucket *bolt.Bucket, event Event, transport string) error {
	wire, err := json.Marshal(deliveryLink{MessageID: event.MessageID, Part: event.Part})
	if err != nil {
		return err
	}
	for _, key := range deliveryKeys(event, transport) {
		if err := bucket.Put(key, wire); err != nil {
			return err
		}
	}
	return nil
}

func resolveDeliveryLink(bucket *bolt.Bucket, event Event, transport string) (string, int, bool) {
	for _, key := range deliveryKeys(event, transport) {
		value := bucket.Get(key)
		if value == nil {
			continue
		}
		var link deliveryLink
		if json.Unmarshal(value, &link) == nil && identifier(link.MessageID) && link.Part > 0 {
			return link.MessageID, link.Part, true
		}
	}
	// Pre-transport stores used unscoped links. Fall back only while neither
	// transport has a scoped link for the same correlation key; otherwise the
	// old value is ambiguous and must not cross-associate a delivery.
	legacy := legacyDeliveryKeys(event)
	for index, key := range legacy {
		if bucket.Get(deliveryKeys(event, "vowifi")[index]) != nil || bucket.Get(deliveryKeys(event, "cellular")[index]) != nil {
			continue
		}
		value := bucket.Get(key)
		if value == nil {
			continue
		}
		var link deliveryLink
		if json.Unmarshal(value, &link) == nil && identifier(link.MessageID) && link.Part > 0 {
			return link.MessageID, link.Part, true
		}
	}
	return "", 0, false
}

func deliveryKeys(event Event, transport string) [][]byte {
	prefix := event.LineID + "\x00" + transport + "\x00"
	return correlationKeys(prefix, event)
}

func legacyDeliveryKeys(event Event) [][]byte {
	return correlationKeys(event.LineID+"\x00", event)
}

func correlationKeys(prefix string, event Event) [][]byte {
	keys := make([][]byte, 0, 3)
	for _, value := range []string{event.InReplyTo, event.CallID} {
		if value = strings.TrimSpace(value); value != "" {
			keys = append(keys, []byte(prefix+"call:"+value))
		}
	}
	if event.RPMR > 0 {
		keys = append(keys, []byte(fmt.Sprintf("%smr:%d", prefix, event.RPMR)))
	}
	return keys
}

func (store *Store) List(lineID string, limit int) ([]Record, error) {
	return store.ListTransport(lineID, "", limit)
}

func (store *Store) ListTransport(lineID, transport string, limit int) ([]Record, error) {
	lineID = strings.TrimSpace(lineID)
	transport = strings.TrimSpace(transport)
	if transport != "" && transport != "vowifi" && transport != "cellular" {
		return nil, errors.New("message transport is invalid")
	}
	if limit <= 0 || limit > 500 {
		return nil, errors.New("message list limit must be between 1 and 500")
	}
	result := make([]Record, 0, limit)
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketRecords).Cursor()
		for key, value := cursor.Last(); key != nil && len(result) < limit; key, value = cursor.Prev() {
			var record Record
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode message record: %w", err)
			}
			if record.Kind == KindDelivery && strings.TrimSpace(record.MessageID) == "" {
				if messageID, part, found := resolveDeliveryLink(tx.Bucket(bucketLinks), record.Event, inferTransport(record.Event)); found {
					record.MessageID, record.Part = messageID, part
				}
			}
			if record.Transport == "" {
				record.Transport = inferTransport(record.Event)
			}
			if (lineID == "" || record.LineID == lineID) && (transport == "" || record.Transport == transport) {
				result = append(result, record)
			}
		}
		return nil
	})
	sort.SliceStable(result, func(left, right int) bool { return result[left].ReceivedAt.Before(result[right].ReceivedAt) })
	return result, err
}

// DeleteHistory removes presentation records only. Event-ID receipts,
// notification outbox entries and delivery correlation remain intact so a
// late producer replay cannot recreate a deleted message or trigger I/O.
func (store *Store) DeleteHistory(lineID, transport, peer string, eventIDs []string, all bool) (int, error) {
	lineID, transport, peer = strings.TrimSpace(lineID), strings.TrimSpace(transport), strings.TrimSpace(peer)
	if !identifier(lineID) || (transport != "" && transport != "vowifi" && transport != "cellular") ||
		(all && (peer != "" || len(eventIDs) != 0)) || (!all && peer == "" && len(eventIDs) == 0) || len(eventIDs) > 500 {
		return 0, errors.New("invalid message history deletion")
	}
	wanted := make(map[string]struct{}, len(eventIDs))
	for _, id := range eventIDs {
		if !identifier(id) {
			return 0, errors.New("invalid message event identity")
		}
		wanted[id] = struct{}{}
	}
	deleted := 0
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketRecords)
		cursor := bucket.Cursor()
		keys := make([][]byte, 0)
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var record Record
			if json.Unmarshal(value, &record) != nil || record.Event.Validate() != nil {
				return errors.New("stored message record is invalid")
			}
			if record.Transport == "" {
				record.Transport = inferTransport(record.Event)
			}
			if record.LineID != lineID || transport != "" && record.Transport != transport {
				continue
			}
			matches := all
			if peer != "" {
				matches = strings.TrimSpace(record.Sender) == peer || strings.TrimSpace(record.Recipient) == peer
			}
			if _, exact := wanted[record.EventID]; exact {
				matches = true
			}
			if matches {
				keys = append(keys, append([]byte(nil), key...))
			}
		}
		for _, key := range keys {
			if err := bucket.Delete(key); err != nil {
				return err
			}
			deleted++
		}
		return nil
	})
	return deleted, err
}

// PurgeLine removes all user-visible and queued payload for a permanently
// deleted line. The line tombstone rejects old Provider retries without
// retaining the full-event values used by the normal deduplication index.
func (store *Store) PurgeLine(lineID string) error {
	lineID = strings.TrimSpace(lineID)
	if !identifier(lineID) {
		return errors.New("invalid message purge line identity")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketPurgedLines).Put([]byte(lineID), []byte{1}); err != nil {
			return err
		}
		var identityKeys [][]byte
		for _, bucketName := range [][]byte{bucketRecords, bucketNotify, bucketCellularNotify} {
			bucket := tx.Bucket(bucketName)
			var keys [][]byte
			if err := bucket.ForEach(func(key, value []byte) error {
				matches := false
				if bytes.Equal(bucketName, bucketRecords) {
					var record Record
					if json.Unmarshal(value, &record) != nil || record.Event.Validate() != nil {
						return errors.New("stored message record is invalid")
					}
					matches = record.LineID == lineID
					if matches {
						identityKeys = append(identityKeys, append([]byte(nil), eventIdentity(record.Event)...))
					}
				} else {
					var source NotificationSource
					if json.Unmarshal(value, &source) != nil || source.Validate() != nil {
						return errors.New("stored notification source is invalid")
					}
					matches = source.LineID == lineID
				}
				if matches {
					keys = append(keys, append([]byte(nil), key...))
				}
				return nil
			}); err != nil {
				return err
			}
			for _, key := range keys {
				if err := bucket.Delete(key); err != nil {
					return err
				}
			}
		}
		for _, key := range identityKeys {
			if err := tx.Bucket(bucketIDs).Delete(key); err != nil {
				return err
			}
		}
		links := tx.Bucket(bucketLinks)
		prefix := []byte(lineID + "\x00")
		cursor := links.Cursor()
		var linkKeys [][]byte
		for key, _ := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, _ = cursor.Next() {
			linkKeys = append(linkKeys, append([]byte(nil), key...))
		}
		for _, key := range linkKeys {
			if err := links.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
}

// RetainLine freezes existing user-visible history while removing queued
// notification payload. The tombstone prevents a late Provider retry from
// appending to the retired business identity.
func (store *Store) RetainLine(lineID string) error {
	lineID = strings.TrimSpace(lineID)
	if !identifier(lineID) {
		return errors.New("invalid message retention line identity")
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketPurgedLines).Put([]byte(lineID), []byte{1}); err != nil {
			return err
		}
		for _, bucketName := range [][]byte{bucketNotify, bucketCellularNotify} {
			bucket := tx.Bucket(bucketName)
			var keys [][]byte
			if err := bucket.ForEach(func(key, value []byte) error {
				var source NotificationSource
				if json.Unmarshal(value, &source) != nil || source.Validate() != nil {
					return errors.New("stored notification source is invalid")
				}
				if source.LineID == lineID {
					keys = append(keys, append([]byte(nil), key...))
				}
				return nil
			}); err != nil {
				return err
			}
			for _, key := range keys {
				if err := bucket.Delete(key); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func inferTransport(event Event) string {
	if event.ProviderID == "cellular" || strings.HasPrefix(event.EventID, "cellular-") {
		return "cellular"
	}
	return "vowifi"
}

// Window returns an exact bounded slice selected by Core receive time. It
// scans the durable store rather than trusting producer/device timestamps or
// silently truncating the newest N records.
func (store *Store) Window(lineID string, start, end time.Time, limit int) ([]Record, error) {
	lineID = strings.TrimSpace(lineID)
	start, end = start.UTC(), end.UTC()
	if !identifier(lineID) || start.IsZero() || end.Before(start) || limit <= 0 || limit > 2000 {
		return nil, errors.New("invalid message receive window")
	}
	result := make([]Record, 0)
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketRecords).Cursor()
		for key, value := cursor.Last(); key != nil; key, value = cursor.Prev() {
			var record Record
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode message record: %w", err)
			}
			if record.LineID != lineID || record.ReceivedAt.Before(start) || record.ReceivedAt.After(end) {
				continue
			}
			result = append(result, record)
			if len(result) > limit {
				return ErrWindowTooLarge
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].ReceivedAt.Equal(result[right].ReceivedAt) {
			return result[left].EventID < result[right].EventID
		}
		return result[left].ReceivedAt.Before(result[right].ReceivedAt)
	})
	return result, nil
}

// Find returns one durable event by its exact business identity. It is used
// to make a very old cellular submission replay idempotent without relying on
// the bounded recent-message projection.
func (store *Store) Find(lineID, providerID, eventID string) (Record, bool, error) {
	if !identifier(lineID) || !identifier(providerID) || !identifier(eventID) {
		return Record{}, false, errors.New("invalid message event identity")
	}
	var result Record
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketRecords).Cursor()
		for key, value := cursor.Last(); key != nil; key, value = cursor.Prev() {
			var record Record
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("decode message record: %w", err)
			}
			if record.LineID == lineID && record.ProviderID == providerID && record.EventID == eventID {
				result, found = record, true
				return nil
			}
		}
		return nil
	})
	return result, found, err
}

// FindEvent provides migration-safe lookup for cellular facts that older
// releases keyed by the source Agent ID. New cellular facts use the stable
// provider identity "cellular" so moving a SIM between Agents cannot duplicate
// the same PDU fingerprint.
func (store *Store) FindEvent(lineID, eventID string) (Record, bool, error) {
	if lineID != "" && !identifier(lineID) || !identifier(eventID) {
		return Record{}, false, errors.New("invalid message event identity")
	}
	var result Record
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRecords).ForEach(func(_, value []byte) error {
			var record Record
			if json.Unmarshal(value, &record) != nil || record.Event.Validate() != nil {
				return errors.New("stored message record is invalid")
			}
			if lineID != "" && record.LineID != lineID || record.EventID != eventID {
				return nil
			}
			if found {
				left, right := result.Event, record.Event
				left.ProcessGeneration, right.ProcessGeneration = "", ""
				if strings.HasPrefix(eventID, "cellular-") {
					left.ProviderID, right.ProviderID = "", ""
				}
				if left != right {
					return ErrConflict
				}
			}
			result, found = record, true
			return nil
		})
	})
	return result, found, err
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.closeOnce.Do(func() { store.closeErr = store.db.Close() })
	return store.closeErr
}

func eventIdentity(event Event) []byte {
	return []byte(event.LineID + "\x00" + event.ProviderID + "\x00" + event.EventID)
}

func notificationSourceID(event Event, transport string) string {
	digest := sha256.Sum256(eventIdentity(event))
	return transport + "-sms-" + fmt.Sprintf("%x", digest[:16])
}
