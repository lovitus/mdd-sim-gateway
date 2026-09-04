package linecatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maximumLineRequestBytes = 32 << 10

type LifecycleGuard interface {
	ActiveLine(string) (bool, error)
}

type LifecycleGuardFunc func(string) (bool, error)

func (function LifecycleGuardFunc) ActiveLine(lineID string) (bool, error) { return function(lineID) }

type Handler struct {
	store *Store
	guard LifecycleGuard
}

func NewHandler(store *Store, guards ...LifecycleGuard) *Handler {
	var guard LifecycleGuard
	if len(guards) > 0 {
		guard = guards[0]
	}
	return &Handler{store: store, guard: guard}
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if handler == nil || handler.store == nil {
		writeCatalogJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "catalog_unavailable"})
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodPut && request.Method != http.MethodPost {
		writeCatalogJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	id := strings.TrimSpace(request.PathValue("lineID"))
	if request.Method == http.MethodPost {
		handler.lifecycle(response, request, id, request.PathValue("operation"))
		return
	}
	if request.Method == http.MethodPut {
		handler.put(response, request, id)
		return
	}
	if id == "" {
		includeDeleted := request.URL.Query().Get("include_deleted") == "true"
		var snapshot linecatalogSnapshot
		var err error
		if includeDeleted {
			snapshot, err = handler.store.SnapshotIncludingDeleted()
		} else {
			snapshot, err = handler.store.Snapshot()
		}
		if err != nil {
			writeCatalogJSON(response, http.StatusInternalServerError, map[string]string{"code": "catalog_read_failed"})
			return
		}
		response.Header().Set("ETag", revisionETag(snapshot.Revision))
		writeCatalogJSON(response, http.StatusOK, snapshot)
		return
	}
	line, revision, err := handler.store.GetWithRevision(id)
	if err == ErrNotFound {
		writeCatalogJSON(response, http.StatusNotFound, map[string]string{"code": "line_not_found"})
		return
	}
	if err != nil {
		writeCatalogJSON(response, http.StatusInternalServerError, map[string]string{"code": "catalog_read_failed"})
		return
	}
	response.Header().Set("ETag", revisionETag(revision))
	writeCatalogJSON(response, http.StatusOK, line)
}

type linecatalogSnapshot = Snapshot

func (handler *Handler) lifecycle(response http.ResponseWriter, request *http.Request, id, operation string) {
	if id == "" {
		writeCatalogJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	if operation != "soft-delete" && operation != "restore" {
		writeCatalogJSON(response, http.StatusNotFound, map[string]string{"code": "line_route_not_found"})
		return
	}
	if operation == "soft-delete" && handler.guard != nil {
		active, guardErr := handler.guard.ActiveLine(id)
		if guardErr != nil {
			writeCatalogJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "line_lease_state_unavailable"})
			return
		}
		if active {
			writeCatalogJSON(response, http.StatusConflict, map[string]string{"code": "line_active_lease"})
			return
		}
	}
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeCatalogJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "catalog_revision_required"})
		return
	}
	line, revision, err := handler.store.SetDeletedExpected(id, operation == "soft-delete", expected)
	if errors.Is(err, ErrRevision) {
		response.Header().Set("ETag", revisionETag(revision))
		writeCatalogJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "catalog_revision_changed"})
		return
	}
	if errors.Is(err, ErrLineActive) {
		writeCatalogJSON(response, http.StatusConflict, map[string]string{"code": "line_runtime_active"})
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeCatalogJSON(response, http.StatusNotFound, map[string]string{"code": "line_not_found"})
		return
	}
	if err != nil {
		writeCatalogJSON(response, http.StatusConflict, map[string]string{"code": "line_lifecycle_failed"})
		return
	}
	response.Header().Set("ETag", revisionETag(revision))
	writeCatalogJSON(response, http.StatusOK, map[string]any{"schema_version": SchemaVersion, "revision": revision, "line": line})
}

func (handler *Handler) put(response http.ResponseWriter, request *http.Request, id string) {
	if id == "" {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "line_id_required"})
		return
	}
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeCatalogJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "catalog_revision_required"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumLineRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumLineRequestBytes {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_line"})
		return
	}
	var line Line
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&line) != nil || decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(line.ID) != id {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_line"})
		return
	}
	line, revision, err := handler.store.PutExpectedManaged(line, expected)
	if errors.Is(err, ErrRevision) {
		response.Header().Set("ETag", revisionETag(revision))
		writeCatalogJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "catalog_revision_changed"})
		return
	}
	if errors.Is(err, ErrCardInUse) {
		writeCatalogJSON(response, http.StatusConflict, map[string]string{"code": "card_identity_in_use"})
		return
	}
	if errors.Is(err, ErrIMEIBindingManaged) {
		writeCatalogJSON(response, http.StatusConflict, map[string]string{"code": "imei_binding_managed"})
		return
	}
	if err != nil {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_line"})
		return
	}
	response.Header().Set("ETag", revisionETag(revision))
	writeCatalogJSON(response, http.StatusOK, map[string]any{
		"schema_version": SchemaVersion, "revision": revision, "line": line,
	})
}

func revisionETag(revision uint64) string { return `"` + strconv.FormatUint(revision, 10) + `"` }

func parseIfMatch(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value[1:len(value)-1], `"`) {
		return 0, errors.New("invalid If-Match")
	}
	return strconv.ParseUint(value[1:len(value)-1], 10, 64)
}

func writeCatalogJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
