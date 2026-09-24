package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

// mobileStatus is already implemented by providercontrol.Handler. Reads retain
// its live Provider generation and catalog-card fences; no new mutation owner.
type mobileStatus interface {
	Status(context.Context, string) (vowifiipc.Snapshot, error)
}
type mobileReadiness struct {
	Ready   bool           `json:"ready"`
	Blocked []state.Layer  `json:"blocked,omitempty"`
	Reasons []mobileReason `json:"reasons,omitempty"`
}
type mobileReason struct {
	Layer state.Layer `json:"layer"`
	Code  string      `json:"code"`
}

func compactMobileOperations(source map[string]state.Readiness) map[string]mobileReadiness {
	result := map[string]mobileReadiness{}
	for name, readiness := range source {
		row := mobileReadiness{Ready: readiness.Ready, Blocked: readiness.Blocked}
		for _, fact := range readiness.Facts {
			if !fact.Available || !fact.Fresh {
				row.Reasons = append(row.Reasons, mobileReason{Layer: fact.Layer, Code: fact.Code})
			}
		}
		result[name] = row
	}
	return result
}

type mobileLine struct {
	ID               string                         `json:"id"`
	Name             string                         `json:"name"`
	Number           string                         `json:"number,omitempty"`
	CardID           string                         `json:"card_id"`
	Enabled          bool                           `json:"enabled"`
	Operations       map[string]mobileReadiness     `json:"operations"`
	Incoming         *vowifiipc.PendingIncomingCall `json:"incoming,omitempty"`
	CellularIncoming *mobileCellularCall            `json:"cellular_incoming,omitempty"`
	Active           *vowifiipc.ActiveCall          `json:"active,omitempty"`
	IMS              string                         `json:"ims"`
	Failure          string                         `json:"failure,omitempty"`
}
type mobileCellularCall struct {
	IncomingEventID      string `json:"incoming_event_id"`
	LineID               string `json:"line_id"`
	CardID               string `json:"card_id"`
	SIMSessionGeneration string `json:"sim_session_generation"`
	NativeCallIndex      int    `json:"native_call_index"`
	Occurrence           uint64 `json:"occurrence"`
	Number               string `json:"number,omitempty"`
	Actionable           bool   `json:"actionable"`
	Blocked              string `json:"blocked,omitempty"`
	Claiming             bool   `json:"claiming"`
	State                string `json:"state"`
}

func compactMobileCellular(call cellularmedia.IncomingCallView) mobileCellularCall {
	return mobileCellularCall{IncomingEventID: call.IncomingEventID, LineID: call.LineID, CardID: call.CardID, SIMSessionGeneration: call.SIMSessionGeneration, NativeCallIndex: call.NativeCallIndex, Occurrence: call.Occurrence, Number: call.Number, Actionable: call.Actionable, Blocked: call.Blocked, Claiming: call.Claiming, State: call.State}
}

type mobileData struct {
	Lines            []mobileLine `json:"lines"`
	IncomingLines    []mobileLine `json:"incoming_lines"`
	IncomingMore     bool         `json:"incoming_more"`
	IncomingRevision string       `json:"incoming_revision"`
	allIncoming      []mobileLine
	CellularCalls    []mobileCellularCall      `json:"cellular_calls"`
	Messages         []providermessages.Record `json:"messages"`
	Incomplete       bool                      `json:"incomplete"`
}

type mobileCallFacts interface {
	CurrentVoWiFiCalls(time.Time) []callhistory.CallObservation
}

