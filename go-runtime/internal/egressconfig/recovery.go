package egressconfig

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// RecoveryPath is served only on the authenticated apply-helper Unix socket.
const RecoveryPath = "/v1/system/egress-recovery"

type RecoveryRequest struct {
	SchemaVersion      int    `json:"schema_version"`
	ConfigRevision     uint64 `json:"config_revision"`
	CatalogRevision    uint64 `json:"catalog_revision"`
	LineID             string `json:"line_id"`
	ProviderGeneration string `json:"provider_generation"`
	FailureID          string `json:"failure_id"`
	ExpectedGeneration string `json:"expected_generation"`
	Country            string `json:"country"`
	FromNode           string `json:"from_node"`
	ToNode             string `json:"to_node"`
}

func (request RecoveryRequest) Validate() error {
	country, ok := normalizeCountry(request.Country)
	if request.SchemaVersion != SchemaVersion || request.ConfigRevision == 0 || request.CatalogRevision == 0 ||
		!ok || country != request.Country || !validIdentifier(request.LineID, 128) || !validIdentifier(request.ProviderGeneration, 128) ||
		len(request.FailureID) != 64 || strings.TrimLeft(request.FailureID, "0123456789abcdef") != "" ||
		len(request.ExpectedGeneration) != 64 || strings.TrimLeft(request.ExpectedGeneration, "0123456789abcdef") != "" ||
		strings.TrimSpace(request.FromNode) == "" || strings.TrimSpace(request.ToNode) == "" || request.FromNode == request.ToNode ||
		len(request.FromNode) > 512 || len(request.ToNode) > 512 || containsControl(request.FromNode) || containsControl(request.ToNode) {
		return errors.New("invalid exit recovery request")
	}
	return nil
}

type RecoveryService interface {
	RecoverEgress(context.Context, RecoveryRequest) (ApplyResult, error)
}

func RecoveryHandler(service RecoveryService) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", "POST")
			writeApplyError(response, &ApplyError{Status: 405, Code: "method_not_allowed"})
			return
		}
		var input RecoveryRequest
		if err := decodeApplyRequest(response, request, &input); err != nil || input.Validate() != nil {
			writeApplyError(response, &ApplyError{Status: 400, Code: "invalid_egress_recovery_request"})
			return
		}
		result, err := service.RecoverEgress(request.Context(), input)
		if err != nil {
			writeApplyError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
	})
}

func (client *ApplyClient) RecoverEgress(ctx context.Context, request RecoveryRequest) (ApplyResult, error) {
	var result ApplyResult
	if err := request.Validate(); err != nil {
		return result, err
	}
	err := client.requestAt(ctx, http.MethodPost, RecoveryPath, request, &result)
	return result, err
}
