package providermessages

import (
	"encoding/binary"
	"errors"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// SyncPage is independent of the presentation window and producer timestamps.
// Gap is explicit when retained history cannot cover every committed sequence.
type SyncPage struct {
	Messages []Record `json:"messages"`
	Cursor   string   `json:"cursor"`
	More     bool     `json:"more"`
	Gap      bool     `json:"gap"`
	Initial  bool     `json:"initial"`
}

func (store *Store) Sync(after string, limit int) (SyncPage, error) {
	page := SyncPage{Messages: []Record{}, Initial: after == ""}
	if limit < 1 || limit > 500 {
		return page, ErrHistoryQuery
	}
	var stream string
	var sequence uint64
	if after != "" {
		parts := strings.Split(after, ":")
		if len(parts) != 2 || len(parts[0]) != 32 {
			return page, ErrHistoryQuery
		}
		var err error
		sequence, err = strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return page, ErrHistoryQuery
		}
		stream = parts[0]
	}
	err := store.db.View(func(tx *bolt.Tx) error {
		current := string(tx.Bucket(bucketMeta).Get(keySyncStream))
		bucket := tx.Bucket(bucketRecords)
		last := bucket.Sequence()
		if after != "" && (stream != current || sequence > last) {
			page.Gap, page.Initial = true, true
		}
		if page.Initial {
			// Seed the recent window without pretending to replay all historic notifications.
			sequence = 0
			if last > uint64(limit) {
				sequence = last - uint64(limit)
			}
		}
		if sequence == last {
			page.Cursor = current + ":" + strconv.FormatUint(last, 10)
			return nil
		}
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], sequence+1)
		cursor := bucket.Cursor()
		for k, v := cursor.Seek(key[:]); k != nil; k, v = cursor.Next() {
			if len(k) != 8 {
				return errors.New("invalid message sync sequence")
			}
			next := binary.BigEndian.Uint64(k)
			if len(page.Messages) == limit {
				page.More = true
				break
			}
			if next != sequence+1 && !page.Initial {
				page.Gap = true
			}
			record, err := historyRecord(tx, v)
			if err != nil {
				return err
			}
			page.Messages = append(page.Messages, record)
			sequence = next
		}
		if !page.More && sequence < last {
			if !page.Initial {
				page.Gap = true
			}
			sequence = last
		}
		page.Cursor = current + ":" + strconv.FormatUint(sequence, 10)
		return nil
	})
	return page, err
}