func WithMobileCallFacts(facts mobileCallFacts) Option {
	return func(s *Server) { s.mobileCallFacts = facts }
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
	result := mobileData{Lines: []mobileLine{}, CellularCalls: []mobileCellularCall{}, Messages: []providermessages.Record{}}
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
			for _, call := range calls {
				result.CellularCalls = append(result.CellularCalls, compactMobileCellular(call))
			}
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
		result.Lines = append(result.Lines, mobileLine{ID: line.ID, Name: line.Name, Number: line.SIM.MSISDN, CardID: line.CardID, Enabled: line.Enabled, Operations: compactMobileOperations(ops[line.ID]), IMS: "unknown"})
	}
	if s.mobileCallFacts != nil && s.providers != nil {
		observed := map[string]callhistory.CallObservation{}
		for _, fact := range s.mobileCallFacts.CurrentVoWiFiCalls(s.now().UTC()) {
			if generation, ok := s.providers.CurrentGeneration(fact.LineID); ok && generation == fact.Generation {
				observed[fact.LineID] = fact
			}
		}
		for i := range result.Lines {
			line := &result.Lines[i]
			if !line.Enabled {
				line.IMS = "disabled"
				continue
			}
			fact, ok := observed[line.ID]
			if !ok || fact.CardID != line.CardID {
				line.Failure = "provider_observation_unavailable"
				result.Incomplete = true
				continue
			}
			line.IMS = string(fact.Snapshot.IMS.Condition)
			line.Incoming = fact.Snapshot.PendingIncomingCall
			line.Active = fact.Snapshot.ActiveCall
		}
		compactMobileLines(&result)
		return result
	}
	source, ok := s.control.(mobileStatus)
	if !ok {
		compactMobileLines(&result)
		return result
	}
	// Bounded concurrency and one shared deadline: an unhealthy Provider must
	// not stall authentication checks, all other lines or mobile heartbeats.
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	jobs := make(chan int, len(result.Lines))
	for index := range result.Lines {
		jobs <- index
	}
	close(jobs)
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				line := &result.Lines[index]
				if !line.Enabled {
					line.IMS = "disabled"
					continue
				}
				if deadline.Err() != nil {
					line.Failure = "observation_timeout"
					continue
				}
				status, err := source.Status(deadline, line.ID)
				if err != nil {
					line.Failure = "provider_status_unavailable"
					continue
				}
				line.IMS = string(status.IMS.Condition)
				line.Incoming, line.Active = status.PendingIncomingCall, status.ActiveCall
			}
		}()
	}
	workers.Wait()
	for _, line := range result.Lines {
		if line.Failure != "" {
			result.Incomplete = true
		}
	}
	compactMobileLines(&result)
	return result
}

func compactMobileLines(result *mobileData) {
	result.IncomingLines = []mobileLine{}
	result.allIncoming = []mobileLine{}
	cellular := map[string]mobileCellularCall{}
	for _, call := range result.CellularCalls {
		if call.Actionable {
			cellular[call.LineID] = call
		}
	}
	for _, line := range result.Lines {
		if call, ok := cellular[line.ID]; ok && call.CardID == line.CardID {
			copy := call
			line.CellularIncoming = &copy
		}
		if line.Incoming != nil || line.CellularIncoming != nil {
			result.allIncoming = append(result.allIncoming, line)
			// The incoming window is independent of directory paging.
			if len(result.IncomingLines) < 128 {
				result.IncomingLines = append(result.IncomingLines, line)
			} else {
				result.Incomplete = true
				result.IncomingMore = true
			}
		}
	}
	identities := [][3]string{}
	for _, line := range result.allIncoming {
		if line.Incoming != nil {
			identities = append(identities, [3]string{line.ID, "vowifi", line.Incoming.CallID})
		}
		if line.CellularIncoming != nil {
			identities = append(identities, [3]string{line.ID, "cellular", line.CellularIncoming.IncomingEventID})
		}
	}
	wire, _ := json.Marshal(identities)
	digest := sha256.Sum256(wire)
	result.IncomingRevision = hex.EncodeToString(digest[:])
	if len(result.Lines) > 128 {
		result.Lines = result.Lines[:128]
		result.Incomplete = true
	}
}

