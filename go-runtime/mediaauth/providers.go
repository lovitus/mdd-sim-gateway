package mediaauth

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
)

var (
	ErrProviderGenerationReused = errors.New("a replaced media provider generation cannot become current again")
	ErrProviderUnavailable      = errors.New("current media provider is unavailable")
	ErrProviderFenceConflict    = errors.New("current media provider no longer matches the expected route")
)

type Provider struct {
	LineID     string `json:"line_id"`
	ProviderID string `json:"provider_id"`
	Generation string `json:"generation"`
	CardID     string `json:"card_id"`
	BaseURL    string `json:"base_url"`
	Token      string `json:"token"`
}

// ProviderFence is the complete non-secret routing identity observed before a
// bounded lifecycle operation. CardID is part of the fence so an operation
// observed for one SIM cannot be delivered to a same-line replacement bound
// to another SIM.
type ProviderFence struct {
	LineID     string
	ProviderID string
	Generation string
	CardID     string
}

func (provider Provider) Fence() ProviderFence {
	return ProviderFence{
		LineID: provider.LineID, ProviderID: provider.ProviderID,
		Generation: provider.Generation, CardID: provider.CardID,
	}
}

// ProviderDirectory contains routing identity only. Runtime health and
// recovery state deliberately stay outside this directory.
type ProviderDirectory struct {
	mu        sync.RWMutex
	current   map[string]providerRecord
	seen      map[string]map[string]struct{}
	operation sync.Mutex
	lineLocks map[string]*sync.RWMutex
	now       func() time.Time
	ttl       time.Duration
}

type providerRecord struct {
	Provider
	lastSeen time.Time
}

func NewProviderDirectory() *ProviderDirectory {
	directory, _ := NewProviderDirectoryWithClock(time.Now, 30*time.Second)
	return directory
}

func NewProviderDirectoryWithClock(now func() time.Time, ttl time.Duration) (*ProviderDirectory, error) {
	if now == nil || ttl < time.Second || ttl > 5*time.Minute {
		return nil, errors.New("invalid media provider directory clock or TTL")
	}
	return &ProviderDirectory{
		current: make(map[string]providerRecord), seen: make(map[string]map[string]struct{}),
		lineLocks: make(map[string]*sync.RWMutex), now: now, ttl: ttl,
	}, nil
}

func (directory *ProviderDirectory) Replace(provider Provider) error {
	provider.LineID = strings.TrimSpace(provider.LineID)
	provider.ProviderID = strings.TrimSpace(provider.ProviderID)
	provider.Generation = strings.TrimSpace(provider.Generation)
	provider.CardID = strings.TrimSpace(provider.CardID)
	provider.BaseURL = strings.TrimSuffix(strings.TrimSpace(provider.BaseURL), "/")
	if !validID(provider.LineID) || !validID(provider.ProviderID) || !validID(provider.Generation) || validateProvider(provider) != nil {
		return errors.New("invalid browser media provider")
	}
	// A same-generation heartbeat changes no routing identity and must remain
	// able to refresh while a bounded operation is in flight.
	directory.mu.Lock()
	if current, found := directory.current[provider.LineID]; found && current.Generation == provider.Generation {
		if current.Provider != provider {
			directory.mu.Unlock()
			return errors.New("media provider identity changed inside one generation")
		}
		current.lastSeen = directory.now().UTC()
		directory.current[provider.LineID] = current
		directory.mu.Unlock()
		return nil
	}
	directory.mu.Unlock()

	lineLock := directory.lineLock(provider.LineID)
	lineLock.Lock()
	defer lineLock.Unlock()
	directory.mu.Lock()
	defer directory.mu.Unlock()
	if current, found := directory.current[provider.LineID]; found && current.Generation == provider.Generation {
		if current.Provider != provider {
			return errors.New("media provider identity changed inside one generation")
		}
		current.lastSeen = directory.now().UTC()
		directory.current[provider.LineID] = current
		return nil
	}
	if _, reused := directory.seen[provider.LineID][provider.Generation]; reused {
		return ErrProviderGenerationReused
	}
	if directory.seen[provider.LineID] == nil {
		directory.seen[provider.LineID] = make(map[string]struct{})
	}
	directory.seen[provider.LineID][provider.Generation] = struct{}{}
	directory.current[provider.LineID] = providerRecord{Provider: provider, lastSeen: directory.now().UTC()}
	return nil
}

