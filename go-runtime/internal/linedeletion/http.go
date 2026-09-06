package linedeletion

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

type linePurger interface{ PurgeLine(string) error }
type historyFinalizer interface {
	PurgeLine(string) error
	RetainLine(string) error
}

type Config struct {
	Catalog       *linecatalog.Store
	Guard         linecatalog.LifecycleGuard
	Notifications linePurger
	Events        linePurger
	Allowance     linePurger
	Messages      historyFinalizer
	SMSOperations linePurger
	Calls         historyFinalizer
	Now           func() time.Time
}

type Handler struct{ config Config }

type requestBody struct {
	SchemaVersion int    `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	DeleteHistory *bool  `json:"delete_history,omitempty"`
}

func NewHandler(config Config) (*Handler, error) {
	if config.Catalog == nil || config.Guard == nil || config.Notifications == nil || config.Events == nil || config.Allowance == nil ||
		config.Messages == nil || config.SMSOperations == nil || config.Calls == nil {
		return nil, errors.New("invalid line deletion handler configuration")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Handler{config: config}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	lineID := strings.TrimSpace(request.PathValue("lineID"))
	expected, err := parseRevision(request.Header.Get("If-Match"))
	if err != nil {
		writeJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "catalog_revision_required"})
		return
	}
	var input requestBody
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.SchemaVersion != 1 || !linecatalog.ValidOperationID(input.OperationID) ||
		decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_deletion_request"})
		return
	}
	deleteHistory := true
	if input.DeleteHistory != nil {
		deleteHistory = *input.DeleteHistory
	}
	prior, found, readErr := handler.config.Catalog.GetDeletion(input.OperationID)
	if readErr != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "line_deletion_incomplete"})
		return
	}
	if found {
		if prior.LineID != lineID || prior.DeleteHistory != deleteHistory {
			writeJSON(response, http.StatusConflict, map[string]any{"code": "line_deletion_conflict", "operation": prior})
			return
		}
		if prior.Stage == linecatalog.DeletionSucceeded {
			handler.writeSuccess(response, prior)
			return
		}
	}
	if active, guardErr := handler.config.Guard.ActiveLine(lineID); guardErr != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "line_lease_state_unavailable"})
		return
	} else if active {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "line_active_lease"})
		return
	}
	receipt, _, err := handler.config.Catalog.PrepareDeletionExpected(lineID, input.OperationID, deleteHistory, expected, handler.config.Now())
	if err != nil {
		if errors.Is(err, linecatalog.ErrDeletionConflict) && receipt.OperationID != "" {
			writeJSON(response, http.StatusConflict, map[string]any{"code": "line_deletion_conflict", "operation": receipt})
			return
		}
		handler.writeStoreError(response, err, receipt.CatalogRevision)
		return
	}
	steps := []struct {
		expected linecatalog.DeletionStage
		next     linecatalog.DeletionStage
		run      func(string) error
	}{
		{linecatalog.DeletionPrepared, linecatalog.DeletionNotifications, handler.config.Notifications.PurgeLine},
		{linecatalog.DeletionNotifications, linecatalog.DeletionEvents, handler.config.Events.PurgeLine},
		{linecatalog.DeletionEvents, linecatalog.DeletionAllowance, handler.config.Allowance.PurgeLine},
		{linecatalog.DeletionAllowance, linecatalog.DeletionMessages, func(lineID string) error {
			if receipt.DeleteHistory {
				return handler.config.Messages.PurgeLine(lineID)
			}
			return handler.config.Messages.RetainLine(lineID)
		}},
		{linecatalog.DeletionMessages, linecatalog.DeletionSMSOperations, handler.config.SMSOperations.PurgeLine},
		{linecatalog.DeletionSMSOperations, linecatalog.DeletionCalls, func(lineID string) error {
			if receipt.DeleteHistory {
				return handler.config.Calls.PurgeLine(lineID)
			}
			return handler.config.Calls.RetainLine(lineID)
		}},
	}
	for _, step := range steps {
		if receipt.Stage == linecatalog.DeletionSucceeded {
			break
		}
		if receipt.Stage != step.expected {
			continue
		}
		if err := step.run(lineID); err != nil {
			writeJSON(response, http.StatusServiceUnavailable, map[string]any{"code": "line_deletion_incomplete", "operation": receipt})
			return
		}
		receipt, err = handler.config.Catalog.AdvanceDeletion(input.OperationID, step.expected, step.next, handler.config.Now())
		if err != nil {
			writeJSON(response, http.StatusServiceUnavailable, map[string]any{"code": "line_deletion_incomplete", "operation": receipt})
			return
		}
	}
	if receipt.Stage != linecatalog.DeletionSucceeded {
		if active, guardErr := handler.config.Guard.ActiveLine(lineID); guardErr != nil || active {
			writeJSON(response, http.StatusConflict, map[string]any{"code": "line_active_lease", "operation": receipt})
			return
		}
		receipt, err = handler.config.Catalog.FinalizeDeletion(input.OperationID, handler.config.Now())
		if err != nil {
			handler.writeStoreError(response, err, receipt.CatalogRevision)
			return
		}
	}
	handler.writeSuccess(response, receipt)
}

func (handler *Handler) writeSuccess(response http.ResponseWriter, receipt linecatalog.DeletionReceipt) {
	response.Header().Set("ETag", `"`+strconv.FormatUint(receipt.CatalogRevision, 10)+`"`)
	writeJSON(response, http.StatusOK, map[string]any{"schema_version": 1, "operation": receipt})
}

func (handler *Handler) writeStoreError(response http.ResponseWriter, err error, revision uint64) {
	switch {
	case errors.Is(err, linecatalog.ErrRevision):
		if revision > 0 {
			response.Header().Set("ETag", `"`+strconv.FormatUint(revision, 10)+`"`)
		}
		writeJSON(response, http.StatusPreconditionFailed, map[string]string{"code": "catalog_revision_changed"})
	case errors.Is(err, linecatalog.ErrNotFound):
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "line_not_found"})
	case errors.Is(err, linecatalog.ErrLineNotDeleted):
		writeJSON(response, http.StatusConflict, map[string]string{"code": "line_not_in_recycle_bin"})
	case errors.Is(err, linecatalog.ErrLineActive), errors.Is(err, linecatalog.ErrLineOperationActive):
		writeJSON(response, http.StatusConflict, map[string]string{"code": "line_not_inactive"})
	case errors.Is(err, linecatalog.ErrDeletionConflict):
		writeJSON(response, http.StatusConflict, map[string]string{"code": "line_deletion_conflict"})
	default:
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "line_deletion_incomplete"})
	}
}

func parseRevision(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("missing revision")
	}
	return strconv.ParseUint(value[1:len(value)-1], 10, 64)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
