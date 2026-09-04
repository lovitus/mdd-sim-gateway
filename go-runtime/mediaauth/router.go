// Package mediaauth binds an authenticated browser session to one exact
// browser-media session and the current generation of one local provider.
// It does not inspect WebSocket messages or own call/provider lifecycle.
package mediaauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
)

const defaultCapacity = 256

var ErrLeaseCapacity = errors.New("browser media lease capacity is exhausted")

// SessionVerifier returns a stable, non-secret subject for the currently
// authenticated browser cookie. Header-only CLI authentication must not be
// accepted because a browser WebSocket cannot reproduce it.
type SessionVerifier interface {
	VerifyBrowserSession(context.Context, *http.Request) (string, error)
}

type SessionVerifierFunc func(context.Context, *http.Request) (string, error)

func (function SessionVerifierFunc) VerifyBrowserSession(ctx context.Context, request *http.Request) (string, error) {
	return function(ctx, request)
}

// ProviderResolver returns a target only while generation is still current.
type ProviderResolver interface {
	ResolveMedia(context.Context, string, string, string) (mediaproxy.Target, error)
}

type LeaseRequest struct {
	Subject            string
	LineID             string
	CallID             string
	ProviderGeneration string
	ExpiresAt          time.Time
}

type Lease struct {
	SessionID          string
	LineID             string
	CallID             string
	ProviderGeneration string
	ExpiresAt          time.Time
}

type Router struct {
	mu        sync.Mutex
	verifier  SessionVerifier
	providers ProviderResolver
	now       func() time.Time
	capacity  int
	leases    map[string]leaseRecord
}

type leaseRecord struct {
	Lease
	subject string
}

func NewRouter(verifier SessionVerifier, providers ProviderResolver, now func() time.Time, capacity int) (*Router, error) {
	if verifier == nil || providers == nil {
		return nil, errors.New("browser media verifier and provider resolver are required")
	}
	if now == nil {
		now = time.Now
	}
	if capacity == 0 {
		capacity = defaultCapacity
	}
	if capacity < 1 || capacity > 4096 {
		return nil, errors.New("browser media lease capacity must be between 1 and 4096")
	}
	return &Router{verifier: verifier, providers: providers, now: now, capacity: capacity, leases: make(map[string]leaseRecord)}, nil
}

// Issue creates an opaque path identifier. The caller remains responsible for
// revoking it when the exact call/session ends.
func (router *Router) Issue(request LeaseRequest) (Lease, error) {
	now := router.now().UTC()
	request.Subject = strings.TrimSpace(request.Subject)
	request.LineID = strings.TrimSpace(request.LineID)
	request.CallID = strings.TrimSpace(request.CallID)
	request.ProviderGeneration = strings.TrimSpace(request.ProviderGeneration)
	request.ExpiresAt = request.ExpiresAt.UTC()
	if request.Subject == "" || !validID(request.LineID) || !validID(request.CallID) ||
		!validID(request.ProviderGeneration) || !request.ExpiresAt.After(now) {
		return Lease{}, errors.New("invalid browser media lease")
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	router.purgeExpiredLocked(now)
	if len(router.leases) >= router.capacity {
		return Lease{}, ErrLeaseCapacity
	}
	for attempts := 0; attempts < 4; attempts++ {
		sessionID, err := randomID()
		if err != nil {
			return Lease{}, err
		}
		if _, exists := router.leases[sessionID]; exists {
			continue
		}
		lease := Lease{SessionID: sessionID, LineID: request.LineID, CallID: request.CallID,
			ProviderGeneration: request.ProviderGeneration, ExpiresAt: request.ExpiresAt}
		router.leases[sessionID] = leaseRecord{Lease: lease, subject: request.Subject}
		return lease, nil
	}
	return Lease{}, errors.New("could not allocate a unique browser media lease")
}

func (router *Router) Revoke(sessionID string) {
	router.mu.Lock()
	delete(router.leases, strings.TrimSpace(sessionID))
	router.mu.Unlock()
}

// ActiveLine reports whether a non-expired browser media lease currently
// exists for the line. It is used by lifecycle operations as a fail-closed
// preflight; it does not revoke or mutate the lease.
func (router *Router) ActiveLine(lineID string) (bool, error) {
	lineID = strings.TrimSpace(lineID)
	now := router.now().UTC()
	router.mu.Lock()
	defer router.mu.Unlock()
	router.purgeExpiredLocked(now)
	for _, record := range router.leases {
		if record.LineID == lineID {
			return true, nil
		}
	}
	return false, nil
}

func (router *Router) AuthorizeMedia(ctx context.Context, request *http.Request) (mediaproxy.Target, error) {
	subject, err := router.verifier.VerifyBrowserSession(ctx, request)
	if err != nil || strings.TrimSpace(subject) == "" {
		return mediaproxy.Target{}, authorization(http.StatusUnauthorized, "login_required")
	}
	sessionID := strings.TrimSpace(request.PathValue("sessionID"))
	if !validID(sessionID) {
		return mediaproxy.Target{}, authorization(http.StatusNotFound, "media_lease_not_found")
	}
	router.mu.Lock()
	record, found := router.leases[sessionID]
	if found && !record.ExpiresAt.After(router.now().UTC()) {
		delete(router.leases, sessionID)
		found = false
	}
	router.mu.Unlock()
	if !found {
		return mediaproxy.Target{}, authorization(http.StatusGone, "media_lease_expired")
	}
	if !secureEqual(strings.TrimSpace(subject), record.subject) {
		return mediaproxy.Target{}, authorization(http.StatusForbidden, "media_lease_owner_mismatch")
	}
	target, err := router.providers.ResolveMedia(ctx, record.LineID, record.ProviderGeneration, record.SessionID)
	if err != nil {
		return mediaproxy.Target{}, authorization(http.StatusConflict, "media_provider_changed")
	}
	return target, nil
}

func (router *Router) purgeExpiredLocked(now time.Time) {
	for id, record := range router.leases {
		if !record.ExpiresAt.After(now) {
			delete(router.leases, id)
		}
	}
}

func authorization(status int, code string) error {
	return &mediaproxy.AuthorizationError{Status: status, Code: code}
}

func randomID() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			continue
		}
		return false
	}
	return true
}

func secureEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}
