// Package providerapply supplies the read-only evidence required before an
// explicit deployment adapter may replace provider process configurations.
package providerapply

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
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

const Path = "/v1/provider/apply-preflight"

type Snapshot struct {
	SchemaVersion   int          `json:"schema_version"`
	CatalogRevision uint64       `json:"catalog_revision"`
	Lines           []LineStatus `json:"lines"`
}

type LineStatus struct {
	LineID            string                  `json:"line_id"`
	Code              string                  `json:"code"`
	ProviderPresent   bool                    `json:"provider_present"`
	ProcessGeneration string                  `json:"process_generation,omitempty"`
	Runtime           vowifiipc.RuntimeStatus `json:"runtime"`
	ActiveCall        *vowifiipc.ActiveCall   `json:"active_call,omitempty"`
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
	if request.Method != http.MethodGet || request.URL.Path != Path || request.URL.RawQuery != "" {
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
	snapshot, err := handler.Snapshot(request.Context())
	if err != nil {
		write(response, http.StatusInternalServerError, map[string]string{"code": "preflight_failed"})
		return
	}
	write(response, http.StatusOK, snapshot)
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
