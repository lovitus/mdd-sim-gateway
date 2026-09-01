package allowance

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const storeSchemaVersion uint64 = 1

var (
	bucketMetadata  = []byte("metadata")
	bucketSnapshots = []byte("snapshots")
	bucketRules     = []byte("query_rules")
	bucketQueries   = []byte("queries")
	bucketActive    = []byte("active_queries")
	keySchema       = []byte("schema_version")
)

type Store struct {
	db        *bolt.DB
	closeOnce sync.Once
	closeErr  error
}

type ReconcileUpdate struct {
	Expected         Query
	SentAt           time.Time
	CorrelationUntil time.Time
	Parsed           map[string]string
	UpdatedAt        time.Time
	ReplyCount       int
	ReplyCode        string
	Complete         bool
	Stale            bool
}

func Open(path string, timeout time.Duration) (*Store, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || timeout <= 0 {
		return nil, errors.New("invalid allowance store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
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

func (store *Store) initialize() error {
	return store.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMetadata, bucketSnapshots, bucketRules, bucketQueries, bucketActive} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		var schema [8]byte
		binary.BigEndian.PutUint64(schema[:], storeSchemaVersion)
		stored := tx.Bucket(bucketMetadata).Get(keySchema)
		if stored == nil {
			return tx.Bucket(bucketMetadata).Put(keySchema, schema[:])
		}
		if !bytes.Equal(stored, schema[:]) {
			return errors.New("unsupported allowance store schema")
		}
		return nil
	})
}

func (store *Store) Snapshot(lineID string) (Snapshot, error) {
	lineID = strings.TrimSpace(lineID)
	if !validID(lineID) {
		return Snapshot{}, errors.New("invalid allowance line identity")
	}
	result := defaultSnapshot(lineID)
	err := store.db.View(func(tx *bolt.Tx) error {
		wire := tx.Bucket(bucketSnapshots).Get([]byte(lineID))
		if wire == nil {
			return nil
		}
		if json.Unmarshal(wire, &result) != nil || result.SchemaVersion != SchemaVersion ||
			result.LineID != lineID || result.Revision == 0 {
			return errors.New("stored allowance snapshot is invalid")
		}
		return nil
	})
	return result, err
}

func (store *Store) PutSnapshotExpected(lineID string, expected uint64, input Values,
	now time.Time) (Snapshot, bool, error) {
	lineID, now = strings.TrimSpace(lineID), now.UTC()
	if !validID(lineID) || now.IsZero() {
		return Snapshot{}, false, errors.New("invalid allowance snapshot")
	}
	var result Snapshot
	changed := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		current, err := snapshotFromBucket(tx.Bucket(bucketSnapshots), lineID)
		if err != nil {
			return err
		}
		result = current
		if current.Revision != expected {
			return ErrRevision
		}
		values, err := cleanValuesForUpdate(input, current.Values)
		if err != nil {
			return err
		}
		if sameValues(current.Values, values) {
			return nil
		}
		result.Values, result.Source, result.UpdatedAt = values, SourceManual, now
		result.Revision++
		changed = true
		return putJSON(tx.Bucket(bucketSnapshots), lineID, result)
	})
	return result, changed, err
}

func (store *Store) Rule(lineID string) (QueryRule, error) {
	lineID = strings.TrimSpace(lineID)
	if !validID(lineID) {
		return QueryRule{}, errors.New("invalid allowance line identity")
	}
	result := defaultRule(lineID)
	err := store.db.View(func(tx *bolt.Tx) error {
		var err error
		result, err = ruleFromBucket(tx.Bucket(bucketRules), lineID)
		return err
	})
	return result, err
}

