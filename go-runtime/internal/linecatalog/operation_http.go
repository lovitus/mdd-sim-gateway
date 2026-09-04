package linecatalog

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// OperationHandler exposes only the redacted durable operation projection.
// It is intended to be mounted behind the same management authentication as
// the catalog; it never performs or retries an operation.
type OperationHandler struct{ store *Store }

func NewOperationHandler(store *Store) *OperationHandler { return &OperationHandler{store: store} }

func (handler *OperationHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodGet {
		writeOperationJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	if handler == nil || handler.store == nil {
		writeOperationJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "operation_store_unavailable"})
		return
	}
	operationID := strings.TrimSpace(request.PathValue("operationID"))
	receipt, found, err := handler.store.GetOperation(operationID)
	if errors.Is(err, ErrOperationNotFound) || !found {
		writeOperationJSON(response, http.StatusNotFound, map[string]string{"code": "operation_not_found"})
		return
	}
	if err != nil {
		writeOperationJSON(response, http.StatusInternalServerError, map[string]string{"code": "operation_receipt_invalid"})
		return
	}
	writeOperationJSON(response, http.StatusOK, receipt.PublicStatus())
}

func writeOperationJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
