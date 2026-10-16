package adminauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

func enrollmentHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func cloneEnrollments(source map[string]agentEnrollment) map[string]agentEnrollment {
	result := make(map[string]agentEnrollment, len(source))
	for id, entry := range source {
		result[id] = entry
	}
	return result
}

func validateEnrollments(entries map[string]agentEnrollment) error {
	if len(entries) > maxAgentCredentials {
		return errors.New("too many Agent enrollment records")
	}
	requests := map[string]bool{}
	for id, entry := range entries {
		request, e1 := base64.RawURLEncoding.DecodeString(entry.Request)
		token, e2 := base64.RawURLEncoding.DecodeString(entry.TokenHash)
		if !validAgentID(id) || e1 != nil || e2 != nil || len(request) != 32 || len(token) != 32 || requests[entry.Request] {
			return errors.New("invalid Agent enrollment record")
		}
		requests[entry.Request] = true
	}
	return nil
}

// CreateAgentToken never rotates an identity. The enrollment record survives
// rotate/revoke/unenroll so a lost response cannot later resurrect access.
func (manager *Manager) CreateAgentToken(id, request string, admit func(string) (func(), bool)) (string, error) {
	if id != strings.TrimSpace(id) || !validAgentID(id) || len(request) < 32 || len(request) > 128 || !validAgentID(request) {
		return "", ErrAgentCredential
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.pendingMutation != nil {
		return "", ErrCredentialPending
	}
	requestHash := enrollmentHash(request)
	if pending := manager.pendingEnrollment; pending != nil {
		if pending.AgentEnrollments[id].Request != requestHash {
			return "", ErrCredentialPending
		}
		if err := manager.persistCredentialLocked(*pending); err != nil {
			return "", ErrCredentialPending
		}
		manager.agentTokens = pending.AgentTokens
		manager.agentEnrollments = pending.AgentEnrollments
		manager.pendingEnrollment = nil
	}
	if prior, found := manager.agentEnrollments[id]; found {
		token := manager.agentTokens[id]
		if prior.Request != requestHash || token == "" || prior.TokenHash != enrollmentHash(token) {
			return "", ErrEnrollmentConflict
		}
		return token, nil
	}
	for _, prior := range manager.agentEnrollments {
		if prior.Request == requestHash {
			return "", ErrEnrollmentConflict
		}
	}
	if _, found := manager.agentTokens[id]; found {
		return "", ErrEnrollmentConflict
	}
	if admit != nil {
		release, allowed := admit(id)
		if !allowed {
			return "", ErrEnrollmentConflict
		}
		defer release()
	}
	if len(manager.agentEnrollments) >= maxAgentCredentials || len(manager.agentTokens) >= maxAgentCredentials {
		return "", ErrAgentCredential
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	stored := manager.credentialLocked()
	stored.AgentTokens[id] = token
	stored.AgentEnrollments[id] = agentEnrollment{Request: requestHash, TokenHash: enrollmentHash(token)}
	payload, err := json.Marshal(stored)
	if err != nil {
		return "", err
	}
	if err := validateCredentialDocument(append(payload, '\n')); err != nil {
		return "", err
	}
	if err := manager.persistCredentialLocked(stored); err != nil {
		manager.pendingEnrollment = &stored
		return "", ErrCredentialPending
	}
	manager.agentTokens = stored.AgentTokens
	manager.agentEnrollments = stored.AgentEnrollments
	return token, nil
}