func (store *Store) PutRuleExpected(lineID string, expected uint64, input QueryRule,
	now time.Time) (QueryRule, bool, error) {
	lineID, now = strings.TrimSpace(lineID), now.UTC()
	rule, err := cleanRule(lineID, input)
	if err != nil || now.IsZero() {
		return QueryRule{}, false, err
	}
	var result QueryRule
	changed := false
	err = store.db.Update(func(tx *bolt.Tx) error {
		current, err := ruleFromBucket(tx.Bucket(bucketRules), lineID)
		if err != nil {
			return err
		}
		result = current
		if current.Revision != expected {
			return ErrRevision
		}
		if sameRule(current, rule) {
			return nil
		}
		rule.Revision, rule.UpdatedAt = current.Revision+1, now
		result, changed = rule, true
		return putJSON(tx.Bucket(bucketRules), lineID, rule)
	})
	return result, changed, err
}

func (store *Store) DeleteRuleExpected(lineID string, expected uint64,
	now time.Time) (QueryRule, bool, error) {
	lineID, now = strings.TrimSpace(lineID), now.UTC()
	if !validID(lineID) || now.IsZero() {
		return QueryRule{}, false, errors.New("invalid allowance query rule reset")
	}
	var result QueryRule
	changed := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		current, err := ruleFromBucket(tx.Bucket(bucketRules), lineID)
		if err != nil {
			return err
		}
		result = current
		if current.Revision != expected {
			return ErrRevision
		}
		if current.Recipient == "" && current.Body == "" && current.Parser == ParserNone {
			return nil
		}
		result = defaultRule(lineID)
		result.Revision, result.UpdatedAt = current.Revision+1, now
		changed = true
		return putJSON(tx.Bucket(bucketRules), lineID, result)
	})
	return result, changed, err
}

func (store *Store) Query(lineID string) (Query, bool, error) {
	lineID = strings.TrimSpace(lineID)
	if !validID(lineID) {
		return Query{}, false, errors.New("invalid allowance line identity")
	}
	var result Query
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		queryID := tx.Bucket(bucketActive).Get([]byte(lineID))
		if queryID == nil {
			return nil
		}
		var err error
		result, err = queryFromBucket(tx.Bucket(bucketQueries), string(queryID))
		found = err == nil
		return err
	})
	return result, found, err
}

func (store *Store) QueryByID(queryID string) (Query, bool, error) {
	queryID = strings.TrimSpace(queryID)
	if !validID(queryID) {
		return Query{}, false, errors.New("invalid allowance query identity")
	}
	var result Query
	found := false
	err := store.db.View(func(tx *bolt.Tx) error {
		wire := tx.Bucket(bucketQueries).Get([]byte(queryID))
		if wire == nil {
			return nil
		}
		var err error
		result, err = queryFromBucket(tx.Bucket(bucketQueries), queryID)
		found = err == nil
		return err
	})
	return result, found, err
}

func (store *Store) BeginQuery(candidate Query, now time.Time) (Query, bool, error) {
	now = now.UTC()
	if err := validateQueryCandidate(candidate); err != nil || now.IsZero() {
		return Query{}, false, errors.New("invalid allowance query")
	}
	var result Query
	created := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		queries, active := tx.Bucket(bucketQueries), tx.Bucket(bucketActive)
		if prior := queries.Get([]byte(candidate.QueryID)); prior != nil {
			if json.Unmarshal(prior, &result) != nil {
				return errors.New("stored allowance query is invalid")
			}
			if result.RequestSHA256 != candidate.RequestSHA256 {
				return ErrQueryConflict
			}
			return nil
		}
		if currentID := active.Get([]byte(candidate.LineID)); currentID != nil {
			current, err := queryFromBucket(queries, string(currentID))
			if err != nil {
				return err
			}
			if now.Before(current.CorrelationUntil) || current.State == QueryPrepared || current.State == QuerySent {
				return ErrQueryActive
			}
		}
		candidate.SchemaVersion = SchemaVersion
		candidate.State = QueryPrepared
		candidate.CreatedAt = now
		candidate.SentAt = time.Time{}
		candidate.CorrelationUntil = now.Add(QueryWindow)
		candidate.ReplyCount = 0
		candidate.ReplyCode = ""
		if err := putJSON(queries, candidate.QueryID, candidate); err != nil {
			return err
		}
		if err := active.Put([]byte(candidate.LineID), []byte(candidate.QueryID)); err != nil {
			return err
		}
		result, created = candidate, true
		return nil
	})
	return result, created, err
}

