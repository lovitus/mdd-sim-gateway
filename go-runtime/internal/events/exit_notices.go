package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
	bolt "go.etcd.io/bbolt"
)

var exitNoticePrefix = []byte("exit-notice-v1\x00")

func exitNoticeKey(id string) []byte {
	return append(append([]byte(nil), exitNoticePrefix...), []byte(id)...)
}

func validExitNotice(notice recovery.ExitNotice) bool {
	return len(notice.ID) == 64 && strings.TrimLeft(notice.ID, "0123456789abcdef") == "" && validRecoveryLine(notice.LineID) &&
		len(notice.CardID) >= 4 && len(notice.CardID) <= 32 && strings.TrimLeft(notice.CardID, "0123456789") == "" &&
		len(notice.LineName) <= 256 && len(notice.MSISDN) <= 128 && len(notice.Text) > 0 && len(notice.Text) <= 8192 && !notice.OccurredAt.IsZero()
}

func putExitNotice(tx *bolt.Tx, notice *recovery.ExitNotice, lineID string) error {
	if notice == nil {
		return nil
	}
	if !validExitNotice(*notice) || notice.LineID != lineID {
		return errors.New("invalid exit recovery notice")
	}
	payload, err := json.Marshal(notice)
	if err != nil {
		return err
	}
	bucket := tx.Bucket(bucketMetadata)
	key := exitNoticeKey(notice.ID)
	if prior := bucket.Get(key); prior != nil && !bytes.Equal(prior, payload) {
		return errors.New("exit notice identity changed")
	}
	return bucket.Put(key, payload)
}

func (store *BoltStore) PendingExitRecoveryNotices(limit int) ([]recovery.ExitNotice, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid exit notice limit")
	}
	result := []recovery.ExitNotice{}
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketMetadata).Cursor()
		for key, payload := cursor.Seek(exitNoticePrefix); key != nil && bytes.HasPrefix(key, exitNoticePrefix) && len(result) < limit; key, payload = cursor.Next() {
			var notice recovery.ExitNotice
			if json.Unmarshal(payload, &notice) != nil || !validExitNotice(notice) {
				return errors.New("stored exit notice is invalid")
			}
			result = append(result, notice)
		}
		return nil
	})
	return result, err
}

func (store *BoltStore) AckExitRecoveryNotice(id string) error {
	if len(id) != 64 || strings.TrimLeft(id, "0123456789abcdef") != "" {
		return errors.New("invalid exit notice ID")
	}
	return store.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketMetadata).Delete(exitNoticeKey(id)) })
}

func purgeExitNotices(tx *bolt.Tx, lineID string) error {
	bucket := tx.Bucket(bucketMetadata)
	cursor := bucket.Cursor()
	var keys [][]byte
	for key, payload := cursor.Seek(exitNoticePrefix); key != nil && bytes.HasPrefix(key, exitNoticePrefix); key, payload = cursor.Next() {
		var notice recovery.ExitNotice
		if json.Unmarshal(payload, &notice) != nil {
			return errors.New("stored exit notice is invalid")
		}
		if notice.LineID == lineID {
			keys = append(keys, append([]byte(nil), key...))
		}
	}
	for _, key := range keys {
		if err := bucket.Delete(key); err != nil {
			return err
		}
	}
	return nil
}
