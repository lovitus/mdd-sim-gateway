package mediaauth

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
)

var ErrProviderGenerationReused = errors.New("a replaced media provider generation cannot become current again")

type Provider struct {
	LineID     string
	Generation string
	BaseURL    string
	Token      string
}

// ProviderDirectory contains routing identity only. Runtime health and
// recovery state deliberately stay outside this directory.
type ProviderDirectory struct {
	mu      sync.RWMutex
	current map[string]Provider
	seen    map[string]map[string]struct{}
}

func NewProviderDirectory() *ProviderDirectory {
	return &ProviderDirectory{current: make(map[string]Provider), seen: make(map[string]map[string]struct{})}
}

func (directory *ProviderDirectory) Replace(provider Provider) error {
	provider.LineID = strings.TrimSpace(provider.LineID)
	provider.Generation = strings.TrimSpace(provider.Generation)
	provider.BaseURL = strings.TrimSuffix(strings.TrimSpace(provider.BaseURL), "/")
	if !validID(provider.LineID) || !validID(provider.Generation) || validateProvider(provider) != nil {
		return errors.New("invalid browser media provider")
	}
	directory.mu.Lock()
	defer directory.mu.Unlock()
	if current, found := directory.current[provider.LineID]; found && current.Generation == provider.Generation {
		if current != provider {
			return errors.New("media provider identity changed inside one generation")
		}
		return nil
	}
	if _, reused := directory.seen[provider.LineID][provider.Generation]; reused {
		return ErrProviderGenerationReused
	}
	if directory.seen[provider.LineID] == nil {
		directory.seen[provider.LineID] = make(map[string]struct{})
	}
	directory.seen[provider.LineID][provider.Generation] = struct{}{}
	directory.current[provider.LineID] = provider
	return nil
}

// Remove is generation-aware so a late shutdown cannot remove its replacement.
func (directory *ProviderDirectory) Remove(lineID, generation string) {
	directory.mu.Lock()
	if current, found := directory.current[strings.TrimSpace(lineID)]; found && current.Generation == strings.TrimSpace(generation) {
		delete(directory.current, strings.TrimSpace(lineID))
	}
	directory.mu.Unlock()
}

func (directory *ProviderDirectory) ResolveMedia(_ context.Context, lineID, generation, sessionID string) (mediaproxy.Target, error) {
	directory.mu.RLock()
	provider, found := directory.current[lineID]
	directory.mu.RUnlock()
	if !found || provider.Generation != generation || !validID(sessionID) {
		return mediaproxy.Target{}, errors.New("browser media provider generation changed")
	}
	return mediaproxy.Target{URL: provider.BaseURL + "/v1/media/" + sessionID, Token: provider.Token}, nil
}

func validateProvider(provider Provider) error {
	parsed, err := url.Parse(provider.BaseURL)
	if err != nil || parsed.Scheme != "ws" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") || parsed.Port() == "" {
		return errors.New("provider base URL must be a loopback ws origin")
	}
	address := net.ParseIP(strings.Trim(parsed.Hostname(), "[]"))
	if address == nil || !address.IsLoopback() {
		return errors.New("provider base URL must use a literal loopback address")
	}
	return (mediaproxy.Target{URL: provider.BaseURL + "/v1/media/probe", Token: provider.Token}).Validate()
}
