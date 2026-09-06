package events

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/diagnosticlog"
	bolt "go.etcd.io/bbolt"
)

type DiagnosticEntry struct {
	ReceivedAt time.Time `json:"received_at"`
	Source     string    `json:"source"`
	Layer      string    `json:"layer"`
	Condition  string    `json:"condition"`
	Available  bool      `json:"available"`
	Code       string    `json:"code,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

const maximumDiagnosticScan = 10_000

// DiagnosticEntries returns a bounded newest-first projection. Producer IDs,
// generations and event IDs are intentionally omitted from this operator view.
func (store *BoltStore) DiagnosticEntries(lineID string, limit int) ([]DiagnosticEntry, bool, error) {
	lineID = strings.TrimSpace(lineID)
	if lineID == "" || limit < 1 || limit > 500 {
		return nil, false, errors.New("invalid line diagnostic query")
	}
	result := make([]DiagnosticEntry, 0, limit)
	scanned := 0
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketRecords).Cursor()
		for _, value := cursor.Last(); value != nil && len(result) < limit && scanned < maximumDiagnosticScan; _, value = cursor.Prev() {
			scanned++
			record, err := decodeRecord(value, store.maxRecordBytes)
			if err != nil {
				return err
			}
			if record.Event.LineID != lineID {
				continue
			}
			result = append(result, diagnosticEntry(record))
		}
		return nil
	})
	sort.SliceStable(result, func(i, j int) bool { return result[i].ReceivedAt.After(result[j].ReceivedAt) })
	return result, len(result) < limit && scanned == maximumDiagnosticScan, err
}

// DiagnosticEntriesForLines scans the global append log once. Both returned
// records and examined records are bounded independently of catalog size.
func (store *BoltStore) DiagnosticEntriesForLines(lineIDs []string, totalLimit, perLineLimit int) (map[string][]DiagnosticEntry, bool, error) {
	if len(lineIDs) == 0 || len(lineIDs) > 4096 || totalLimit < 1 || totalLimit > 500 ||
		perLineLimit < 1 || perLineLimit > totalLimit {
		return nil, false, errors.New("invalid multi-line diagnostic query")
	}
	wanted := make(map[string]struct{}, len(lineIDs))
	for _, lineID := range lineIDs {
		lineID = strings.TrimSpace(lineID)
		if lineID == "" {
			return nil, false, errors.New("invalid multi-line diagnostic identity")
		}
		wanted[lineID] = struct{}{}
	}
	result := make(map[string][]DiagnosticEntry)
	scanned, collected := 0, 0
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketRecords).Cursor()
		for _, value := cursor.Last(); value != nil && collected < totalLimit && scanned < maximumDiagnosticScan; _, value = cursor.Prev() {
			scanned++
			record, err := decodeRecord(value, store.maxRecordBytes)
			if err != nil {
				return err
			}
			if _, ok := wanted[record.Event.LineID]; !ok || len(result[record.Event.LineID]) >= perLineLimit {
				continue
			}
			result[record.Event.LineID] = append(result[record.Event.LineID], diagnosticEntry(record))
			collected++
		}
		return nil
	})
	return result, collected < totalLimit && scanned == maximumDiagnosticScan, err
}

func diagnosticEntry(record Record) DiagnosticEntry {
	detail := diagnosticlog.RedactString(record.Event.Detail)
	if runes := []rune(detail); len(runes) > 1024 {
		detail = string(runes[:1024])
	}
	return DiagnosticEntry{ReceivedAt: record.ReceivedAt, Source: diagnosticSource(record.Event.ProducerRole),
		Layer: string(record.Event.Layer), Condition: string(record.Event.Condition), Available: record.Event.Available,
		Code: record.Event.Code, Detail: detail}
}

func diagnosticSource(role ProducerRole) string {
	switch role {
	case RoleAgent:
		return "agent"
	case RoleVoWiFi:
		return "provider"
	default:
		return "core"
	}
}