func (store *Store) CloseQuery(lineID, queryID string) (Query, error) {
	lineID, queryID = strings.TrimSpace(lineID), strings.TrimSpace(queryID)
	var result Query
	err := store.db.Update(func(tx *bolt.Tx) error {
		active := tx.Bucket(bucketActive).Get([]byte(lineID))
		if string(active) != queryID {
			return ErrQueryChanged
		}
		current, err := queryFromBucket(tx.Bucket(bucketQueries), queryID)
		if err != nil {
			return err
		}
		result = current
		if current.State == QueryClosed {
			return nil
		}
		current.State, current.ReplyCode = QueryClosed, "user_closed"
		result = current
		return putJSON(tx.Bucket(bucketQueries), queryID, current)
	})
	return result, err
}

func (store *Store) AuthorizeDispatch(queryID, transport, lineID, expectedCardID,
	operationID, messageID, recipient, body string, now time.Time) (Query, error) {
	queryID, transport, lineID = strings.TrimSpace(queryID), strings.TrimSpace(transport), strings.TrimSpace(lineID)
	expectedCardID, operationID, messageID = strings.TrimSpace(expectedCardID), strings.TrimSpace(operationID), strings.TrimSpace(messageID)
	recipient, now = strings.TrimSpace(recipient), now.UTC()
	var result Query
	err := store.db.Update(func(tx *bolt.Tx) error {
		current, err := queryFromBucket(tx.Bucket(bucketQueries), queryID)
		if err != nil {
			return err
		}
		if (current.State != QueryPrepared && current.State != QuerySent) ||
			current.Transport != transport || current.LineID != lineID || current.ExpectedCardID != expectedCardID ||
			current.OperationID != operationID || current.MessageID != messageID ||
			current.Recipient != recipient || current.Body != body || now.IsZero() {
			return ErrQueryChanged
		}
		authorizedAt := now
		if current.DispatchAuthorizedAt.After(authorizedAt) {
			authorizedAt = current.DispatchAuthorizedAt
		}
		current.DispatchAuthorizedAt = authorizedAt
		minimumUntil := authorizedAt.Add(MaximumDispatchUncertainty + QueryWindow)
		if minimumUntil.After(current.CorrelationUntil) {
			current.CorrelationUntil = minimumUntil
		}
		result = current
		return putJSON(tx.Bucket(bucketQueries), queryID, current)
	})
	return result, err
}

func (store *Store) ApplyReconcile(update ReconcileUpdate) (Query, Snapshot, bool, error) {
	var query Query
	var snapshot Snapshot
	snapshotChanged := false
	err := store.db.Update(func(tx *bolt.Tx) error {
		current, err := queryFromBucket(tx.Bucket(bucketQueries), update.Expected.QueryID)
		if err != nil {
			return err
		}
		if !queryFenceEqual(current, update.Expected) ||
			(current.State != QueryPrepared && current.State != QuerySent) {
			return ErrQueryChanged
		}
		query = current
		if update.Stale {
			query.State, query.ReplyCode = QueryStale, "card_route_stale"
			return putJSON(tx.Bucket(bucketQueries), query.QueryID, query)
		}
		if current.State == QueryPrepared && !update.SentAt.IsZero() {
			query.State = QuerySent
			query.SentAt = update.SentAt.UTC()
			if update.CorrelationUntil.After(query.CorrelationUntil) {
				query.CorrelationUntil = update.CorrelationUntil.UTC()
			}
		}
		if update.ReplyCount > query.ReplyCount {
			query.ReplyCount = update.ReplyCount
		}
		if update.ReplyCode != "" {
			query.ReplyCode = update.ReplyCode
		}
		snapshot, err = snapshotFromBucket(tx.Bucket(bucketSnapshots), query.LineID)
		if err != nil {
			return err
		}
		if len(update.Parsed) != 0 {
			merged := snapshot.Values
			applyParsedValues(&merged, update.Parsed)
			merged, err = cleanValuesForUpdate(merged, snapshot.Values)
			if err != nil {
				return err
			}
			if !sameValues(snapshot.Values, merged) {
				snapshot.Values, snapshot.Source = merged, SourceSMS
				snapshot.UpdatedAt = update.UpdatedAt.UTC()
				snapshot.Revision++
				snapshotChanged = true
				if err := putJSON(tx.Bucket(bucketSnapshots), query.LineID, snapshot); err != nil {
					return err
				}
			}
		}
		if update.Complete {
			query.State = QueryReplied
		}
		return putJSON(tx.Bucket(bucketQueries), query.QueryID, query)
	})
	return query, snapshot, snapshotChanged, err
}

