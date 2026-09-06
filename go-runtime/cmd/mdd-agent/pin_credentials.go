package main

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpin"
)

type agentPINCredentials struct {
	mu       sync.Mutex
	path     string
	recovery *agentpin.Manager
}

func newAgentPINCredentials(path string, recovery *agentpin.Manager) (*agentPINCredentials, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SIM PIN configuration requires a loaded Agent config")
	}
	return &agentPINCredentials{path: path, recovery: recovery}, nil
}

func (store *agentPINCredentials) PINForCard(ctx context.Context, cardID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	settings, err := readConfigForEdit(store.path, false)
	if err != nil {
		return "", err
	}
	return settings.Agent.PINs[cardID], nil
}

func (store *agentPINCredentials) Configuration(ctx context.Context, cardID string) (agentlink.SIMPINConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	settings, err := readConfigForEdit(store.path, false)
	if err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	return pinConfiguration(settings, cardID)
}

func (store *agentPINCredentials) Save(ctx context.Context, cardID, pin, expectedRevision string) (agentlink.SIMPINConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	if !digits(cardID, 1, 64) || !digits(pin, 4, 8) {
		return agentlink.SIMPINConfiguration{}, errors.New("invalid SIM PIN configuration")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	settings, err := readConfigForEdit(store.path, false)
	if err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	current, err := pinConfiguration(settings, cardID)
	if err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	if current.Revision != expectedRevision {
		return agentlink.SIMPINConfiguration{}, agentlink.ErrSIMPINConfigurationChanged
	}
	revision, err := randomHex(16)
	if err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	if settings.Agent.PINs == nil {
		settings.Agent.PINs = map[string]string{}
	}
	if settings.Agent.PINRevisions == nil {
		settings.Agent.PINRevisions = map[string]string{}
	}
	settings.Agent.PINs[cardID], settings.Agent.PINRevisions[cardID] = pin, revision
	if err := settings.validate(); err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	if err := saveConfig(store.path, settings); err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	if store.recovery != nil {
		store.recovery.SetPIN(cardID, pin, revision)
	}
	return agentlink.SIMPINConfiguration{Configured: true, Revision: revision}, nil
}

func (store *agentPINCredentials) Remove(ctx context.Context, cardID, expectedRevision string) (agentlink.SIMPINConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	if !digits(cardID, 1, 64) || expectedRevision == "" {
		return agentlink.SIMPINConfiguration{}, errors.New("invalid SIM PIN removal")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	settings, err := readConfigForEdit(store.path, false)
	if err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	current, err := pinConfiguration(settings, cardID)
	if err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	if !current.Configured || current.Revision != expectedRevision {
		return agentlink.SIMPINConfiguration{}, agentlink.ErrSIMPINConfigurationChanged
	}
	delete(settings.Agent.PINs, cardID)
	delete(settings.Agent.PINRevisions, cardID)
	if err := settings.validate(); err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	if err := saveConfig(store.path, settings); err != nil {
		return agentlink.SIMPINConfiguration{}, err
	}
	if store.recovery != nil {
		store.recovery.RemovePIN(cardID)
	}
	return agentlink.SIMPINConfiguration{}, nil
}

func pinConfiguration(settings config, cardID string) (agentlink.SIMPINConfiguration, error) {
	pin, configured := settings.Agent.PINs[cardID]
	revision := settings.Agent.PINRevisions[cardID]
	if configured && pin != "" && revision == "" {
		revision = "legacy-config"
	}
	result := agentlink.SIMPINConfiguration{Configured: configured && pin != "", Revision: revision}
	if result.Validate() != nil {
		return agentlink.SIMPINConfiguration{}, errors.New("stored SIM PIN configuration is inconsistent")
	}
	return result, nil
}
