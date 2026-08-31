package linecatalog

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	metadataBucket           = []byte("metadata")
	linesBucket              = []byte("lines")
	cardsBucket              = []byte("cards")
	runtimeIntentsBucket     = []byte("runtime_intents")
	rawModemBindingsBucket   = []byte("raw_modem_bindings")
	schemaKey                = []byte("schema")
	revisionKey              = []byte("revision")
	runtimeIntentRevisionKey = []byte("runtime_intent_revision")
	rawModemRevisionKey      = []byte("raw_modem_revision")
	importKey                = []byte("legacy_import")
	ErrNotFound              = errors.New("line not found")
	ErrAlreadyExists         = errors.New("line already exists")
	ErrCardInUse             = errors.New("card identity belongs to another line")
	ErrNotEmpty              = errors.New("line catalog is not empty")
	ErrRevision              = errors.New("line catalog revision does not match")
	ErrRawModemRevision      = errors.New("raw modem binding revision does not match")
	ErrRawModemBindingInUse  = errors.New("raw modem source binding belongs to another line")
)

type ImportReceipt struct {
	SourceSHA256       string    `json:"source_sha256"`
	EgressSourceSHA256 string    `json:"egress_source_sha256,omitempty"`
	LineCount          int       `json:"line_count"`
	ImportedAt         time.Time `json:"imported_at"`
}

type Store struct{ db *bolt.DB }