func snapshotFromBucket(bucket *bolt.Bucket, lineID string) (Snapshot, error) {
	result := defaultSnapshot(lineID)
	wire := bucket.Get([]byte(lineID))
	if wire == nil {
		return result, nil
	}
	if json.Unmarshal(wire, &result) != nil || result.SchemaVersion != SchemaVersion ||
		result.LineID != lineID || result.Revision == 0 {
		return Snapshot{}, errors.New("stored allowance snapshot is invalid")
	}
	return result, nil
}

func ruleFromBucket(bucket *bolt.Bucket, lineID string) (QueryRule, error) {
	result := defaultRule(lineID)
	wire := bucket.Get([]byte(lineID))
	if wire == nil {
		return result, nil
	}
	if json.Unmarshal(wire, &result) != nil || result.SchemaVersion != SchemaVersion ||
		result.LineID != lineID || result.Revision == 0 || !validParser(result.Parser) {
		return QueryRule{}, errors.New("stored allowance query rule is invalid")
	}
	return result, nil
}

func queryFromBucket(bucket *bolt.Bucket, queryID string) (Query, error) {
	var result Query
	wire := bucket.Get([]byte(queryID))
	if wire == nil || json.Unmarshal(wire, &result) != nil || result.SchemaVersion != SchemaVersion ||
		result.QueryID != queryID || validateStoredQuery(result) != nil {
		return Query{}, errors.New("stored allowance query is invalid")
	}
	return result, nil
}

func validateQueryCandidate(query Query) error {
	if !validID(query.QueryID) || !validID(query.LineID) || !validCardID(query.ExpectedCardID) ||
		(query.Transport != "vowifi" && query.Transport != "cellular") || query.RuleRevision == 0 ||
		!serviceNumber.MatchString(query.Recipient) || strings.TrimSpace(query.Body) == "" ||
		!validParser(query.Parser) || !validID(query.OperationID) || !validID(query.MessageID) ||
		len(query.RequestSHA256) != sha256HexLength {
		return errors.New("invalid allowance query fields")
	}
	return nil
}

const sha256HexLength = 64

func validateStoredQuery(query Query) error {
	if err := validateQueryCandidate(query); err != nil || query.CreatedAt.IsZero() || query.CorrelationUntil.IsZero() {
		return errors.New("invalid allowance query record")
	}
	switch query.State {
	case QueryPrepared, QuerySent, QueryClosed, QueryStale, QueryReplied:
	default:
		return errors.New("invalid allowance query state")
	}
	return nil
}

func putJSON(bucket *bolt.Bucket, key string, value any) error {
	wire, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put([]byte(key), wire)
}

func applyParsedValues(values *Values, parsed map[string]string) {
	for key, value := range parsed {
		switch key {
		case "balance":
			values.Balance = value
		case "sms_remaining":
			values.SMSRemaining = value
		case "data_remaining":
			values.DataRemaining = value
		case "voice_remaining":
			values.VoiceRemaining = value
		case "valid_until":
			if parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value)); err == nil &&
				parsed.Format("2006-01-02") == strings.TrimSpace(value) {
				values.ValidUntil = strings.TrimSpace(value)
			}
		case "activated_at":
			values.ActivatedAt = value
		}
	}
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.closeOnce.Do(func() { store.closeErr = store.db.Close() })
	return store.closeErr
}
