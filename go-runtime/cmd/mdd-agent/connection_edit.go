package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/pintls"
)

// An editor owns the original file, not a new enrollment or running Agent.
type connectionEdit struct {
	path     string
	digest   string
	file     os.FileInfo
	settings config
}

type connectionChange struct {
	Server, TLSMode, Fingerprint, Token string
}

type connectionEditFailure struct {
	code  string
	cause error
}

func (failure *connectionEditFailure) Error() string { return failure.cause.Error() }
func (failure *connectionEditFailure) Unwrap() error { return failure.cause }

func readConnectionEdit(path string) (connectionEdit, error) {
	if !filepath.IsAbs(path) {
		return connectionEdit{}, errors.New("configuration path must be absolute")
	}
	if _, err := os.Lstat(path + ".setup-pending"); !errors.Is(err, os.ErrNotExist) {
		return connectionEdit{}, errors.New("finish the existing setup before editing this configuration")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return connectionEdit{}, err
	}
	payload, err := readSetupFile(path)
	if err != nil {
		return connectionEdit{}, err
	}
	var settings config
	if err = strictSetupJSON(payload, &settings); err != nil {
		return connectionEdit{}, err
	}
	checked := settings
	if err = checked.validate(); err != nil {
		return connectionEdit{}, err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return connectionEdit{}, errors.New("configuration changed while opening the editor")
	}
	return connectionEdit{path: path, digest: configDigest(payload), file: current, settings: settings}, nil
}

func (edit connectionEdit) checkOriginal() error {
	current, err := readConnectionEdit(edit.path)
	if err != nil {
		return err
	}
	if current.digest != edit.digest || !os.SameFile(edit.file, current.file) {
		return errors.New("configuration changed outside this editor; reopen it before saving")
	}
	return nil
}

func (edit connectionEdit) save(ctx context.Context, change connectionChange) error {
	return edit.saveGuarded(ctx, change, nil)
}

func (edit connectionEdit) saveGuarded(ctx context.Context, change connectionChange, authorize func(config) error) error {
	lock, err := lockConfigMutation(edit.path)
	if err != nil {
		return &connectionEditFailure{"management_config_locked", err}
	}
	defer lock.Close()
	if err = edit.checkOriginal(); err != nil {
		return &connectionEditFailure{"management_edit_stale_or_invalid", err}
	}
	settings := edit.settings
	settings.Agent.ServerURL, err = normalizeSetupServer(change.Server)
	if err != nil {
		return &connectionEditFailure{"management_input_invalid", err}
	}
	switch change.TLSMode {
	case "system":
		settings.Agent.TLSMode, settings.Agent.TLSFingerprint = "system", ""
	case "pin":
		pin := strings.TrimSpace(change.Fingerprint)
		if pin == "" {
			pin = settings.Agent.TLSFingerprint
		}
		settings.Agent.TLSFingerprint, err = normalizeSHA256(pin)
		if err != nil {
			return &connectionEditFailure{"management_input_invalid", err}
		}
		settings.Agent.TLSMode = "pin"
	default:
		return &connectionEditFailure{"management_input_invalid", errors.New("select system certificate verification or an explicit trusted certificate pin")}
	}
	if strings.TrimSpace(change.Token) != "" {
		settings.Agent.ServerToken = strings.TrimSpace(change.Token)
	}
	checked := settings
	if err = checked.validate(); err != nil {
		return &connectionEditFailure{"management_input_invalid", err}
	}
	if authorize != nil {
		if err = authorize(settings); err != nil {
			return err
		}
	}
	client, err := pintls.NewPolicyHTTPClient(settings.Agent.ServerURL, settings.Agent.TLSMode, settings.Agent.TLSFingerprint, 10*time.Second)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	client.Timeout = 20 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("configuration verification redirect refused")
	}
	endpoint, _ := url.Parse(settings.Agent.ServerURL)
	peer := &setupHTTP{client: client, origin: "https://" + endpoint.Host}
	// Establish the selected trust before transmitting the unchanged identity's
	// credential. Check never enrolls, replaces a WSS owner, or starts hardware.
	if err = peer.request(ctx, http.MethodGet, "/api/auth/status", nil, nil); err != nil {
		return &connectionEditFailure{"management_target_verification_failed", err}
	}
	var response struct {
		OK bool `json:"ok"`
	}
	if err = peer.request(ctx, http.MethodPost, "/api/auth/agent-credentials/check", map[string]string{
		"agent_id": settings.Agent.ID, "agent_token": settings.Agent.ServerToken,
	}, &response); err != nil {
		return &connectionEditFailure{"management_credential_check_failed", err}
	}
	if !response.OK {
		return &connectionEditFailure{"management_credential_check_failed", errors.New("Core did not confirm the Agent credential")}
	}
	if err = edit.checkOriginal(); err != nil {
		return &connectionEditFailure{"management_edit_stale_or_invalid", err}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if authorize != nil {
		if err = authorize(settings); err != nil {
			return err
		}
	}
	payload, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := writeSetupFile(edit.path, append(payload, '\n'), false, edit.path); err != nil {
		return &connectionEditFailure{"management_publish_unconfirmed", err}
	}
	return nil
}

// A delegated editor cannot export the service's secret by changing its recipient.
// A replacement token is explicit input; a retained token stays at its old origin
// and trust binding. This is stricter than the full-file administrator editor.
func checkRetainedConnectionCredential(previous, next config, replacement string) error {
	if strings.TrimSpace(replacement) != "" {
		return nil
	}
	before, err := normalizeSetupServer(previous.Agent.ServerURL)
	if err != nil {
		return errors.New("new target credential required")
	}
	a, _ := url.Parse(before)
	b, _ := url.Parse(next.Agent.ServerURL)
	oldMode, oldPin := previous.Agent.TLSMode, previous.Agent.TLSFingerprint
	if oldPin != "" {
		oldMode = "pin"
		oldPin, err = normalizeSHA256(oldPin)
	} else if oldMode == "" {
		oldMode = "system"
	}
	if err != nil || b == nil || !strings.EqualFold(a.Host, b.Host) || oldMode != next.Agent.TLSMode || oldPin != next.Agent.TLSFingerprint {
		return errors.New("new target credential required")
	}
	return nil
}
