package agentcontrol

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const minimumControlTokenBytes = 32

type API struct {
	controller       *Controller
	topology         TopologyProvider
	tokenHash        [sha256.Size]byte
	operationTimeout time.Duration
	mux              *http.ServeMux
}

type TopologyProvider interface {
	Topology() agentlink.TopologySnapshot
}

func NewAPI(controller *Controller, token string, operationTimeout time.Duration, topology TopologyProvider) (*API, error) {
	if controller == nil || len(token) < minimumControlTokenBytes || operationTimeout <= 0 {
		return nil, errors.New("invalid Agent control API configuration")
	}
	api := &API{
		controller: controller, topology: topology, tokenHash: sha256.Sum256([]byte(token)),
		operationTimeout: operationTimeout, mux: http.NewServeMux(),
	}
	api.mux.HandleFunc("GET /healthz", api.health)
	api.mux.HandleFunc("GET /v1/status", api.authorized(api.status))
	api.mux.HandleFunc("GET /v1/topology", api.authorized(api.currentTopology))
	api.mux.HandleFunc("POST /v1/runtime/start", api.authorized(api.start))
	api.mux.HandleFunc("POST /v1/runtime/stop", api.authorized(api.stop))
	return api, nil
}

func (api *API) currentTopology(response http.ResponseWriter, _ *http.Request) {
	if api.topology == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "topology_unavailable")
		return
	}
	runtime := api.controller.Status()
	if runtime.State != StateStarting && runtime.State != StateRunning && runtime.State != StateStopping {
		writeAPIError(response, http.StatusServiceUnavailable, "topology_unavailable")
		return
	}
	topology := agentlink.NormalizeTopology(api.topology.Topology())
	if err := topology.Validate(); err != nil {
		writeAPIError(response, http.StatusInternalServerError, "topology_invalid")
		return
	}
	writeAPIJSON(response, http.StatusOK, topology)
}

func (api *API) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	api.mux.ServeHTTP(response, request)
}

func (api *API) authorized(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		const prefix = "Bearer "
		header := request.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			writeAPIError(response, http.StatusUnauthorized, "unauthorized")
			return
		}
		presented := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
		if subtle.ConstantTimeCompare(presented[:], api.tokenHash[:]) != 1 {
			writeAPIError(response, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(response, request)
	}
}

func (api *API) health(response http.ResponseWriter, _ *http.Request) {
	writeAPIJSON(response, http.StatusOK, map[string]string{
		"status": "ok", "component": "mdd-agent-control",
	})
}

func (api *API) status(response http.ResponseWriter, _ *http.Request) {
	writeAPIJSON(response, http.StatusOK, api.controller.Status())
}

func (api *API) start(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), api.operationTimeout)
	defer cancel()
	snapshot, err := api.controller.Start(ctx)
	writeTransition(response, snapshot, err)
}

func (api *API) stop(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), api.operationTimeout)
	defer cancel()
	snapshot, err := api.controller.Stop(ctx)
	writeTransition(response, snapshot, err)
}

func writeTransition(response http.ResponseWriter, snapshot Snapshot, err error) {
	switch {
	case err == nil:
		writeAPIJSON(response, http.StatusOK, snapshot)
	case errors.Is(err, ErrConflict):
		writeAPIJSON(response, http.StatusConflict, map[string]any{
			"code": "runtime_conflict", "status": snapshot,
		})
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeAPIJSON(response, http.StatusGatewayTimeout, map[string]any{
			"code": "transition_timeout", "status": snapshot,
		})
	default:
		writeAPIJSON(response, http.StatusInternalServerError, map[string]any{
			"code": "transition_failed", "status": snapshot,
		})
	}
}

func writeAPIError(response http.ResponseWriter, status int, code string) {
	writeAPIJSON(response, status, map[string]string{"code": code})
}

func writeAPIJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
