package providerapply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const maximumDrainRequestBytes = 64 << 10

type DrainRequest struct {
	SchemaVersion   int      `json:"schema_version"`
	CatalogRevision uint64   `json:"catalog_revision"`
	LeaseID         string   `json:"lease_id"`
	LineIDs         []string `json:"line_ids"`
}

type DrainResult struct {
	SchemaVersion   int               `json:"schema_version"`
	CatalogRevision uint64            `json:"catalog_revision"`
	LeaseID         string            `json:"lease_id"`
	Ready           bool              `json:"ready"`
	Code            string            `json:"code"`
	Lines           []DrainLineResult `json:"lines"`
}

type DrainLineResult struct {
	LineID            string `json:"line_id"`
	Code              string `json:"code"`
	ProcessGeneration string `json:"process_generation,omitempty"`
}

func (handler *Handler) maintenance(response http.ResponseWriter, request *http.Request, begin bool) {
	input, err := decodeDrainRequest(request)
	if err != nil {
		write(response, http.StatusBadRequest, map[string]string{"code": "invalid_drain_request"})
		return
	}
	result, err := handler.Maintenance(request.Context(), input, begin)
	if err != nil {
		write(response, http.StatusConflict, result)
		return
	}
	write(response, http.StatusOK, result)
}

func (handler *Handler) Maintenance(parent context.Context, input DrainRequest, begin bool) (DrainResult, error) {
	result := DrainResult{
		SchemaVersion: 1, CatalogRevision: input.CatalogRevision, LeaseID: input.LeaseID,
		Lines: []DrainLineResult{},
	}
	if input.SchemaVersion != 1 || input.CatalogRevision == 0 ||
		(vowifiipc.MaintenanceRequest{LeaseID: input.LeaseID}).Validate() != nil || len(input.LineIDs) == 0 {
		result.Code = "invalid_request"
		return result, errors.New("invalid drain request")
	}
	catalog, err := handler.catalog.Snapshot()
	if err != nil || catalog.Revision != input.CatalogRevision {
		result.Code = "catalog_revision_changed"
		return result, errors.New("catalog revision changed")
	}
	known := make(map[string]struct{}, len(catalog.Lines))
	for _, line := range catalog.Lines {
		known[line.ID] = struct{}{}
	}
	lineIDs := append([]string(nil), input.LineIDs...)
	sort.Strings(lineIDs)
	for index, lineID := range lineIDs {
		if strings.TrimSpace(lineID) != lineID {
			result.Code = "invalid_line"
			return result, errors.New("invalid drain line")
		}
		if _, found := known[lineID]; !found || (index > 0 && lineIDs[index-1] == lineID) {
			result.Code = "unknown_or_duplicate_line"
			return result, errors.New("unknown or duplicate drain line")
		}
	}
	ctx, cancel := context.WithTimeout(parent, 7*time.Second)
	defer cancel()
	result.Lines = make([]DrainLineResult, len(lineIDs))
	var wait sync.WaitGroup
	for index, lineID := range lineIDs {
		index, lineID := index, lineID
		wait.Add(1)
		go func() {
			defer wait.Done()
			result.Lines[index] = handler.maintenanceLine(ctx, lineID, input.LeaseID, begin)
		}()
	}
	wait.Wait()
	failed := false
	for _, line := range result.Lines {
		if begin {
			failed = failed || (line.Code != "drained" && line.Code != "provider_absent")
		} else {
			failed = failed || line.Code != "resumed"
		}
	}
	latest, revisionErr := handler.catalog.Snapshot()
	if revisionErr != nil || latest.Revision != input.CatalogRevision {
		failed = true
		result.Code = "catalog_revision_changed"
	}
	if begin && failed {
		handler.releasePartialDrain(input.LeaseID, result.Lines)
	}
	result.Ready = !failed
	if failed {
		if result.Code == "" {
			result.Code = "line_not_drained"
		}
		return result, errors.New("provider maintenance request was not complete")
	}
	result.Code = "drained"
	if !begin {
		result.Code = "resumed"
	}
	return result, nil
}

func (handler *Handler) maintenanceLine(ctx context.Context, lineID, leaseID string, begin bool) DrainLineResult {
	result := DrainLineResult{LineID: lineID, Code: "provider_absent"}
	err := handler.providers.UseCurrent(ctx, lineID, func(provider mediaauth.Provider) error {
		client, err := handler.providerClient(provider)
		if err != nil {
			return err
		}
		var maintenance vowifiipc.MaintenanceResult
		if begin {
			maintenance, err = client.BeginDrain(ctx, vowifiipc.MaintenanceRequest{LeaseID: leaseID})
		} else {
			maintenance, err = client.EndDrain(ctx, vowifiipc.MaintenanceRequest{LeaseID: leaseID})
		}
		if err != nil {
			result.Code = maintenanceErrorCode(err)
			return nil
		}
		if maintenance.Draining != begin {
			result.Code = "maintenance_state_mismatch"
			return nil
		}
		if maintenance.Status.LineID != lineID || maintenance.Status.ProviderID != provider.ProviderID ||
			maintenance.Status.ProcessGeneration != provider.Generation {
			result.Code = "identity_mismatch"
			return nil
		}
		result.ProcessGeneration = provider.Generation
		if begin {
			result.Code = "drained"
		} else {
			result.Code = "resumed"
		}
		return nil
	})
	if err != nil && !errors.Is(err, mediaauth.ErrProviderUnavailable) {
		result.Code = "provider_unreachable"
	}
	return result
}

func (handler *Handler) releasePartialDrain(leaseID string, lines []DrainLineResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	for index := range lines {
		if lines[index].Code != "drained" {
			continue
		}
		index, lineID := index, lines[index].LineID
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := handler.providers.UseCurrent(ctx, lineID, func(provider mediaauth.Provider) error {
				client, err := handler.providerClient(provider)
				if err != nil {
					return err
				}
				_, err = client.EndDrain(ctx, vowifiipc.MaintenanceRequest{LeaseID: leaseID})
				return err
			})
			if err != nil {
				lines[index].Code = "drain_rollback_failed"
			} else {
				lines[index].Code = "drain_rolled_back"
			}
		}()
	}
	wait.Wait()
}

func (handler *Handler) providerClient(provider mediaauth.Provider) (*vowifiipc.Client, error) {
	parsed, err := url.Parse(provider.BaseURL)
	if err != nil || parsed.Scheme != "ws" {
		return nil, errors.New("invalid provider route")
	}
	parsed.Scheme = "http"
	return vowifiipc.NewClient(parsed.String(), provider.Token, handler.http)
}

func maintenanceErrorCode(err error) string {
	var response *vowifiipc.ResponseError
	if errors.As(err, &response) && response.Failure.Code != "" {
		return response.Failure.Code
	}
	return "provider_unreachable"
}

func decodeDrainRequest(request *http.Request) (DrainRequest, error) {
	var input DrainRequest
	if request.Header.Get("Content-Type") != "application/json" {
		return input, errors.New("JSON content type required")
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumDrainRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumDrainRequestBytes {
		return input, errors.New("invalid drain request size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return input, errors.New("drain request has trailing JSON")
	}
	return input, nil
}
