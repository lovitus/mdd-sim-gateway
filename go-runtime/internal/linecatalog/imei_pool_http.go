package linecatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const maximumIMEIPoolRequestBytes = 16 << 10

type IMEIPoolHandler struct{ store *Store }

func NewIMEIPoolHandler(store *Store) *IMEIPoolHandler { return &IMEIPoolHandler{store: store} }

func (handler *IMEIPoolHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if handler == nil || handler.store == nil {
		writeCatalogJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "imei_pool_unavailable"})
		return
	}
	if request.URL.RawQuery != "" {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_imei_pool_request"})
		return
	}
	entryID := strings.TrimSpace(request.PathValue("entryID"))
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	switch {
	case entryID == "" && lineID == "" && request.Method == http.MethodGet:
		handler.read(response)
	case entryID != "" && lineID == "" && request.Method == http.MethodPut:
		handler.putEntry(response, request, entryID)
	case entryID != "" && lineID == "" && request.Method == http.MethodDelete:
		handler.deleteEntry(response, request, entryID)
	case entryID != "" && lineID != "" && (request.Method == http.MethodPut || request.Method == http.MethodDelete):
		handler.changeBinding(response, request, entryID, lineID, request.Method == http.MethodPut)
	default:
		writeCatalogJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
	}
}

func (handler *IMEIPoolHandler) read(response http.ResponseWriter) {
	snapshot, err := handler.store.IMEIPoolSnapshot()
	if err != nil {
		writeCatalogJSON(response, http.StatusInternalServerError, map[string]string{"code": "imei_pool_read_failed"})
		return
	}
	response.Header().Set("ETag", revisionETag(snapshot.Revision))
	writeCatalogJSON(response, http.StatusOK, snapshot)
}

func (handler *IMEIPoolHandler) putEntry(response http.ResponseWriter, request *http.Request, entryID string) {
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeCatalogJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "imei_pool_revision_required"})
		return
	}
	var input IMEIPoolEntry
	if decodeIMEIPoolRequest(request.Body, &input) != nil || strings.TrimSpace(input.ID) != entryID ||
		input.normalizeAndValidate() != nil {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_imei_pool_entry"})
		return
	}
	entry, revision, changed, err := handler.store.PutIMEIPoolEntryExpected(input, expected)
	if err != nil {
		handler.writeError(response, err, revision)
		return
	}
	response.Header().Set("ETag", revisionETag(revision))
	writeCatalogJSON(response, http.StatusOK, map[string]any{
		"schema_version": IMEIPoolSchemaVersion, "revision": revision, "changed": changed, "entry": entry,
	})
}

func (handler *IMEIPoolHandler) deleteEntry(response http.ResponseWriter, request *http.Request, entryID string) {
	if !validIdentifier(entryID) {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_imei_pool_entry"})
		return
	}
	expected, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeCatalogJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "imei_pool_revision_required"})
		return
	}
	if request.Body != nil && request.Body != http.NoBody && request.ContentLength != 0 {
		var empty struct{}
		if decodeIMEIPoolRequest(request.Body, &empty) != nil {
			writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_imei_pool_request"})
			return
		}
	}
	revision, err := handler.store.DeleteIMEIPoolEntryExpected(entryID, expected)
	if err != nil {
		handler.writeError(response, err, revision)
		return
	}
	response.Header().Set("ETag", revisionETag(revision))
	response.WriteHeader(http.StatusNoContent)
}

func (handler *IMEIPoolHandler) changeBinding(response http.ResponseWriter, request *http.Request,
	entryID, lineID string, bind bool) {
	expectedPool, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeCatalogJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "imei_pool_revision_required"})
		return
	}
	var input struct {
		ExpectedCatalogRevision uint64 `json:"expected_catalog_revision"`
		ExpectedCardID          string `json:"expected_card_id"`
	}
	if decodeIMEIPoolRequest(request.Body, &input) != nil || input.ExpectedCatalogRevision == 0 {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_imei_binding"})
		return
	}
	if !validIdentifier(entryID) || !validIdentifier(lineID) ||
		!digitsBetween(digitsOnly(input.ExpectedCardID), 4, 32) {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_imei_binding"})
		return
	}
	var line Line
	var poolRevision, catalogRevision uint64
	var changed bool
	if bind {
		line, poolRevision, catalogRevision, changed, err = handler.store.BindIMEIExpected(
			entryID, lineID, input.ExpectedCardID, expectedPool, input.ExpectedCatalogRevision)
	} else {
		line, poolRevision, catalogRevision, changed, err = handler.store.UnbindIMEIExpected(
			entryID, lineID, input.ExpectedCardID, expectedPool, input.ExpectedCatalogRevision)
	}
	if err != nil {
		handler.writeBindingError(response, err, poolRevision, catalogRevision)
		return
	}
	response.Header().Set("ETag", revisionETag(poolRevision))
	writeCatalogJSON(response, http.StatusOK, map[string]any{
		"schema_version": IMEIPoolSchemaVersion, "revision": poolRevision,
		"catalog_revision": catalogRevision, "changed": changed, "line": line,
	})
}

func (handler *IMEIPoolHandler) writeError(response http.ResponseWriter, err error, revision uint64) {
	if revision != 0 {
		response.Header().Set("ETag", revisionETag(revision))
	}
	switch {
	case errors.Is(err, ErrIMEIPoolRevision):
		writeCatalogJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "imei_pool_revision_changed"})
	case errors.Is(err, ErrIMEIEntryNotFound):
		writeCatalogJSON(response, http.StatusNotFound, map[string]string{"code": "imei_pool_entry_not_found"})
	case errors.Is(err, ErrIMEIValueExists):
		writeCatalogJSON(response, http.StatusConflict, map[string]string{"code": "imei_value_in_pool"})
	case errors.Is(err, ErrIMEIValueInUse):
		writeCatalogJSON(response, http.StatusConflict, map[string]string{"code": "imei_value_in_use"})
	default:
		writeCatalogJSON(response, http.StatusInternalServerError, map[string]string{"code": "imei_pool_write_failed"})
	}
}

func (handler *IMEIPoolHandler) writeBindingError(response http.ResponseWriter, err error,
	poolRevision, catalogRevision uint64) {
	if poolRevision != 0 {
		response.Header().Set("ETag", revisionETag(poolRevision))
	}
	switch {
	case errors.Is(err, ErrIMEIPoolRevision):
		writeCatalogJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "imei_pool_revision_changed"})
	case errors.Is(err, ErrRevision):
		writeCatalogJSON(response, http.StatusPreconditionFailed, map[string]any{
			"code": "catalog_revision_changed", "catalog_revision": catalogRevision,
		})
	case errors.Is(err, ErrIMEIEntryNotFound), errors.Is(err, ErrNotFound):
		writeCatalogJSON(response, http.StatusNotFound, map[string]string{"code": "imei_binding_target_not_found"})
	case errors.Is(err, ErrCardInUse):
		writeCatalogJSON(response, http.StatusConflict, map[string]string{"code": "imei_binding_card_changed"})
	case errors.Is(err, ErrIMEIBinding):
		writeCatalogJSON(response, http.StatusConflict, map[string]string{"code": "imei_binding_changed"})
	default:
		writeCatalogJSON(response, http.StatusInternalServerError, map[string]string{"code": "imei_binding_failed"})
	}
}

func decodeIMEIPoolRequest(body io.Reader, target any) error {
	payload, err := io.ReadAll(io.LimitReader(body, maximumIMEIPoolRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumIMEIPoolRequestBytes {
		return errors.New("invalid IMEI pool request")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid IMEI pool request")
	}
	return nil
}
