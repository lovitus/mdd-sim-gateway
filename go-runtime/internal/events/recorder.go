package events

import (
	"strings"
	"sync"
	"time"
)

type generationClock struct {
	Current string            `json:"current"`
	Epoch   uint64            `json:"epoch"`
	Seen    map[string]uint64 `json:"seen"`
}

// Recorder assigns server-owned epochs before events are persisted. Producer
// generation counters may reset or collide across different machines.
type Recorder struct {
	mu       sync.Mutex
	streams  map[string]generationClock
	bindings map[string]string
}

func NewRecorder() *Recorder {
	return &Recorder{streams: make(map[string]generationClock), bindings: make(map[string]string)}
}

// Authorize binds one line and producer role to an exact process/session
// generation. Replacing hardware or a runtime is therefore an explicit core
// transaction; an old process cannot make itself current by inventing a new
// generation string.
func (r *Recorder) Authorize(lineID string, role ProducerRole, producerID, generation string) error {
	lineID = strings.TrimSpace(lineID)
	producerID = strings.TrimSpace(producerID)
	generation = strings.TrimSpace(generation)
	if lineID == "" || producerID == "" || generation == "" ||
		(role != RoleCore && role != RoleAgent && role != RoleVoWiFi) {
		return ErrInvalidEvent
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	binding := bindingKey(lineID, role)
	fingerprint := string(role) + "\x00" + producerID + "\x00" + generation
	if r.bindings[binding] == producerID+"\x00"+generation {
		return nil
	}
	streamPrefix := lineID + "\x00"
	for streamKey, clock := range r.streams {
		if !strings.HasPrefix(streamKey, streamPrefix) {
			continue
		}
		if _, seen := clock.Seen[fingerprint]; seen && clock.Current != fingerprint {
			return ErrGenerationReused
		}
	}
	r.bindings[binding] = producerID + "\x00" + generation
	return nil
}

func (r *Recorder) Accept(event Event, receivedAt time.Time) (Record, error) {
	if err := validateEvent(event); err != nil || receivedAt.IsZero() {
		if err != nil {
			return Record{}, err
		}
		return Record{}, ErrInvalidEvent
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	expected := r.bindings[bindingKey(strings.TrimSpace(event.LineID), event.ProducerRole)]
	actual := strings.TrimSpace(event.ProducerID) + "\x00" + strings.TrimSpace(event.Generation)
	if expected == "" || expected != actual {
		return Record{}, ErrUnauthorizedProducer
	}
	key := strings.TrimSpace(event.LineID) + "\x00" + string(event.Layer)
	fingerprint := string(event.ProducerRole) + "\x00" + strings.TrimSpace(event.ProducerID) + "\x00" + strings.TrimSpace(event.Generation)
	clock := r.streams[key]
	epoch, err := assignEpoch(&clock, fingerprint)
	if err != nil {
		return Record{}, err
	}
	r.streams[key] = clock
	return Record{ReceivedAt: receivedAt, Epoch: epoch, Event: event}, nil
}

func assignEpoch(clock *generationClock, fingerprint string) (uint64, error) {
	if clock.Seen == nil {
		clock.Seen = make(map[string]uint64)
	}
	epoch, seen := clock.Seen[fingerprint]
	if clock.Epoch == 0 {
		clock.Epoch = 1
		clock.Current = fingerprint
		epoch = clock.Epoch
		clock.Seen[fingerprint] = epoch
	} else if clock.Current == fingerprint {
		epoch = clock.Epoch
	} else if seen {
		return 0, ErrGenerationReused
	} else {
		clock.Epoch++
		clock.Current = fingerprint
		epoch = clock.Epoch
		clock.Seen[fingerprint] = epoch
	}
	return epoch, nil
}

func bindingKey(lineID string, role ProducerRole) string {
	return lineID + "\x00" + string(role)
}
