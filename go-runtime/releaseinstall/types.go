// Package releaseinstall installs and removes verified release directories
// without itself starting, stopping, enabling, or restarting any service.
package releaseinstall

import (
	"context"
	"errors"
	"path/filepath"
)

type Layout struct {
	ReleasesDirectory string
	CurrentLink       string
	LibexecDirectory  string
	UnitDirectory     string
	ConfigDirectory   string
	StateDirectory    string
	ProviderState     string
	SystemState       string
	ReceiptDirectory  string
}

func DefaultLayout() Layout {
	return Layout{
		ReleasesDirectory: "/usr/lib/mdd/releases",
		CurrentLink:       "/usr/lib/mdd/current",
		LibexecDirectory:  "/usr/libexec/mdd",
		UnitDirectory:     "/etc/systemd/system",
		ConfigDirectory:   "/etc/mdd",
		StateDirectory:    "/var/lib/mdd",
		ProviderState:     "/var/lib/mdd/providers",
		SystemState:       "/var/lib/mdd-system",
		ReceiptDirectory:  "/var/lib/mdd-system/release-install",
	}
}

func (layout Layout) Validate() error {
	values := []string{
		layout.ReleasesDirectory, layout.CurrentLink, layout.LibexecDirectory, layout.UnitDirectory,
		layout.ConfigDirectory, layout.StateDirectory, layout.ProviderState, layout.SystemState, layout.ReceiptDirectory,
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) {
			return errors.New("release installation paths must be clean, absolute, and scoped")
		}
		if _, found := seen[value]; found {
			return errors.New("release installation paths must be distinct")
		}
		seen[value] = struct{}{}
	}
	if filepath.Dir(layout.CurrentLink) != filepath.Dir(layout.ReleasesDirectory) ||
		filepath.Dir(layout.ProviderState) != layout.StateDirectory ||
		filepath.Dir(layout.ReceiptDirectory) != layout.SystemState {
		return errors.New("release installation path hierarchy is invalid")
	}
	return nil
}

type Reloader interface {
	DaemonReload() error
}

// RemovalGate rechecks the external service lifecycle immediately before and
// after installed unit links are detached. Implementations must fail unless
// every MDD process that can execute files from the release is inactive and no
// unit is enabled for automatic restart.
type RemovalGate interface {
	VerifyInactiveDisabled(context.Context) error
}

// RemovePlan is a non-secret description of the exact managed installation
// that passed removal preflight.
type RemovePlan struct {
	SchemaVersion  int      `json:"schema_version"`
	CurrentRelease string   `json:"current_release"`
	ReleaseIDs     []string `json:"release_ids"`
	StableLinks    []string `json:"stable_links"`
}

type ReceiptState string

const (
	StateApplying       ReceiptState = "applying"
	StateApplied        ReceiptState = "applied"
	StateRolledBack     ReceiptState = "rolled_back"
	StateManualRecovery ReceiptState = "manual_recovery_required"
)

type Receipt struct {
	SchemaVersion   int          `json:"schema_version"`
	ReceiptID       string       `json:"receipt_id"`
	State           ReceiptState `json:"state"`
	Code            string       `json:"code,omitempty"`
	ReleaseID       string       `json:"release_id"`
	PreviousTarget  string       `json:"previous_target,omitempty"`
	CandidateTarget string       `json:"candidate_target"`
}

var ErrIncompleteInstall = errors.New("an incomplete release installation requires explicit recovery")
