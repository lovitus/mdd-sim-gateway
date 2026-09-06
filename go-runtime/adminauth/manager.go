// Package adminauth provides the single-administrator authentication contract
// used by the MDD browser and CLI. It reads the existing auth.json scrypt
// credential format and keeps sessions in memory only.
package adminauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
)

const (
	SessionCookie       = "mdd_session"
	SessionTTL          = 12 * time.Hour
	maxSessions         = 1024
	maxAuthBytes        = 16 << 10
	maxAgentCredentials = 64
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrSessionCapacity    = errors.New("administrator session capacity is exhausted")
	ErrAuthentication     = errors.New("authentication required")
	ErrCSRF               = errors.New("invalid CSRF token")
	ErrAgentCredential    = errors.New("Agent credential unavailable")
)

const (
	AgentCredentialTransition = "transition"
	AgentCredentialScoped     = "scoped"
)

type ThrottleError struct{ RetryAfter int }

func (failure *ThrottleError) Error() string { return "too many login attempts" }

type credentialFile struct {
	Version        int               `json:"version"`
	Username       string            `json:"username"`
	Salt           string            `json:"salt"`
	PasswordHash   string            `json:"password_hash"`
	AgentToken     string            `json:"agent_token"`
	AgentTokenMode string            `json:"agent_token_mode,omitempty"`
	AgentTokens    map[string]string `json:"agent_tokens,omitempty"`
}

type Manager struct {
	mu             sync.Mutex
	credentialPath string
	username       string
	salt           []byte
	passwordHash   []byte
	agentToken     string
	agentTokenMode string
	agentTokens    map[string]string
	secureCookies  bool
	now            func() time.Time
	sessions       map[[32]byte]sessionRecord
	failures       map[string][]time.Time
	persister      CredentialPersister
}

type CredentialPersister interface {
	PersistCredential([]byte) error
}

type ManagerOption func(*Manager) error

func WithCredentialPersister(persister CredentialPersister) ManagerOption {
	return func(manager *Manager) error {
		if persister == nil {
			return errors.New("administrator credential persister is required")
		}
		manager.persister = persister
		return nil
	}
}

type Session struct {
	CSRF      string
	Subject   string
	ExpiresAt time.Time
}

type sessionRecord struct{ Session }

type LoginResult struct {
	Token   string
	Session Session
}

func NewManager(path string, secureCookies bool, now func() time.Time, options ...ManagerOption) (*Manager, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxAuthBytes+1))
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(payload) > maxAuthBytes {
		return nil, errors.New("administrator credential file is too large")
	}
	var stored credentialFile
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&stored); err != nil {
		return nil, errors.New("invalid administrator credential file")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("administrator credential file has trailing data")
	}
	username := strings.TrimSpace(stored.Username)
	if username == "" {
		username = "admin"
	}
	salt, saltErr := hex.DecodeString(stored.Salt)
	passwordHash, hashErr := hex.DecodeString(stored.PasswordHash)
	mode := strings.TrimSpace(stored.AgentTokenMode)
	if mode == "" {
		mode = AgentCredentialTransition
	}
	if stored.Version != 1 || utf8.RuneCountInString(username) > 64 || saltErr != nil || len(salt) != 16 ||
		hashErr != nil || len(passwordHash) != 32 {
		return nil, errors.New("administrator credential file is incomplete")
	}
	if mode != AgentCredentialTransition && mode != AgentCredentialScoped {
		return nil, errors.New("administrator credential file has an invalid Agent token mode")
	}
	agentTokens := make(map[string]string, len(stored.AgentTokens))
	if len(stored.AgentTokens) > maxAgentCredentials {
		return nil, errors.New("administrator credential file has too many scoped Agent tokens")
	}
	for rawAgentID, token := range stored.AgentTokens {
		agentID := strings.TrimSpace(rawAgentID)
		token = strings.TrimSpace(token)
		if agentID != rawAgentID || !validAgentID(agentID) || token != "" && (len(token) < 32 || len(token) > 512) {
			return nil, errors.New("administrator credential file has an invalid scoped Agent token")
		}
		agentTokens[agentID] = token
	}
	if now == nil {
		now = time.Now
	}
	manager := &Manager{credentialPath: path, username: username, salt: salt, passwordHash: passwordHash,
		agentToken: strings.TrimSpace(stored.AgentToken), secureCookies: secureCookies,
		agentTokenMode: mode, agentTokens: agentTokens,
		now: now, sessions: make(map[[32]byte]sessionRecord),
		failures: make(map[string][]time.Time), persister: &fileCredentialPersister{path: path, uid: -1, gid: -1}}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("nil administrator authentication option")
		}
		if err := option(manager); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

