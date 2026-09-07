package events

import (
	"encoding/json"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	bolt "go.etcd.io/bbolt"
)

const AvailabilityWindow = 48 * time.Hour
const availabilityContinuity = 30 * time.Second

var bucketAvailability = []byte("availability_v1")

type AvailabilitySegment struct {
	State  string `json:"state"`
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Reason string `json:"reason,omitempty"`
}

type availabilityHistory struct {
	Session    string                `json:"session"`
	Generation string                `json:"generation"`
	Layers     map[state.Layer]Event `json:"layers"`
	Segments   []AvailabilitySegment `json:"segments"`
}

type AvailabilitySummary struct {
	Up       int64    `json:"up"`
	Down     int64    `json:"down"`
	Off      int64    `json:"off"`
	Unknown  int64    `json:"unknown"`
	Observed int64    `json:"observed_seconds"`
	Outages  int      `json:"outages"`
	Longest  int64    `json:"longest_outage_seconds"`
	Ratio    *float64 `json:"uptime_ratio"`
}

type Availability struct {
	Instance      string                `json:"instance"`
	Start         int64                 `json:"start"`
	End           int64                 `json:"end"`
	Span          int64                 `json:"span_seconds"`
	MaxSpan       int64                 `json:"max_span_seconds"`
	RecordedSince *int64                `json:"recorded_since"`
	Segments      []AvailabilitySegment `json:"segments"`
	Summary       AvailabilitySummary   `json:"summary"`
}

// AvailabilityNeedsSeed requests one complete accepted Provider snapshot when
// upgrading an existing events DB. Old replay facts are not historical samples.
func (store *BoltStore) AvailabilityNeedsSeed(lineID string) (bool, error) {
	needed := true
	err := store.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketAvailability).Get([]byte(lineID))
		if data == nil {
			return nil
		}
		var history availabilityHistory
		if err := json.Unmarshal(data, &history); err != nil {
			return err
		}
		_, runtime := history.Layers[state.LayerVoWiFiRuntime]
		_, tunnel := history.Layers[state.LayerTunnel]
		_, ims := history.Layers[state.LayerIMS]
		needed = !runtime || !tunnel || !ims
		return nil
	})
	return needed, err
}

// recordAvailabilityTx consumes only an accepted, new complete Provider receipt.
// It shares the checkpoint transaction; repeated receipts cannot extend history.
func (store *BoltStore) recordAvailabilityTx(tx *bolt.Tx, changed []Event, cp ProducerCheckpoint) error {
	if cp.ProducerRole != RoleVoWiFi {
		return nil
	}
	bucket := tx.Bucket(bucketAvailability)
	var history availabilityHistory
	if data := bucket.Get([]byte(cp.LineID)); data != nil {
		if err := json.Unmarshal(data, &history); err != nil {
			return err
		}
	}
	fingerprint := checkpointFingerprint(cp)
	if history.Generation != fingerprint || history.Layers == nil {
		history.Layers = make(map[state.Layer]Event)
	}
	for _, event := range changed {
		// Store machine codes only, never free-form Provider diagnostic material.
		event.Detail = ""
		history.Layers[event.Layer] = event
	}
	kind, reason := availabilityCondition(history.Layers)
	now := cp.ReceivedAt.Unix()
	segments := history.Segments
	if len(segments) > 0 && now < segments[len(segments)-1].End {
		// Preserve accepted layer changes, but break continuity until the wall
		// clock catches up. No later unchanged heartbeat may revive old facts.
		history.Session, history.Generation = "", fingerprint
		data, err := json.Marshal(history)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(cp.LineID), data)
	}
	continuous := len(segments) > 0 && history.Session == store.availabilitySession &&
		history.Generation == fingerprint && now-segments[len(segments)-1].End <= int64(availabilityContinuity/time.Second)
	if continuous {
		last := &segments[len(segments)-1]
		last.End = now
		if last.State != kind {
			segments = append(segments, AvailabilitySegment{State: kind, Start: now, End: now, Reason: reason})
		}
	} else {
		segments = append(segments, AvailabilitySegment{State: kind, Start: now, End: now, Reason: reason})
	}
	cutoff := now - int64(AvailabilityWindow/time.Second)
	first := 0
	for first < len(segments) && segments[first].End < cutoff {
		first++
	}
	history.Segments = segments[first:]
	if len(history.Segments) > 0 {
		history.Segments[0].Start = max(history.Segments[0].Start, cutoff)
	}
	history.Session, history.Generation = store.availabilitySession, fingerprint
	data, err := json.Marshal(history)
	if err != nil {
		return err
	}
	return bucket.Put([]byte(cp.LineID), data)
}

