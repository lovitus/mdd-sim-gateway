package providerapply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

var ErrMaintenanceBlocked = errors.New("provider maintenance request is blocked")

func RequestMaintenance(ctx context.Context, coreBaseURL, token string, input DrainRequest, begin bool,
	client *http.Client,
) (DrainResult, error) {
	var result DrainResult
	parsed, err := url.Parse(strings.TrimSpace(coreBaseURL))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.Port() == "" || len(token) < 32 {
		return result, errors.New("invalid provider maintenance client configuration")
	}
	address := net.ParseIP(parsed.Hostname())
	if address == nil || !address.IsLoopback() {
		return result, errors.New("provider maintenance requires literal loopback")
	}
	if input.SchemaVersion != 1 || input.CatalogRevision == 0 || len(input.LineIDs) == 0 ||
		(vowifiipc.MaintenanceRequest{LeaseID: input.LeaseID}).Validate() != nil {
		return result, errors.New("invalid provider maintenance request")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	path := DrainPath
	if !begin {
		path = ResumePath
	}
	parsed.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return result, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	} else {
		clone := *client
		client = &clone
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	wire, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return result, err
	}
	if len(wire) > 1<<20 {
		return result, errors.New("provider maintenance response is too large")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict {
		return result, errors.New("provider maintenance request was rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		result.SchemaVersion != 1 || result.CatalogRevision != input.CatalogRevision || result.LeaseID != input.LeaseID ||
		len(result.Lines) != len(input.LineIDs) || strings.TrimSpace(result.Code) == "" {
		return DrainResult{}, errors.New("provider maintenance returned an invalid result")
	}
	seen := make(map[string]struct{}, len(result.Lines))
	for _, line := range result.Lines {
		if strings.TrimSpace(line.LineID) == "" || strings.TrimSpace(line.Code) == "" {
			return DrainResult{}, errors.New("provider maintenance returned an invalid line result")
		}
		seen[line.LineID] = struct{}{}
	}
	for _, lineID := range input.LineIDs {
		if _, found := seen[lineID]; !found {
			return DrainResult{}, errors.New("provider maintenance omitted a requested line")
		}
	}
	if response.StatusCode == http.StatusConflict || !result.Ready {
		return result, ErrMaintenanceBlocked
	}
	return result, nil
}