// ChangePassword verifies the current password, atomically replaces the
// existing Python-compatible credential file, and revokes every old session.
// The agent token and username are preserved; callers must provide a fresh
// login after a successful change.
func (manager *Manager) ChangePassword(current, next string) error {
	if !validPassword(next) {
		return ErrInvalidCredentials
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !validPassword(current) || !manager.passwordMatchesLocked(current) {
		return ErrInvalidCredentials
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := scrypt.Key([]byte(next), salt, 1<<15, 8, 1, 32)
	if err != nil {
		return err
	}
	stored := manager.credentialLocked()
	stored.Salt, stored.PasswordHash = hex.EncodeToString(salt), hex.EncodeToString(hash)
	if err := manager.writeCredentialLocked(stored); err != nil {
		return err
	}
	manager.salt = salt
	manager.passwordHash = hash
	manager.sessions = make(map[[32]byte]sessionRecord)
	return nil
}

func validPassword(password string) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) > 0 && utf8.RuneCountInString(password) <= 256
}

func (manager *Manager) passwordMatchesLocked(password string) bool {
	if !validPassword(password) {
		return false
	}
	derived, err := scrypt.Key([]byte(password), manager.salt, 1<<15, 8, 1, 32)
	return err == nil && secureBytes(derived, manager.passwordHash)
}

func (manager *Manager) Username() string { return manager.username }

func (manager *Manager) SecureCookies() bool { return manager.secureCookies }

func (manager *Manager) AgentToken() string { return manager.agentToken }

type AgentCredentialStatus struct {
	Mode                  string   `json:"mode"`
	LegacyFallbackEnabled bool     `json:"legacy_fallback_enabled"`
	Active                []string `json:"active"`
	Revoked               []string `json:"revoked"`
}

func (manager *Manager) AgentCredentials() AgentCredentialStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := AgentCredentialStatus{Mode: manager.agentTokenMode,
		LegacyFallbackEnabled: manager.agentTokenMode == AgentCredentialTransition,
		Active:                []string{}, Revoked: []string{}}
	for agentID, token := range manager.agentTokens {
		if token == "" {
			result.Revoked = append(result.Revoked, agentID)
		} else {
			result.Active = append(result.Active, agentID)
		}
	}
	sort.Strings(result.Active)
	sort.Strings(result.Revoked)
	return result
}