func availabilityCondition(layers map[state.Layer]Event) (string, string) {
	runtime, ok := layers[state.LayerVoWiFiRuntime]
	if !ok || runtime.Condition == state.ConditionUnknown {
		return "unknown", "runtime_unknown"
	}
	if runtime.Condition == state.ConditionInactive {
		return "off", runtime.Code
	}
	for _, layer := range []state.Layer{state.LayerVoWiFiRuntime, state.LayerTunnel, state.LayerIMS} {
		fact, ok := layers[layer]
		if !ok || fact.Condition == state.ConditionUnknown {
			return "unknown", string(layer) + "_unknown"
		}
		if fact.Condition != state.ConditionReady || !fact.Available {
			return "down", fact.Code
		}
	}
	return "up", "ims_registered"
}

func (store *BoltStore) Availability(lineID string, now time.Time) (Availability, error) {
	if lineID == "" || now.IsZero() {
		return Availability{}, ErrInvalidEvent
	}
	var history availabilityHistory
	err := store.db.View(func(tx *bolt.Tx) error {
		if data := tx.Bucket(bucketAvailability).Get([]byte(lineID)); data != nil {
			return json.Unmarshal(data, &history)
		}
		return nil
	})
	if err != nil {
		return Availability{}, err
	}
	end := now.Unix()
	maximum := int64(AvailabilityWindow / time.Second)
	span := int64(time.Hour / time.Second)
	result := Availability{Instance: lineID, End: end, MaxSpan: maximum}
	if len(history.Segments) > 0 {
		first := history.Segments[0].Start
		result.RecordedSince = &first
		span = min(maximum, max(span, end-first))
	}
	result.Start, result.Span = end-span, span
	result.Segments, result.Summary = availabilityTimeline(history.Segments, result.Start, end)
	return result, nil
}

// Ported from retired MDD store.py line_state_timeline/line_state_summary.
// Continuity is resolved during ingestion, never by extrapolating the query tail.
func availabilityTimeline(rows []AvailabilitySegment, start, end int64) ([]AvailabilitySegment, AvailabilitySummary) {
	result := make([]AvailabilitySegment, 0, len(rows)+1)
	cursor := start
	for _, row := range rows {
		if row.End < start || row.Start > end {
			continue
		}
		row.Start, row.End = max(row.Start, start), min(row.End, end)
		if row.End < row.Start {
			continue
		}
		if row.Start > cursor {
			result = append(result, AvailabilitySegment{State: "unknown", Start: cursor, End: row.Start})
		}
		if len(result) > 0 && result[len(result)-1].State == row.State && result[len(result)-1].End == row.Start {
			result[len(result)-1].End = row.End
		} else {
			result = append(result, row)
		}
		cursor = row.End
	}
	if end > cursor {
		result = append(result, AvailabilitySegment{State: "unknown", Start: cursor, End: end})
	}
	var summary AvailabilitySummary
	for _, row := range result {
		seconds := max(int64(0), row.End-row.Start)
		switch row.State {
		case "up":
			summary.Up += seconds
		case "down":
			summary.Down += seconds
			summary.Outages++
			summary.Longest = max(summary.Longest, seconds)
		case "off":
			summary.Off += seconds
		default:
			summary.Unknown += seconds
		}
	}
	summary.Observed = summary.Up + summary.Down
	if summary.Observed > 0 {
		ratio := float64(summary.Up) / float64(summary.Observed)
		summary.Ratio = &ratio
	}
	return result, summary
}
