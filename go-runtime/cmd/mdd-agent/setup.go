package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/pintls"
)

type agentConnectionPackage struct {
	Kind           string `json:"kind"`
	Version        int    `json:"version"`
	AgentID        string `json:"agent_id"`
	ServerURL      string `json:"server_url"`
	ServerToken    string `json:"server_token"`
	TLSMode        string `json:"tls_mode"`
	TLSFingerprint string `json:"tls_sha256,omitempty"`
}

type setupInput struct {
	Server, Fingerprint, Username, Password, Package, AgentID, Token string
}

func normalizeSetupServer(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		return normalizeAgentServer(value)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Hostname() == "" {
		return "", errors.New("enter an HTTPS origin or exact WSS endpoint without credentials, query or fragment")
	}
	if parsed.Scheme == "https" {
		if parsed.Path != "" && parsed.Path != "/" {
			return "", errors.New("enter the server origin, not a web page path")
		}
		parsed.Scheme, parsed.Path = "wss", "/v1/agent/ws"
	}
	if parsed.Port() == "" {
		parsed.Host = net.JoinHostPort(parsed.Hostname(), "443")
	}
	if parsed.Path != "/v1/agent/ws" {
		return "", errors.New("setup requires the /v1/agent/ws endpoint")
	}
	return normalizeAgentServer(parsed.String())
}

func (tx *setupTransaction) configure(input setupInput) error {
	settings := tx.pending.Settings
	if strings.TrimSpace(input.Package) != "" {
		var bundle agentConnectionPackage
		if err := strictSetupJSON([]byte(input.Package), &bundle); err != nil {
			return fmt.Errorf("invalid connection package: %w", err)
		}
		if bundle.Kind != "mdd-agent-connection" || bundle.Version != 1 {
			return errors.New("unsupported Agent connection package")
		}
		if len(bundle.ServerToken) < 32 || (bundle.TLSMode != "pin" && bundle.TLSMode != "system") {
			return errors.New("connection package requires an Agent token and explicit TLS mode")
		}
		if tx.pending.OriginalSHA256 != "" && bundle.AgentID != settings.Agent.ID {
			return errors.New("package Agent ID differs from the existing configuration")
		}
		settings.Agent.ID, settings.Agent.ServerToken = bundle.AgentID, bundle.ServerToken
		settings.Agent.ServerURL = bundle.ServerURL
		settings.Agent.TLSMode, settings.Agent.TLSFingerprint = bundle.TLSMode, bundle.TLSFingerprint
	} else {
		if input.Server != "" {
			settings.Agent.ServerURL = input.Server
		}
		if input.Fingerprint != "" {
			pin, err := normalizeSHA256(input.Fingerprint)
			if err != nil {
				return err
			}
			settings.Agent.TLSMode, settings.Agent.TLSFingerprint = "pin", pin
		} else if settings.Agent.TLSMode == "" && settings.Agent.TLSFingerprint == "" {
			settings.Agent.TLSMode = "system"
		}
		if input.Token != "" {
			if tx.pending.OriginalSHA256 != "" && input.AgentID != settings.Agent.ID {
				return errors.New("Agent ID differs from the existing configuration")
			}
			settings.Agent.ID, settings.Agent.ServerToken = strings.TrimSpace(input.AgentID), strings.TrimSpace(input.Token)
		}
	}
	server, err := normalizeSetupServer(settings.Agent.ServerURL)
	if err != nil {
		return err
	}
	settings.Agent.ServerURL = server
	validation := settings
	if validation.Agent.ServerToken == "" {
		validation.Agent.ServerToken = strings.Repeat("validation-only-", 3)
	}
	if err := validation.validate(); err != nil {
		return err
	}
	if tx.pending.Requested {
		old := tx.pending.Settings.Agent
		if old.ID != settings.Agent.ID || old.ServerURL != settings.Agent.ServerURL || old.TLSMode != settings.Agent.TLSMode || old.TLSFingerprint != settings.Agent.TLSFingerprint || old.ServerToken != settings.Agent.ServerToken {
			return errors.New("unfinished enrollment is bound to its original identity, endpoint and trust; retry without changing them")
		}
	}
	tx.pending.Settings = settings
	return nil
}

type setupHTTP struct {
	client                *http.Client
	origin, session, csrf string
}

