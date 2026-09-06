package events

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/operations"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
)

const DefaultMaxRecordBytes = 1 << 20

type LineProjection struct {
	LineID     string                     `json:"line_id"`
	Facts      []state.FactView           `json:"facts"`
	Operations map[string]state.Readiness `json:"operations"`
}

type Replay struct {
	mu      sync.Mutex
	reducer *state.Reducer
	lines   map[string]struct{}
	lastAt  time.Time
}

func NewReplay(ttl time.Duration) (*Replay, error) {
	definitions, err := Definitions(ttl)
	if err != nil {
		return nil, err
	}
	reducer, err := state.NewReducer(definitions)
	if err != nil {
		return nil, err
	}
	return &Replay{reducer: reducer, lines: make(map[string]struct{})}, nil
}

func (r *Replay) Apply(record Record) (state.ApplyResult, error) {
	lineID, observation, err := record.Observation()
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result, err := r.reducer.Apply(lineID, observation)
	if err != nil {
		return "", err
	}
	r.lines[lineID] = struct{}{}
	if record.ReceivedAt.After(r.lastAt) {
		r.lastAt = record.ReceivedAt
	}
	return result, nil
}

func (r *Replay) Confirm(checkpoint ProducerCheckpoint) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.reducer.Confirm(
		checkpoint.LineID, string(checkpoint.ProducerRole), checkpoint.ProducerID,
		checkpoint.Generation, checkpoint.Layers, checkpoint.Sequence, checkpoint.ReceivedAt,
	); err != nil {
		return err
	}
	r.lines[checkpoint.LineID] = struct{}{}
	if checkpoint.ReceivedAt.After(r.lastAt) {
		r.lastAt = checkpoint.ReceivedAt
	}
	return nil
}

func (r *Replay) Projections(at time.Time) []LineProjection {
	r.mu.Lock()
	defer r.mu.Unlock()
	lineIDs := make([]string, 0, len(r.lines))
	for lineID := range r.lines {
		lineIDs = append(lineIDs, lineID)
	}
	sort.Strings(lineIDs)
	result := make([]LineProjection, 0, len(lineIDs))
	for _, lineID := range lineIDs {
		view := r.reducer.View(lineID, at)
		result = append(result, LineProjection{
			LineID: lineID, Facts: view.Facts, Operations: operations.EvaluateAll(view),
		})
	}
	return result
}

func (r *Replay) LastReceivedAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastAt
}

func (r *Replay) RemoveLine(lineID string) {
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.lines, lineID)
	r.reducer.RemoveLine(lineID)
}

func ReadJSONLines(reader io.Reader, replay *Replay, maxRecordBytes int) error {
	if replay == nil || maxRecordBytes <= 0 {
		return ErrInvalidEvent
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxRecordBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record Record
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return fmt.Errorf("record %d: %w: %v", lineNumber, ErrInvalidEvent, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return fmt.Errorf("record %d: %w: trailing JSON", lineNumber, ErrInvalidEvent)
		}
		if _, err := replay.Apply(record); err != nil {
			return fmt.Errorf("record %d: %w", lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read events: %w", err)
	}
	return nil
}
