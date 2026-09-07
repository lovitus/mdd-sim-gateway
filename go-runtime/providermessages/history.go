package providermessages

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

type Conversation struct {
	Peer  string `json:"peer"`
	Count int    `json:"count"`
	Last  Record `json:"last"`
}

var ErrHistoryQuery = errors.New("invalid message history query")

type HistoryPage struct {
	Messages   []Record `json:"messages"`
	NextBefore string   `json:"next_before,omitempty"`
}

func validateHistoryScope(lineID, transport string) error {
	if !identifier(lineID) || transport != "vowifi" && transport != "cellular" {
		return ErrHistoryQuery
	}
	return nil
}

func historyRecord(tx *bolt.Tx, value []byte) (Record, error) {
	var record Record
	if err := json.Unmarshal(value, &record); err != nil {
		return Record{}, err
	}
	if record.Transport == "" {
		record.Transport = inferTransport(record.Event)
	}
	if record.Kind == KindDelivery && strings.TrimSpace(record.MessageID) == "" {
		if id, part, ok := resolveDeliveryLink(tx.Bucket(bucketLinks), record.Event, record.Transport); ok {
			record.MessageID, record.Part = id, part
		}
	}
	return record, nil
}

func historyPeer(record Record) string {
	if peer := strings.TrimSpace(record.Sender); peer != "" {
		return peer
	}
	return strings.TrimSpace(record.Recipient)
}

// Resolve peer-less delivery reports against retained messages in the same
// line and transport. Conflicting identities remain unresolved, never guessed.
func historyPeers(tx *bolt.Tx, lineID, transport string) (map[string]string, error) {
	peers := make(map[string]string)
	err := tx.Bucket(bucketRecords).ForEach(func(_, value []byte) error {
		record, err := historyRecord(tx, value)
		if err != nil {
			return err
		}
		if record.LineID != lineID || record.Transport != transport || record.MessageID == "" {
			return nil
		}
		peer := historyPeer(record)
		if peer == "" {
			return nil
		}
		if previous, found := peers[record.MessageID]; found && previous != peer {
			peers[record.MessageID] = ""
		} else if !found {
			peers[record.MessageID] = peer
		}
		return nil
	})
	return peers, err
}

func resolvedHistoryPeer(record Record, peers map[string]string) string {
	if peer := historyPeer(record); peer != "" {
		return peer
	}
	return peers[record.MessageID]
}

// Conversations ports the retired store.py list_threads grouping over the
// complete retained line, not just the first page of message events.
func (store *Store) Conversations(lineID, transport string) ([]Conversation, error) {
	if err := validateHistoryScope(lineID, transport); err != nil {
		return nil, err
	}
	byPeer := make(map[string]*Conversation)
	err := store.db.View(func(tx *bolt.Tx) error {
		peers, err := historyPeers(tx, lineID, transport)
		if err != nil {
			return err
		}
		cursor := tx.Bucket(bucketRecords).Cursor()
		for key, value := cursor.Last(); key != nil; key, value = cursor.Prev() {
			record, err := historyRecord(tx, value)
			if err != nil {
				return err
			}
			if record.LineID != lineID || record.Transport != transport {
				continue
			}
			peer := resolvedHistoryPeer(record, peers)
			if peer == "" {
				continue
			}
			item := byPeer[peer]
			if item == nil {
				item = &Conversation{Peer: peer, Last: record}
				byPeer[peer] = item
			}
			item.Count++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]Conversation, 0, len(byPeer))
	for _, item := range byPeer {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Last.ReceivedAt.Equal(result[j].Last.ReceivedAt) {
			return result[i].Peer < result[j].Peer
		}
		return result[i].Last.ReceivedAt.After(result[j].Last.ReceivedAt)
	})
	return result, nil
}

// MessagePage uses the immutable bbolt record sequence as an exclusive cursor.
// New arrivals and deletions cannot shift an offset and skip older records.
func (store *Store) MessagePage(lineID, transport, peer, before string, limit int) (HistoryPage, error) {
	if err := validateHistoryScope(lineID, transport); err != nil {
		return HistoryPage{}, err
	}
	if strings.TrimSpace(peer) != peer || len(peer) > 256 || limit < 1 || limit > 500 {
		return HistoryPage{}, ErrHistoryQuery
	}
	var boundary uint64
	if before != "" {
		var err error
		boundary, err = strconv.ParseUint(before, 10, 64)
		if err != nil || boundary == 0 {
			return HistoryPage{}, ErrHistoryQuery
		}
	}
	page := HistoryPage{Messages: make([]Record, 0, limit)}
	err := store.db.View(func(tx *bolt.Tx) error {
		peers, err := historyPeers(tx, lineID, transport)
		if err != nil {
			return err
		}
		cursor := tx.Bucket(bucketRecords).Cursor()
		key, value := cursor.Last()
		if before != "" {
			var encoded [8]byte
			binary.BigEndian.PutUint64(encoded[:], boundary)
			key, value = cursor.Seek(encoded[:])
			if key == nil {
				key, value = cursor.Last()
			} else {
				key, value = cursor.Prev()
			}
		}
		var oldest uint64
		for ; key != nil; key, value = cursor.Prev() {
			record, err := historyRecord(tx, value)
			if err != nil {
				return err
			}
			if record.LineID != lineID || record.Transport != transport || peer != "" && resolvedHistoryPeer(record, peers) != peer {
				continue
			}
			if len(page.Messages) == limit {
				page.NextBefore = strconv.FormatUint(oldest, 10)
				break
			}
			if len(key) != 8 {
				return errors.New("invalid message record sequence")
			}
			oldest = binary.BigEndian.Uint64(key)
			page.Messages = append(page.Messages, record)
		}
		return nil
	})
	// Preserve the existing oldest-to-newest page presentation without using
	// producer wall-clock timestamps as pagination authority.
	for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
		page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
	}
	return page, err
}