func (client *setupHTTP) request(ctx context.Context, method, path string, input, output any) error {
	var payload []byte
	var err error
	if input != nil {
		payload, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, client.origin+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if client.session != "" {
		req.Header.Set("X-MDD-Session", client.session)
		req.Header.Set("X-MDD-CSRF-Token", client.csrf)
	}
	res, err := client.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maximumAgentConfigBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maximumAgentConfigBytes {
		return errors.New("Core setup response is too large")
	}
	if res.StatusCode != http.StatusOK {
		if path == "/api/auth/login" && res.StatusCode == http.StatusUnauthorized {
			return errors.New("Web username or password was rejected")
		}
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body, &failure)
		// Only stable codes, never reflect server text that might contain secrets.
		switch failure.Code {
		case "agent_enrollment_conflict", "agent_enrollment_unconfirmed", "agent_credential_rejected", "invalid_csrf_token":
			return fmt.Errorf("Core %s (HTTP %d)", failure.Code, res.StatusCode)
		default:
			return fmt.Errorf("Core setup request failed (HTTP %d)", res.StatusCode)
		}
	}
	if output != nil {
		return json.Unmarshal(body, output)
	}
	return nil
}

// enroll uses a short-lived administrator client. The saved config is the only
// input to the separately constructed runtime client; no session/cookie escapes.
func (tx *setupTransaction) enroll(ctx context.Context, input setupInput) (settings config, warning string, err error) {
	if err = tx.configure(input); err != nil {
		return
	}
	if err = tx.checkOriginal(); err != nil {
		return
	}
	client, err := pintls.NewPolicyHTTPClient(tx.pending.Settings.Agent.ServerURL, tx.pending.Settings.Agent.TLSMode, tx.pending.Settings.Agent.TLSFingerprint, 10*time.Second)
	if err != nil {
		return
	}
	client.Timeout = 20 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("setup redirect refused") }
	defer client.CloseIdleConnections()
	endpoint, _ := url.Parse(tx.pending.Settings.Agent.ServerURL)
	admin := &setupHTTP{client: client, origin: "https://" + endpoint.Host}
	// Complete authenticated TLS before sending any password or Agent token.
	if err = admin.request(ctx, http.MethodGet, "/api/auth/status", nil, nil); err != nil {
		return
	}
	if tx.pending.Settings.Agent.ServerToken == "" {
		var login struct {
			Token string `json:"token"`
			CSRF  string `json:"csrf"`
		}
		err = admin.request(ctx, http.MethodPost, "/api/auth/login", map[string]string{"username": input.Username, "password": input.Password}, &login)
		input.Password = ""
		if err != nil {
			return
		}
		admin.session, admin.csrf = login.Token, login.CSRF
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if logoutErr := admin.request(cleanup, http.MethodPost, "/api/auth/logout", nil, nil); logoutErr != nil {
				warning = "temporary administrator logout was not confirmed; the server session expires normally"
			}
			admin.session, admin.csrf = "", ""
		}()
		if login.Token == "" || login.CSRF == "" {
			err = errors.New("Core did not return the administrator session contract")
			return
		}
		tx.pending.Requested = true
		if err = tx.savePending(); err != nil {
			return
		}
		var issued struct {
			ID    string `json:"agent_id"`
			Token string `json:"agent_token"`
		}
		err = admin.request(ctx, http.MethodPost, "/api/auth/agent-credentials", map[string]string{
			"action": "create", "agent_id": tx.pending.Settings.Agent.ID, "enrollment_id": tx.pending.EnrollmentID,
		}, &issued)
		if err != nil {
			return
		}
		if issued.ID != tx.pending.Settings.Agent.ID || len(issued.Token) < 32 {
			err = errors.New("Core enrollment response identity is invalid")
			return
		}
		tx.pending.Settings.Agent.ServerToken = issued.Token
	}
	tx.pending.Requested = true
	if err = tx.savePending(); err != nil {
		return
	}
	// This endpoint verifies credentials without registering a hardware owner or
	// replacing an already connected Agent with an imported identity.
	var checked struct {
		OK bool `json:"ok"`
	}
	err = admin.request(ctx, http.MethodPost, "/api/auth/agent-credentials/check", map[string]string{
		"agent_id": tx.pending.Settings.Agent.ID, "agent_token": tx.pending.Settings.Agent.ServerToken,
	}, &checked)
	if err != nil {
		return
	}
	if !checked.OK {
		err = errors.New("Core did not confirm the Agent credential")
		return
	}
	settings, err = tx.publish()
	return
}
