package cellulardata

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"
)

const InternalPath = "/v1/internal/cellular-data/session"

type InternalHandler struct {
	service *Service
	token   [32]byte
}

func NewInternalHandler(service *Service, token string) (*InternalHandler, error) {
	if service == nil || len(strings.TrimSpace(token)) < 32 {
		return nil, errors.New("invalid cellular data IPC handler")
	}
	return &InternalHandler{service: service, token: sha256.Sum256([]byte(strings.TrimSpace(token)))}, nil
}

func (handler *InternalHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.URL.Path != InternalPath || !handler.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"code": "cellular_data_ipc_authentication_required"})
		return
	}
	switch request.Method {
	case http.MethodGet:
		cardID, purpose := strings.TrimSpace(request.URL.Query().Get("card_id")), strings.TrimSpace(request.URL.Query().Get("purpose"))
		if len(request.URL.Query()) != 2 || cardID == "" || !validPurpose(purpose) {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_data_owner"})
			return
		}
		view, found := handler.service.OwnedCard(cardID, purpose)
		if !found {
			writeJSON(response, http.StatusNotFound, map[string]string{"code": "cellular_data_owner_not_found"})
			return
		}
		writeJSON(response, http.StatusOK, view)
	case http.MethodPost:
		var input struct {
			CardID      string    `json:"card_id"`
			Purpose     string    `json:"purpose"`
			Profile     string    `json:"profile,omitempty"`
			OperationID string    `json:"operation_id"`
			ExpiresAt   time.Time `json:"expires_at"`
			MaxBytes    uint64    `json:"max_bytes"`
		}
		if request.URL.RawQuery != "" || decodeRequest(request.Body, &input) != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_data_owner"})
			return
		}
		view, err := handler.service.EnsureOwnedCard(request.Context(), strings.TrimSpace(input.CardID),
			strings.TrimSpace(input.Purpose), strings.TrimSpace(input.Profile), input.ExpiresAt, input.MaxBytes,
			strings.TrimSpace(input.OperationID))
		if err != nil {
			writeServiceError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, view)
	case http.MethodDelete:
		var input struct {
			CardID    string `json:"card_id"`
			SessionID string `json:"session_id"`
			Purpose   string `json:"purpose"`
		}
		if request.URL.RawQuery != "" || decodeRequest(request.Body, &input) != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_cellular_data_owner"})
			return
		}
		if err := handler.service.StopOwnedCard(strings.TrimSpace(input.CardID), strings.TrimSpace(input.SessionID),
			strings.TrimSpace(input.Purpose)); err != nil {
			writeServiceError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		response.Header().Set("Allow", "GET, POST, DELETE")
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
	}
}

func (handler *InternalHandler) authorized(request *http.Request) bool {
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	return subtle.ConstantTimeCompare(presented[:], handler.token[:]) == 1
}