func Open(path string, timeout time.Duration) (*Store, error) {
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid line catalog configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("open line catalog: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	store := &Store{db: db}
	if err := store.initialize(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if created {
		if err := syncParentDirectory(filepath.Dir(path)); err != nil {
			return nil, errors.Join(err, db.Close())
		}
	}
	return store, nil
}

func (store *Store) initialize() error {
	return store.db.Update(func(transaction *bolt.Tx) error {
		metadata, err := transaction.CreateBucketIfNotExists(metadataBucket)
		if err != nil {
			return err
		}
		schema := metadata.Get(schemaKey)
		if schema == nil {
			if err := metadata.Put(schemaKey, uint64Bytes(SchemaVersion)); err != nil {
				return err
			}
		} else if bytesUint64(schema) != SchemaVersion {
			return fmt.Errorf("unsupported line catalog schema %d", bytesUint64(schema))
		}
		if _, err := transaction.CreateBucketIfNotExists(linesBucket); err != nil {
			return err
		}
		if _, err := transaction.CreateBucketIfNotExists(cardsBucket); err != nil {
			return err
		}
		if _, err := transaction.CreateBucketIfNotExists(runtimeIntentsBucket); err != nil {
			return err
		}
		if _, err := transaction.CreateBucketIfNotExists(rawModemBindingsBucket); err != nil {
			return err
		}
		if metadata.Get(revisionKey) == nil {
			key, _ := transaction.Bucket(linesBucket).Cursor().First()
			if key != nil {
				return errors.New("line catalog revision is missing")
			}
			if err := metadata.Put(revisionKey, uint64Bytes(1)); err != nil {
				return err
			}
		}
		if metadata.Get(runtimeIntentRevisionKey) == nil {
			if err := metadata.Put(runtimeIntentRevisionKey, uint64Bytes(1)); err != nil {
				return err
			}
		}
		if metadata.Get(rawModemRevisionKey) == nil {
			if err := metadata.Put(rawModemRevisionKey, uint64Bytes(1)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *Store) Put(input Line) (Line, error) {
	line, _, err := store.put(input, nil)
	return line, err
}

func (store *Store) PutExpected(input Line, expectedRevision uint64) (Line, uint64, error) {
	return store.put(input, &expectedRevision)
}

// CreateExpected inserts a new line only when both the catalog revision and
// the generated line identity are still unused. Unlike PutExpected it can
// never turn a bootstrap request into an update of an existing line.
func (store *Store) CreateExpected(input Line, expectedRevision uint64) (Line, uint64, error) {
	line := cloneLine(input)
	if err := line.normalizeAndValidate(); err != nil {
		return Line{}, 0, err
	}
	payload, err := json.Marshal(line)
	if err != nil {
		return Line{}, 0, err
	}
	var revision uint64
	err = store.db.Update(func(transaction *bolt.Tx) error {
		lines, cards := transaction.Bucket(linesBucket), transaction.Bucket(cardsBucket)
		metadata := transaction.Bucket(metadataBucket)
		revision = bytesUint64(metadata.Get(revisionKey))
		if revision != expectedRevision {
			return ErrRevision
		}
		if lines.Get([]byte(line.ID)) != nil {
			return ErrAlreadyExists
		}
		if cards.Get([]byte(line.CardID)) != nil {
			return ErrCardInUse
		}
		if err := lines.Put([]byte(line.ID), payload); err != nil {
			return err
		}
		if err := cards.Put([]byte(line.CardID), []byte(line.ID)); err != nil {
			return err
		}
		revision++
		return metadata.Put(revisionKey, uint64Bytes(revision))
	})
	return cloneLine(line), revision, err
}

func (store *Store) put(input Line, expectedRevision *uint64) (Line, uint64, error) {
	line := cloneLine(input)
	if err := line.normalizeAndValidate(); err != nil {
		return Line{}, 0, err
	}
	payload, err := json.Marshal(line)
	if err != nil {
		return Line{}, 0, err
	}
	var revision uint64
	err = store.db.Update(func(transaction *bolt.Tx) error {
		lines, cards := transaction.Bucket(linesBucket), transaction.Bucket(cardsBucket)
		metadata := transaction.Bucket(metadataBucket)
		revision = bytesUint64(metadata.Get(revisionKey))
		if expectedRevision != nil && revision != *expectedRevision {
			return ErrRevision
		}
		if owner := cards.Get([]byte(line.CardID)); owner != nil && string(owner) != line.ID {
			return ErrCardInUse
		}
		if previous := lines.Get([]byte(line.ID)); previous != nil {
			var old Line
			if json.Unmarshal(previous, &old) != nil {
				return errors.New("stored line is corrupt")
			}
			if old.CardID != line.CardID {
				if err := cards.Delete([]byte(old.CardID)); err != nil {
					return err
				}
			}
		}
		if err := lines.Put([]byte(line.ID), payload); err != nil {
			return err
		}
		if err := cards.Put([]byte(line.CardID), []byte(line.ID)); err != nil {
			return err
		}
		revision++
		return metadata.Put(revisionKey, uint64Bytes(revision))
	})
	return cloneLine(line), revision, err
}

func (store *Store) Get(id string) (Line, error) {
	line, _, err := store.GetWithRevision(id)
	return line, err
}

func (store *Store) GetWithRevision(id string) (Line, uint64, error) {
	var line Line
	var revision uint64
	err := store.db.View(func(transaction *bolt.Tx) error {
		payload := transaction.Bucket(linesBucket).Get([]byte(id))
		if payload == nil {
			return ErrNotFound
		}
		if err := json.Unmarshal(payload, &line); err != nil {
			return errors.New("stored line is corrupt")
		}
		revision = bytesUint64(transaction.Bucket(metadataBucket).Get(revisionKey))
		return line.normalizeAndValidate()
	})
	return cloneLine(line), revision, err
}

func (store *Store) Snapshot() (Snapshot, error) {
	result := Snapshot{SchemaVersion: SchemaVersion, Lines: []Line{}}
	err := store.db.View(func(transaction *bolt.Tx) error {
		result.Revision = bytesUint64(transaction.Bucket(metadataBucket).Get(revisionKey))
		return transaction.Bucket(linesBucket).ForEach(func(_, payload []byte) error {
			var line Line
			if err := json.Unmarshal(payload, &line); err != nil {
				return errors.New("stored line is corrupt")
			}
			if err := line.normalizeAndValidate(); err != nil {
				return errors.New("stored line is invalid")
			}
			result.Lines = append(result.Lines, cloneLine(line))
			return nil
		})
	})
	sort.Slice(result.Lines, func(left, right int) bool { return result.Lines[left].ID < result.Lines[right].ID })
	return result, err
}

// RuntimeIntent returns the durable operator intent for one VoWiFi runtime.
// A missing value is intentionally distinct from false: during migration the
// reconciler adopts the current Provider state once, without starting or
// stopping anything, and persists that observation as the initial intent.
func (store *Store) RuntimeIntent(id string) (enabled, found bool, revision uint64, err error) {
	err = store.db.View(func(transaction *bolt.Tx) error {
		if transaction.Bucket(linesBucket).Get([]byte(id)) == nil {
			return ErrNotFound
		}
		value := transaction.Bucket(runtimeIntentsBucket).Get([]byte(id))
		if value != nil {
			if len(value) != 1 || (value[0] != 0 && value[0] != 1) {
				return errors.New("stored runtime intent is corrupt")
			}
			found, enabled = true, value[0] == 1
		}
		revision = bytesUint64(transaction.Bucket(metadataBucket).Get(runtimeIntentRevisionKey))
		return nil
	})
	return
}

// SetRuntimeIntent changes only the VoWiFi runtime intent. It deliberately
// does not advance the line-catalog revision used by Provider configuration
// apply, because starting or stopping a runtime does not change its config.
// lineEnabled is returned so the public control path can persist a future
// intent while refusing to bypass a globally disabled line right now.
func (store *Store) SetRuntimeIntent(id string, enabled bool) (lineEnabled, changed bool, revision uint64, err error) {
	err = store.db.Update(func(transaction *bolt.Tx) error {
		payload := transaction.Bucket(linesBucket).Get([]byte(id))
		if payload == nil {
			return ErrNotFound
		}
		var line Line
		if json.Unmarshal(payload, &line) != nil || line.normalizeAndValidate() != nil {
			return errors.New("stored line is corrupt")
		}
		lineEnabled = line.Enabled
		intents := transaction.Bucket(runtimeIntentsBucket)
		current := intents.Get([]byte(id))
		desired := byte(0)
		if enabled {
			desired = 1
		}
		metadata := transaction.Bucket(metadataBucket)
		revision = bytesUint64(metadata.Get(runtimeIntentRevisionKey))
		if len(current) == 1 && current[0] == desired {
			return nil
		}
		if err := intents.Put([]byte(id), []byte{desired}); err != nil {
			return err
		}
		changed = true
		revision++
		return metadata.Put(runtimeIntentRevisionKey, uint64Bytes(revision))
	})
	return
}

func (store *Store) ImportEmpty(inputs []Line, receipt ImportReceipt) error {
	if len(inputs) == 0 || len(receipt.SourceSHA256) != 64 || len(receipt.EgressSourceSHA256) != 64 ||
		receipt.LineCount != len(inputs) || receipt.ImportedAt.IsZero() {
		return errors.New("invalid legacy import")
	}
	lines := make([]Line, len(inputs))
	ids, cards := make(map[string]struct{}, len(inputs)), make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		lines[index] = cloneLine(input)
		if err := lines[index].normalizeAndValidate(); err != nil {
			return fmt.Errorf("line %q: %w", input.ID, err)
		}
		if _, exists := ids[lines[index].ID]; exists {
			return errors.New("legacy import contains duplicate line IDs")
		}
		if _, exists := cards[lines[index].CardID]; exists {
			return errors.New("legacy import contains duplicate card identities")
		}
		ids[lines[index].ID], cards[lines[index].CardID] = struct{}{}, struct{}{}
	}
	receiptPayload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return store.db.Update(func(transaction *bolt.Tx) error {
		lineBucket, cardBucket := transaction.Bucket(linesBucket), transaction.Bucket(cardsBucket)
		key, _ := lineBucket.Cursor().First()
		if key != nil || transaction.Bucket(metadataBucket).Get(importKey) != nil {
			return ErrNotEmpty
		}
		for _, line := range lines {
			payload, err := json.Marshal(line)
			if err != nil {
				return err
			}
			if err := lineBucket.Put([]byte(line.ID), payload); err != nil {
				return err
			}
			if err := cardBucket.Put([]byte(line.CardID), []byte(line.ID)); err != nil {
				return err
			}
		}
		metadata := transaction.Bucket(metadataBucket)
		if err := metadata.Put(importKey, receiptPayload); err != nil {
			return err
		}
		return incrementRevision(metadata)
	})
}

func incrementRevision(metadata *bolt.Bucket) error {
	revision := bytesUint64(metadata.Get(revisionKey)) + 1
	return metadata.Put(revisionKey, uint64Bytes(revision))
}

func uint64Bytes(value uint64) []byte {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, value)
	return payload
}

func bytesUint64(payload []byte) uint64 {
	if len(payload) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(payload)
}