// Remove is generation-aware so a late shutdown cannot remove its replacement.
func (directory *ProviderDirectory) Remove(lineID, generation string) {
	lineID = strings.TrimSpace(lineID)
	lineLock := directory.lineLock(lineID)
	lineLock.Lock()
	defer lineLock.Unlock()
	directory.mu.Lock()
	if current, found := directory.current[lineID]; found && current.Generation == strings.TrimSpace(generation) {
		delete(directory.current, lineID)
	}
	directory.mu.Unlock()
}

func (directory *ProviderDirectory) ResolveMedia(_ context.Context, lineID, generation, sessionID string) (mediaproxy.Target, error) {
	directory.mu.RLock()
	provider, found := directory.current[lineID]
	directory.mu.RUnlock()
	if !found || provider.Generation != generation || !validID(sessionID) ||
		directory.now().UTC().Sub(provider.lastSeen) > directory.ttl {
		return mediaproxy.Target{}, errors.New("browser media provider generation changed")
	}
	return mediaproxy.Target{URL: provider.BaseURL + "/v1/media/" + sessionID, Token: provider.Token}, nil
}

func (directory *ProviderDirectory) CurrentGeneration(lineID string) (string, bool) {
	directory.mu.RLock()
	provider, found := directory.current[strings.TrimSpace(lineID)]
	directory.mu.RUnlock()
	if !found || directory.now().UTC().Sub(provider.lastSeen) > directory.ttl {
		return "", false
	}
	return provider.Generation, true
}

// CurrentProvider returns a point-in-time routing record for read-only probes.
// Callers must re-check CurrentGeneration after I/O; mutating operations must
// continue to use UseCurrent so replacement is linearized with the action.
func (directory *ProviderDirectory) CurrentProvider(lineID string) (Provider, bool) {
	directory.mu.RLock()
	provider, found := directory.current[strings.TrimSpace(lineID)]
	directory.mu.RUnlock()
	if !found || directory.now().UTC().Sub(provider.lastSeen) > directory.ttl {
		return Provider{}, false
	}
	return provider.Provider, true
}

// UseCurrent linearizes one bounded control operation with replacement of the
// same line. Other lines and same-generation heartbeats remain independent,
// so an operation resolved against generation A cannot be delivered after
// generation B has become current without stalling unrelated lines.
func (directory *ProviderDirectory) UseCurrent(ctx context.Context, lineID string, use func(Provider) error) error {
	lineID = strings.TrimSpace(lineID)
	if directory == nil || !validID(lineID) || use == nil {
		return ErrProviderUnavailable
	}
	lineLock := directory.lineLock(lineID)
	lineLock.RLock()
	defer lineLock.RUnlock()
	directory.mu.RLock()
	provider, found := directory.current[lineID]
	directory.mu.RUnlock()
	if !found || directory.now().UTC().Sub(provider.lastSeen) > directory.ttl {
		return ErrProviderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(provider.Provider)
}

// UseExpected linearizes one bounded control operation with replacement and
// accepts only the exact route observed earlier. A missing or expired route is
// unavailable; a live replacement is a conflict. Neither case falls through
// to the replacement Provider.
func (directory *ProviderDirectory) UseExpected(ctx context.Context, expected ProviderFence, use func(Provider) error) error {
	expected.LineID = strings.TrimSpace(expected.LineID)
	expected.ProviderID = strings.TrimSpace(expected.ProviderID)
	expected.Generation = strings.TrimSpace(expected.Generation)
	expected.CardID = strings.TrimSpace(expected.CardID)
	if directory == nil || !validID(expected.LineID) || !validID(expected.ProviderID) ||
		!validID(expected.Generation) || (expected.CardID != "" && !validCardID(expected.CardID)) || use == nil {
		return ErrProviderUnavailable
	}
	lineLock := directory.lineLock(expected.LineID)
	lineLock.RLock()
	defer lineLock.RUnlock()
	directory.mu.RLock()
	provider, found := directory.current[expected.LineID]
	directory.mu.RUnlock()
	if !found || directory.now().UTC().Sub(provider.lastSeen) > directory.ttl {
		return ErrProviderUnavailable
	}
	if provider.Provider.Fence() != expected {
		return ErrProviderFenceConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(provider.Provider)
}

func (directory *ProviderDirectory) lineLock(lineID string) *sync.RWMutex {
	directory.operation.Lock()
	defer directory.operation.Unlock()
	lock := directory.lineLocks[lineID]
	if lock == nil {
		lock = &sync.RWMutex{}
		directory.lineLocks[lineID] = lock
	}
	return lock
}

func validateProvider(provider Provider) error {
	if provider.CardID != "" && !validCardID(provider.CardID) {
		return errors.New("provider card identity is invalid")
	}
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

func validCardID(value string) bool {
	if len(value) < 4 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
