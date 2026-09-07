// Package providerfacts accepts complete VoWiFi provider snapshots on Core's
// authenticated loopback listener and projects them into durable state facts.
package providerfacts

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/state"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const maximumSnapshotBytes = 64 << 10

type Handler struct {
	providers *mediaauth.ProviderDirectory
	store     *events.BoltStore
	replay    *events.Replay
	tokenHash [sha256.Size]byte
	now       func() time.Time
	calls     CallObserver
	mu        sync.Mutex
}

type CallObserver interface {
	ObserveVoWiFiSnapshot(vowifiipc.Snapshot, string, time.Time) error
}

func NewHandler(providers *mediaauth.ProviderDirectory, store *events.BoltStore, replay *events.Replay, token string, observers ...CallObserver) (*Handler, error) {
	handler, err := newHandlerWithClock(providers, store, replay, token, time.Now)
	if err != nil {
		return nil, err
	}
	if len(observers) > 1 || len(observers) == 1 && observers[0] == nil {
		return nil, errors.New("invalid provider call observer")
	}
	if len(observers) == 1 {
		handler.calls = observers[0]
	}
	return handler, nil
}

func newHandlerWithClock(providers *mediaauth.ProviderDirectory, store *events.BoltStore, replay *events.Replay, token string, now func() time.Time) (*Handler, error) {
	if providers == nil || store == nil || replay == nil || len(token) < 32 || now == nil {
		return nil, errors.New("invalid provider facts configuration")
	}
	return &Handler{providers: providers, store: store, replay: replay, tokenHash: sha256.Sum256([]byte(token)), now: now}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/provider/facts" || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPut {
		writeFailure(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !literalLoopbackRemote(request.RemoteAddr) {
		writeFailure(response, http.StatusForbidden, "loopback_required")
		return
	}
	if !handler.authorized(request.Header.Get("Authorization")) {
		writeFailure(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeFailure(response, http.StatusBadRequest, "invalid_snapshot")
		return
	}
	var snapshot vowifiipc.Snapshot
	if err := decodeSnapshot(request.Body, &snapshot); err != nil || snapshot.Validate() != nil {
		writeFailure(response, http.StatusBadRequest, "invalid_snapshot")
		return
	}

	receivedAt := handler.now().UTC()
	err = handler.providers.UseCurrent(request.Context(), snapshot.LineID, func(provider mediaauth.Provider) error {
		if provider.ProviderID != snapshot.ProviderID || provider.Generation != snapshot.ProcessGeneration {
			return mediaauth.ErrProviderUnavailable
		}
		handler.mu.Lock()
		defer handler.mu.Unlock()
		return handler.accept(snapshot, provider.CardID, receivedAt)
	})
	switch {
	case err == nil:
		response.WriteHeader(http.StatusNoContent)
	case errors.Is(err, mediaauth.ErrProviderUnavailable),
		errors.Is(err, events.ErrUnauthorizedProducer),
		errors.Is(err, events.ErrGenerationReused),
		errors.Is(err, events.ErrOlderCheckpoint),
		errors.Is(err, events.ErrEventIDConflict):
		writeFailure(response, http.StatusConflict, "stale_provider_snapshot")
	default:
		writeFailure(response, http.StatusInternalServerError, "snapshot_persist_failed")
	}
}

func (handler *Handler) accept(snapshot vowifiipc.Snapshot, cardID string, receivedAt time.Time) error {
	seedHistory, err := handler.store.AvailabilityNeedsSeed(snapshot.LineID)
	if err != nil {
		return err
	}
	desired := snapshotFacts(snapshot)
	current := currentFacts(handler.replay.Projections(receivedAt), snapshot.LineID)
	changed := make([]events.Event, 0, len(desired))
	for _, fact := range desired {
		prior, found := current[fact.layer]
		if !seedHistory && found && prior.Source == string(events.RoleVoWiFi) && prior.ProducerID == snapshot.ProviderID &&
			prior.Generation == snapshot.ProcessGeneration && prior.Condition == fact.condition &&
			prior.Available == fact.available && prior.Code == fact.code && prior.Detail == fact.detail {
			continue
		}
		changed = append(changed, events.Event{
			SchemaVersion: events.SchemaVersion,
			EventID:       fmt.Sprintf("provider-snapshot:%s:%s:%d:%s", snapshot.ProviderID, snapshot.ProcessGeneration, snapshot.Sequence, fact.layer),
			LineID:        snapshot.LineID, ProducerRole: events.RoleVoWiFi, ProducerID: snapshot.ProviderID,
			Layer: fact.layer, Condition: fact.condition, Available: fact.available, Code: fact.code, Detail: fact.detail,
			Generation: snapshot.ProcessGeneration, Sequence: snapshot.Sequence, ObservedAt: snapshot.ObservedAt.UTC(),
		})
	}
	checkpoint := events.ProducerCheckpoint{
		LineID: snapshot.LineID, ProducerRole: events.RoleVoWiFi, ProducerID: snapshot.ProviderID,
		Generation: snapshot.ProcessGeneration, Sequence: snapshot.Sequence,
		Layers:     []state.Layer{state.LayerVoWiFiRuntime, state.LayerTunnel, state.LayerIMS, state.LayerIMSVoice, state.LayerMessaging},
		ObservedAt: snapshot.ObservedAt.UTC(), ReceivedAt: receivedAt,
	}
	records, stored, err := handler.store.AcceptSnapshot(changed, checkpoint)
	if err != nil {
		return err
	}
	for _, record := range records {
		if _, err := handler.replay.Apply(record); err != nil {
			return err
		}
	}
	if err := handler.replay.Confirm(stored); err != nil {
		return err
	}
	if handler.calls != nil {
		// History is presentation data. It must never reject or delay the
		// Provider-owned status snapshot that drives runtime truth.
		_ = handler.calls.ObserveVoWiFiSnapshot(snapshot, cardID, receivedAt)
	}
	return nil
}

type desiredFact struct {
	layer     state.Layer
	condition state.Condition
	available bool
	code      string
	detail    string
}

func snapshotFacts(snapshot vowifiipc.Snapshot) []desiredFact {
	runtimeCondition, runtimeAvailable := mapRuntime(snapshot.Runtime.Condition)
	result := []desiredFact{{
		layer: state.LayerVoWiFiRuntime, condition: runtimeCondition, available: runtimeAvailable,
		code:   statusCode(snapshot.Runtime.Code, "runtime", string(snapshot.Runtime.Condition)),
		detail: runtimeNetworkDetail(snapshot.Runtime),
	}}
	for _, input := range []struct {
		layer  state.Layer
		status vowifiipc.LayerStatus
	}{
		{state.LayerTunnel, snapshot.Tunnel},
		{state.LayerIMS, snapshot.IMS},
		{state.LayerIMSVoice, snapshot.Voice},
		{state.LayerMessaging, snapshot.Messaging},
	} {
		condition, available := mapLayer(input.status)
		result = append(result, desiredFact{
			layer: input.layer, condition: condition, available: available,
			code: statusCode(input.status.Code, string(input.layer), string(input.status.Condition)),
		})
	}
	return result
}

func runtimeNetworkDetail(status vowifiipc.RuntimeStatus) string {
	parts := []string{}
	if status.PDNFamily != "" && status.ResponderID != "" {
		parts = append(parts, "pdn_family="+status.PDNFamily, "idr="+status.ResponderID)
	}
	if status.RegisterSupported {
		parts = append(parts, "manual_register=true")
	}
	return strings.Join(parts, ";")
}

func mapRuntime(condition vowifiipc.RuntimeCondition) (state.Condition, bool) {
	switch condition {
	case vowifiipc.RuntimeStarting:
		return state.ConditionStarting, false
	case vowifiipc.RuntimeRunning:
		return state.ConditionReady, true
	case vowifiipc.RuntimeFailed:
		return state.ConditionFailed, false
	case vowifiipc.RuntimeStopped, vowifiipc.RuntimeStopping:
		return state.ConditionInactive, false
	default:
		return state.ConditionUnknown, false
	}
}

func mapLayer(status vowifiipc.LayerStatus) (state.Condition, bool) {
	switch status.Condition {
	case vowifiipc.LayerConnecting:
		return state.ConditionStarting, false
	case vowifiipc.LayerReady:
		return state.ConditionReady, true
	case vowifiipc.LayerDegraded:
		return state.ConditionDegraded, status.Available
	case vowifiipc.LayerBlocked:
		return state.ConditionBlocked, false
	case vowifiipc.LayerStopped:
		return state.ConditionInactive, false
	default:
		return state.ConditionUnknown, false
	}
}

func statusCode(code, layer, condition string) string {
	if code != "" {
		return code
	}
	return layer + "_" + condition
}

func currentFacts(projections []events.LineProjection, lineID string) map[state.Layer]state.FactView {
	result := make(map[state.Layer]state.FactView)
	for _, projection := range projections {
		if projection.LineID != lineID {
			continue
		}
		for _, fact := range projection.Facts {
			result[fact.Layer] = fact
		}
		break
	}
	return result
}

func (handler *Handler) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(presented[:], handler.tokenHash[:]) == 1
}

func decodeSnapshot(body io.Reader, target *vowifiipc.Snapshot) error {
	payload, err := io.ReadAll(io.LimitReader(body, maximumSnapshotBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumSnapshotBytes {
		return errors.New("invalid provider snapshot size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("provider snapshot has trailing JSON")
	}
	return nil
}

func literalLoopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func writeFailure(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"code": code})
}