func (s *Server) mobileIncoming(w http.ResponseWriter, r *http.Request) {
	if _, err := s.browser.VerifyBrowserSession(r.Context(), r); err != nil {
		writeJSON(w, 401, map[string]string{"code": "authentication_required"})
		return
	}
	query := r.URL.Query()
	for k, v := range query {
		if k != "after" || len(v) != 1 || len(v[0]) > 160 {
			writeJSON(w, 400, map[string]string{"code": "invalid_mobile_query"})
			return
		}
	}
	snapshot := s.mobileSnapshot(r.Context())
	all := append([]mobileLine(nil), snapshot.allIncoming...)
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	rows := []mobileLine{}
	next := ""
	for _, line := range all {
		if line.ID <= query.Get("after") {
			continue
		}
		if len(rows) == 128 {
			next = rows[len(rows)-1].ID
			break
		}
		rows = append(rows, line)
	}
	writeJSON(w, 200, map[string]any{"lines": rows, "next_after": next, "revision": snapshot.IncomingRevision, "incomplete": snapshot.Incomplete})
}

// Directory membership is authoritative independently of a compact live feed.
func (s *Server) mobileLines(w http.ResponseWriter, r *http.Request) {
	if _, err := s.browser.VerifyBrowserSession(r.Context(), r); err != nil {
		writeJSON(w, 401, map[string]string{"code": "authentication_required"})
		return
	}
	if s.catalog == nil {
		writeJSON(w, 503, map[string]string{"code": "catalog_unavailable"})
		return
	}
	catalog, err := s.catalog.Snapshot()
	if err != nil {
		writeJSON(w, 503, map[string]string{"code": "catalog_unavailable"})
		return
	}
	id := r.PathValue("lineID")
	q := r.URL.Query()
	for key, values := range q {
		if id != "" || key != "after" && key != "q" || len(values) != 1 {
			writeJSON(w, 400, map[string]string{"code": "invalid_mobile_query"})
			return
		}
	}
	if len(q.Get("q")) > 256 || len(q.Get("after")) > 160 {
		writeJSON(w, 400, map[string]string{"code": "invalid_mobile_query"})
		return
	}
	operations := map[string]map[string]state.Readiness{}
	for _, line := range s.replay.Projections(s.now().UTC()) {
		operations[line.LineID] = line.Operations
	}
	view := func(line linecatalog.Line) mobileLine {
		condition := "unknown"
		if !line.Enabled {
			condition = "disabled"
		}
		return mobileLine{ID: line.ID, Name: line.Name, Number: line.SIM.MSISDN, CardID: line.CardID, Enabled: line.Enabled, Operations: compactMobileOperations(operations[line.ID]), IMS: condition}
	}
	if id != "" {
		for _, line := range catalog.Lines {
			if line.ID != id || line.Deleted {
				continue
			}
			item := view(line)
			if source, ok := s.control.(mobileStatus); ok && line.Enabled {
				ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
				status, err := source.Status(ctx, line.ID)
				cancel()
				if err != nil {
					item.Failure = "provider_status_unavailable"
				} else {
					item.IMS = string(status.IMS.Condition)
					item.Incoming = status.PendingIncomingCall
					item.Active = status.ActiveCall
				}
			}
			writeJSON(w, 200, item)
			return
		}
		writeJSON(w, 404, map[string]string{"code": "line_not_found"})
		return
	}
	sort.Slice(catalog.Lines, func(i, j int) bool { return catalog.Lines[i].ID < catalog.Lines[j].ID })
	items := []mobileLine{}
	next := ""
	query := strings.ToLower(q.Get("q"))
	for _, line := range catalog.Lines {
		if line.Deleted || line.ID <= q.Get("after") || query != "" && !strings.Contains(strings.ToLower(line.ID+" "+line.Name+" "+line.SIM.MSISDN+" "+line.CardID), query) {
			continue
		}
		if len(items) == 128 {
			next = items[len(items)-1].ID
			break
		}
		items = append(items, view(line))
	}
	writeJSON(w, 200, map[string]any{"lines": items, "next_after": next})
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
