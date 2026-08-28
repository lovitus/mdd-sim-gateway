// Package providerapply supplies the evidence and narrow durable maintenance
// lease required before an explicit deployment adapter may replace providers.
package providerapply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const (
	Path       = "/v1/provider/apply-preflight"
	DrainPath  = "/v1/provider/apply-drain"
	ResumePath = "/v1/provider/apply-resume"
)

type Snapshot struct {
	SchemaVersion   int          `json:"schema_version"`
	CatalogRevision uint64       `json:"catalog_revision"`
	Lines           []LineStatus `json:"lines"`
}

type LineStatus struct {
	LineID            string                      `json:"line_id"`
	Code              string                      `json:"code"`
	ProviderPresent   bool                        `json:"provider_present"`
	ProcessGeneration string                      `json:"process_generation,omitempty"`
	Runtime           vowifiipc.RuntimeStatus     `json:"runtime"`
	Maintenance       vowifiipc.MaintenanceStatus `json:"maintenance"`
	ActiveCall        *vowifiipc.ActiveCall       `json:"active_call,omitempty"`
}

type Handler struct {
	catalog   *linecatalog.Store
	providers *mediaauth.ProviderDirectory
	tokenHash [sha256.Size]byte
	http      *http.Client
}

func NewHandler(catalog *linecatalog.Store, providers *mediaauth.ProviderDirectory, token string, client *http.Client) (*Handler, error) {
	if catalog == nil || providers == nil || len(token) < 32 {
		return nil, errors.New("invalid provider apply preflight configuration")
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	} else {
		clone := *client
		client = &clone
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Handler{catalog: catalog, providers: providers, tokenHash: sha256.Sum256([]byte(token)), http: client}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if !loopbackRemote(request.RemoteAddr) {
		write(response, http.StatusForbidden, map[string]string{"code": "loopback_required"})
		return
	}
	if !handler.authorized(request.Header.Get("Authorization")) {
		write(response, http.StatusUnauthorized, map[string]string{"code": "unauthorized"})
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == Path:
		snapshot, err := handler.Snapshot(request.Context())
		if err != nil {
			write(response, http.StatusInternalServerError, map[string]string{"code": "preflight_failed"})
			return
		}
		write(response, http.StatusOK, snapshot)
	case request.Method == http.MethodPost && request.URL.Path == DrainPath:
		handler.maintenance(response, request, true)
	case request.Method == http.MethodPost && request.URL.Path == ResumePath:
		handler.maintenance(response, request, false)
	default:
		http.NotFound(response, request)
	}
}

func (handler *Handler) Snapshot(parent context.Context) (Snapshot, error) {
	catalog, err := handler.catalog.Snapshot()
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{SchemaVersion: 1, CatalogRevision: catalog.Revision, Lines: make([]LineStatus, len(catalog.Lines))}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	for index, line := range catalog.Lines {
		index, lineID := index, line.ID
		wait.Add(1)
		go func() {
			defer wait.Done()
			result.Lines[index] = handler.lineStatus(ctx, lineID)
		}()
	}
	wait.Wait()
	sort.Slice(result.Lines, func(left, right int) bool { return result.Lines[left].LineID < result.Lines[right].LineID })
	return result, nil
}

func (handler *Handler) lineStatus(ctx context.Context, lineID string) LineStatus {
	result := LineStatus{LineID: lineID, Code: "provider_absent"}
	provider, found := handler.providers.CurrentProvider(lineID)
	if !found {
		return result
	}
	result.ProviderPresent = true
	parsed, err := url.Parse(provider.BaseURL)
	if err != nil || parsed.Scheme != "ws" {
		result.Code = "provider_unreachable"
		return result
	}
	parsed.Scheme = "http"
	client, err := vowifiipc.NewClient(parsed.String(), provider.Token, handler.http)
	if err != nil {
		result.Code = "provider_unreachable"
		return result
	}
	status, err := client.Status(ctx)
	currentGeneration, current := handler.providers.CurrentGeneration(lineID)
	if err != nil || !current || currentGeneration != provider.Generation || status.LineID != lineID ||
		status.ProviderID != provider.ProviderID || status.ProcessGeneration != provider.Generation {
		result.Code = "provider_unreachable"
		return result
	}
	result.Code = "provider_reachable"
	result.ProcessGeneration = status.ProcessGeneration
	result.Runtime = status.Runtime
	result.Maintenance = status.Maintenance
	result.ActiveCall = status.ActiveCall
	return result
}

func (handler *Handler) authorized(header string) bool {
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	return subtle.ConstantTimeCompare(presented[:], handler.tokenHash[:]) == 1
}

func loopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func write(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func Fetch(ctx context.Context, endpoint, token string, client *http.Client) (Snapshot, error) {
	var snapshot Snapshot
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path != Path || parsed.Port() == "" || len(token) < 32 {
		return snapshot, errors.New("invalid provider preflight client configuration")
	}
	address := net.ParseIP(parsed.Hostname())
	if address == nil || !address.IsLoopback() {
		return snapshot, errors.New("provider preflight requires literal loopback")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return snapshot, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if client == nil {
		client = &http.Client{}
	} else {
		clone := *client
		client = &clone
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return snapshot, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return snapshot, err
	}
	if len(payload) > 1<<20 {
		return snapshot, errors.New("provider preflight response is too large")
	}
	if response.StatusCode != http.StatusOK {
		return snapshot, errors.New("provider preflight was rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		snapshot.SchemaVersion != 1 || snapshot.CatalogRevision == 0 {
		return Snapshot{}, errors.New("provider preflight returned an invalid snapshot")
	}
	return snapshot, nil
}
