package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

// mobileStatus is already implemented by providercontrol.Handler. Reads retain
// its live Provider generation and catalog-card fences; no new mutation owner.
type mobileStatus interface {
	Status(context.Context, string) (vowifiipc.Snapshot, error)
}
type mobileLine struct {
	ID         string                         `json:"id"`
	Name       string                         `json:"name"`
	CardID     string                         `json:"card_id"`
	Enabled    bool                           `json:"enabled"`
	Operations map[string]state.Readiness     `json:"operations"`
	Incoming   *vowifiipc.PendingIncomingCall `json:"incoming,omitempty"`
	Active     *vowifiipc.ActiveCall          `json:"active,omitempty"`
	IMS        string                         `json:"ims"`
	Failure    string                         `json:"failure,omitempty"`
}
type mobileData struct {
	Lines         []mobileLine                     `json:"lines"`
	CellularCalls []cellularmedia.IncomingCallView `json:"cellular_calls"`
	Messages      []providermessages.Record        `json:"messages"`
	Incomplete    bool                             `json:"incomplete"`
}
type mobileEnvelope struct {
	Type          string      `json:"type"`
	SchemaVersion int         `json:"schema_version"`
	Sequence      uint64      `json:"sequence"`
	At            time.Time   `json:"at"`
	Data          *mobileData `json:"data,omitempty"`
}

func (s *Server) mobileSnapshot(ctx context.Context) mobileData {
	s.mobileMu.Lock()
	defer s.mobileMu.Unlock()
	if !s.mobileAt.IsZero() && time.Since(s.mobileAt) < time.Second {
		return s.mobileCached
	}
	data := s.readMobileSnapshot(ctx)
	s.mobileCached, s.mobileAt = data, time.Now()
	return data
}
func (s *Server) readMobileSnapshot(ctx context.Context) mobileData {
	result := mobileData{Lines: []mobileLine{}, CellularCalls: []cellularmedia.IncomingCallView{}, Messages: []providermessages.Record{}}
	ops := map[string]map[string]state.Readiness{}
	for _, line := range s.replay.Projections(s.now().UTC()) {
		ops[line.LineID] = line.Operations
	}
	if s.messages != nil {
		messages, err := s.messages.List("", 50)
		if err != nil {
			result.Incomplete = true
		} else if messages != nil {
			result.Messages = messages
		}
	}
	if s.cellularCalls != nil {
		calls, err := s.cellularCalls.IncomingCalls()
		if err != nil {
			result.Incomplete = true
		} else if calls != nil {
			result.CellularCalls = calls
		}
	}
	if s.catalog == nil {
		return result
	}
	catalog, err := s.catalog.Snapshot()
	if err != nil {
		result.Incomplete = true
		return result
	}
	for _, line := range catalog.Lines {
		if line.Deleted {
			continue
		}
		if len(result.Lines) >= 128 {
			result.Incomplete = true
			break
		}
		result.Lines = append(result.Lines, mobileLine{ID: line.ID, Name: line.Name, CardID: line.CardID, Enabled: line.Enabled, Operations: ops[line.ID], IMS: "unknown"})
	}
	source, ok := s.control.(mobileStatus)
	if !ok {
		return result
	}
	// Bounded concurrency and one shared deadline: an unhealthy Provider must
	// not stall authentication checks, all other lines or mobile heartbeats.
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	slots := make(chan struct{}, 8)
	for index := range result.Lines {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			line := &result.Lines[index]
			if !line.Enabled {
				line.IMS = "disabled"
				return
			}
			select {
			case slots <- struct{}{}:
			case <-deadline.Done():
				line.Failure = "observation_timeout"
				return
			}
			defer func() { <-slots }()
			status, err := source.Status(deadline, line.ID)
			if err != nil {
				line.Failure = "provider_status_unavailable"
				return
			}
			line.IMS = string(status.IMS.Condition)
			line.Incoming, line.Active = status.PendingIncomingCall, status.ActiveCall
		}(index)
	}
	workers.Wait()
	for _, line := range result.Lines {
		if line.Failure != "" {
			result.Incomplete = true
		}
	}
	return result
}

// Mobile events avoid sending the large desktop snapshot every three seconds.
// Server-side reads use existing facts. Unchanged clients get a 30s heartbeat;
// changed call/SMS observations are delivered on the existing 3s cadence.
func (s *Server) mobileState(response http.ResponseWriter, request *http.Request) {
	subject, err := s.browser.VerifyBrowserSession(request.Context(), request)
	if err != nil || strings.TrimSpace(subject) == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"code": "authentication_required"})
		return
	}
	socket, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer socket.CloseNow()
	ctx := socket.CloseRead(request.Context())
	ticker := time.NewTicker(s.browserEvery)
	defer ticker.Stop()
	var previous [32]byte
	var sent time.Time
	sequence := uint64(0)
	for {
		current, err := s.browser.VerifyBrowserSession(ctx, request)
		if err != nil || current != subject {
			_ = socket.Close(browserAuthClose, "session expired")
			return
		}
		data := s.mobileSnapshot(ctx)
		raw, err := json.Marshal(data)
		if err != nil {
			return
		}
		digest := sha256.Sum256(raw)
		changed := sequence == 0 || digest != previous
		if changed || time.Since(sent) >= 30*time.Second {
			sequence++
			message := mobileEnvelope{Type: "mobile.heartbeat", SchemaVersion: 1, Sequence: sequence, At: s.now().UTC()}
			if changed {
				message.Type = "mobile.snapshot"
				message.Data = &data
			}
			writeCtx, cancel := context.WithTimeout(ctx, browserWriteTimeout)
			err := wsjson.Write(writeCtx, socket, message)
			cancel()
			if err != nil {
				return
			}
			previous, sent = digest, time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
