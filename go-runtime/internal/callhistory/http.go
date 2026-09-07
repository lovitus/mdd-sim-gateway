package callhistory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) (*Handler, error) {
	if store == nil {
		return nil, errors.New("call history store is required")
	}
	return &Handler{store: store}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	switch request.Method {
	case http.MethodGet:
		for key, values := range request.URL.Query() {
			if (key != "line_id" && key != "limit" && key != "transport") || len(values) != 1 {
				writeHTTP(response, http.StatusBadRequest, map[string]string{"code": "invalid_call_history_query"})
				return
			}
		}
		limit := 100
		if raw := request.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				writeHTTP(response, http.StatusBadRequest, map[string]string{"code": "invalid_call_history_query"})
				return
			}
			limit = parsed
		}
		transport := request.URL.Query().Get("transport")
		if limit < 1 || limit > 500 || transport != "" && transport != "vowifi" && transport != "cellular" {
			writeHTTP(response, http.StatusBadRequest, map[string]string{"code": "invalid_call_history_query"})
			return
		}
		records, err := handler.store.ListTransport(strings.TrimSpace(request.URL.Query().Get("line_id")), transport, limit)
		if err != nil {
			writeHTTP(response, http.StatusInternalServerError, map[string]string{"code": "call_history_read_failed"})
			return
		}
		writeHTTP(response, http.StatusOK, map[string]any{"calls": records})
	case http.MethodDelete:
		var input struct {
			IDs []string `json:"ids"`
		}
		if request.URL.RawQuery != "" || decodeHTTP(request.Body, &input) != nil {
			writeHTTP(response, http.StatusBadRequest, map[string]string{"code": "invalid_call_history_deletion"})
			return
		}
		deleted, err := handler.store.Delete(input.IDs)
		if err != nil {
			writeHTTP(response, http.StatusConflict, map[string]string{"code": "call_history_delete_conflict"})
			return
		}
		writeHTTP(response, http.StatusOK, map[string]any{"deleted": deleted})
	default:
		writeHTTP(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
	}
}

func decodeHTTP(body io.Reader, target any) error {
	wire, err := io.ReadAll(io.LimitReader(body, 64<<10))
	if err != nil || len(wire) == 0 {
		return errors.New("invalid request")
	}
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing request data")
	}
	return nil
}

func writeHTTP(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