func (manager *Manager) TokenForAgent(_ context.Context, agentID string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if !validAgentID(agentID) {
		return "", ErrAgentCredential
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if token, found := manager.agentTokens[agentID]; found {
		if token == "" {
			return "", ErrAgentCredential
		}
		return token, nil
	}
	if manager.agentTokenMode != AgentCredentialTransition || len(manager.agentToken) < 32 {
		return "", ErrAgentCredential
	}
	return manager.agentToken, nil
}

func (manager *Manager) IssueAgentToken(agentID string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if !validAgentID(agentID) {
		return "", ErrAgentCredential
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.agentTokens[agentID]; !exists && len(manager.agentTokens) >= maxAgentCredentials {
		return "", ErrAgentCredential
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	stored := manager.credentialLocked()
	stored.AgentTokens[agentID] = token
	if err := manager.writeCredentialLocked(stored); err != nil {
		return "", err
	}
	manager.agentTokens[agentID] = token
	return token, nil
}

func (manager *Manager) RevokeAgentToken(agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if !validAgentID(agentID) {
		return ErrAgentCredential
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	stored := manager.credentialLocked()
	stored.AgentTokens[agentID] = ""
	if err := manager.writeCredentialLocked(stored); err != nil {
		return err
	}
	manager.agentTokens[agentID] = ""
	return nil
}

func (manager *Manager) UnenrollAgentToken(agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if !validAgentID(agentID) {
		return ErrAgentCredential
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.agentTokenMode != AgentCredentialTransition {
		return ErrAgentCredential
	}
	if _, exists := manager.agentTokens[agentID]; !exists {
		return ErrAgentCredential
	}
	stored := manager.credentialLocked()
	delete(stored.AgentTokens, agentID)
	if err := manager.writeCredentialLocked(stored); err != nil {
		return err
	}
	delete(manager.agentTokens, agentID)
	return nil
}

func (manager *Manager) SetAgentCredentialMode(mode string) (bool, error) {
	mode = strings.TrimSpace(mode)
	if mode != AgentCredentialTransition && mode != AgentCredentialScoped {
		return false, ErrAgentCredential
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.agentTokenMode == mode {
		return false, nil
	}
	stored := manager.credentialLocked()
	stored.AgentTokenMode = mode
	if err := manager.writeCredentialLocked(stored); err != nil {
		return false, err
	}
	manager.agentTokenMode = mode
	return true, nil
}

func (manager *Manager) credentialLocked() credentialFile {
	tokens := make(map[string]string, len(manager.agentTokens))
	for agentID, token := range manager.agentTokens {
		tokens[agentID] = token
	}
	return credentialFile{Version: 1, Username: manager.username,
		Salt: hex.EncodeToString(manager.salt), PasswordHash: hex.EncodeToString(manager.passwordHash),
		AgentToken: manager.agentToken, AgentTokenMode: manager.agentTokenMode, AgentTokens: tokens}
}

func (manager *Manager) writeCredentialLocked(stored credentialFile) error {
	payload, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if len(payload)+1 > maxAuthBytes {
		return errors.New("administrator credential file would exceed its size limit")
	}
	return manager.persister.PersistCredential(append(payload, '\n'))
}

func validAgentID(value string) bool {
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

func (manager *Manager) Login(username, password, peer string) (LoginResult, error) {
	now := manager.now().UTC()
	manager.mu.Lock()
	retry := manager.retryAfterLocked(strings.TrimSpace(peer), now)
	salt := append([]byte(nil), manager.salt...)
	passwordHash := append([]byte(nil), manager.passwordHash...)
	manager.mu.Unlock()
	if retry > 0 {
		return LoginResult{}, &ThrottleError{RetryAfter: retry}
	}
	validPasswordLength := utf8.ValidString(password) && utf8.RuneCountInString(password) <= 256
	derived, err := scrypt.Key([]byte(password), salt, 1<<15, 8, 1, 32)
	if err != nil {
		return LoginResult{}, err
	}
	valid := validPasswordLength && secureEqual(username, manager.username) && secureBytes(derived, passwordHash)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	peer = strings.TrimSpace(peer)
	if !valid {
		manager.failures[peer] = append(manager.failures[peer], now)
		return LoginResult{}, ErrInvalidCredentials
	}
	delete(manager.failures, peer)
	manager.purgeExpiredLocked(now)
	if len(manager.sessions) >= maxSessions {
		return LoginResult{}, ErrSessionCapacity
	}
	token, err := randomToken(32)
	if err != nil {
		return LoginResult{}, err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return LoginResult{}, err
	}
	subjectBytes := sha256.Sum256([]byte("mdd-admin-session\x00" + token))
	session := Session{CSRF: csrf, Subject: base64.RawURLEncoding.EncodeToString(subjectBytes[:]), ExpiresAt: now.Add(SessionTTL)}
	manager.sessions[tokenHash(token)] = sessionRecord{Session: session}
	return LoginResult{Token: token, Session: session}, nil
}

func (manager *Manager) Session(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	now := manager.now().UTC()
	key := tokenHash(token)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, found := manager.sessions[key]
	if !found || !record.ExpiresAt.After(now) {
		delete(manager.sessions, key)
		return Session{}, false
	}
	record.ExpiresAt = now.Add(SessionTTL)
	manager.sessions[key] = record
	return record.Session, true
}

func (manager *Manager) Logout(token string) {
	manager.mu.Lock()
	delete(manager.sessions, tokenHash(token))
	manager.mu.Unlock()
}

// VerifyBrowserSession deliberately accepts only the HttpOnly cookie. A CLI
// header cannot be reproduced by the subsequent browser WebSocket handshake.
func (manager *Manager) VerifyBrowserSession(_ context.Context, request *http.Request) (string, error) {
	cookie, err := request.Cookie(SessionCookie)
	if err != nil {
		return "", ErrAuthentication
	}
	session, found := manager.Session(cookie.Value)
	if !found {
		return "", ErrAuthentication
	}
	return session.Subject, nil
}

// AuthorizeBrowserMutation is the cookie-only counterpart of Authorize. It is
// used when an HTTP mutation creates a capability that a browser WebSocket
// must subsequently consume, because WebSocket cannot reproduce a CLI header.
func (manager *Manager) AuthorizeBrowserMutation(request *http.Request) (string, error) {
	cookie, err := request.Cookie(SessionCookie)
	if err != nil {
		return "", ErrAuthentication
	}
	session, found := manager.Session(cookie.Value)
	if !found {
		return "", ErrAuthentication
	}
	if !secureEqual(request.Header.Get("X-MDD-CSRF-Token"), session.CSRF) {
		return "", ErrCSRF
	}
	return session.Subject, nil
}

// Authorize supports the existing CLI/header and browser/cookie contract.
// Browser media must use VerifyBrowserSession instead.
func (manager *Manager) Authorize(request *http.Request, mutation bool) (Session, error) {
	token := TokenFromRequest(request)
	session, found := manager.Session(token)
	if !found {
		return Session{}, ErrAuthentication
	}
	if mutation && !secureEqual(request.Header.Get("X-MDD-CSRF-Token"), session.CSRF) {
		return Session{}, ErrCSRF
	}
	return session, nil
}

func (manager *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutation := request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions
		if _, err := manager.Authorize(request, mutation); err != nil {
			status, code := http.StatusUnauthorized, "authentication_required"
			if errors.Is(err, ErrCSRF) {
				status, code = http.StatusForbidden, "invalid_csrf_token"
			}
			response.Header().Set("Content-Type", "application/json")
			response.Header().Set("Cache-Control", "no-store")
			response.WriteHeader(status)
			_ = json.NewEncoder(response).Encode(map[string]string{"code": code})
			return
		}
		next.ServeHTTP(response, request)
	})
}

func TokenFromRequest(request *http.Request) string {
	if token := strings.TrimSpace(request.Header.Get("X-MDD-Session")); token != "" {
		return token
	}
	authorization := request.Header.Get("Authorization")
	if strings.HasPrefix(authorization, "Bearer ") {
		if token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")); token != "" {
			return token
		}
	}
	if cookie, err := request.Cookie(SessionCookie); err == nil {
		return cookie.Value
	}
	return ""
}

func (manager *Manager) retryAfterLocked(peer string, now time.Time) int {
	cutoff := now.Add(-15 * time.Minute)
	attempts := manager.failures[peer][:0]
	for _, attempt := range manager.failures[peer] {
		if attempt.After(cutoff) {
			attempts = append(attempts, attempt)
		}
	}
	manager.failures[peer] = attempts
	if len(attempts) < 5 {
		return 0
	}
	remaining := 60 - int(now.Sub(attempts[len(attempts)-1]).Seconds())
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (manager *Manager) purgeExpiredLocked(now time.Time) {
	for key, record := range manager.sessions {
		if !record.ExpiresAt.After(now) {
			delete(manager.sessions, key)
		}
	}
}

func randomToken(bytes int) (string, error) {
	payload := make([]byte, bytes)
	if _, err := rand.Read(payload); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func tokenHash(token string) [32]byte { return sha256.Sum256([]byte(token)) }

func secureEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func secureBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare(left, right) == 1
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
