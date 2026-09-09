//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerdeploy"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type hostModemProfile = provideradmin.HostModemProfile

func hostModemControl(payload []byte) (*agentcontrol.Client, error) {
	var document struct {
		Control struct {
			Listen string `json:"listen"`
			Token  string `json:"token"`
		} `json:"control"`
	}
	if json.Unmarshal(payload, &document) != nil {
		return nil, errors.New("invalid host Agent control configuration")
	}
	return agentcontrol.NewClient("http://"+document.Control.Listen, document.Control.Token, &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}})
}

func verifyHostAgentReady(ctx context.Context, payload []byte) error {
	client, err := hostModemControl(payload)
	if err != nil {
		return err
	}
	status, err := client.Status(ctx)
	if err != nil {
		return err
	}
	if status.State != agentcontrol.StateRunning || status.Generation == 0 {
		return errors.New("host Agent runtime is not ready")
	}
	want, err := hostModemRuntimeConfig(payload)
	if err != nil {
		return err
	}
	if status.RuntimeConfig == nil || *status.RuntimeConfig != want {
		return errors.New("host Agent has not loaded the requested modem configuration")
	}
	return nil
}

func hostModemRuntimeConfig(payload []byte) (agentcontrol.RuntimeConfig, error) {
	var document struct {
		Agent struct {
			Backend  string          `json:"modem_backend"`
			Profiles json.RawMessage `json:"modem_profiles"`
			Enabled  bool            `json:"modem_enabled"`
			APDU     bool            `json:"modem_sim_apdu_enabled"`
		} `json:"agent"`
	}
	if json.Unmarshal(payload, &document) != nil {
		return agentcontrol.RuntimeConfig{}, errors.New("invalid host Agent configuration")
	}
	return agentcontrol.ModemRuntimeConfig(document.Agent.Backend, document.Agent.Profiles, document.Agent.Enabled, document.Agent.APDU)
}

func verifyHostAgentProcess(procRoot string, state providerdeploy.HostServiceState, configPath string) error {
	if state.ActiveState != "active" || state.MainPID == 0 {
		return errors.New("host Agent is not running")
	}
	payload, err := os.ReadFile(filepath.Join(procRoot, strconv.FormatUint(state.MainPID, 10), "cmdline"))
	if err != nil || len(payload) > 1<<20 {
		return errors.New("host Agent process identity unavailable")
	}
	return validateHostAgentArguments(bytes.Split(payload, []byte{0}), configPath)
}

func validateHostAgentArguments(arguments [][]byte, configPath string) error {
	if len(arguments) < 2 || filepath.Base(string(arguments[0])) != "mdd-agent" || !filepath.IsAbs(configPath) {
		return errors.New("host Agent process does not match binding")
	}
	matched := false
	for index := 1; index < len(arguments); index++ {
		argument := string(arguments[index])
		path := ""
		if argument == "-config" || argument == "--config" {
			index++
			if index >= len(arguments) {
				return errors.New("host Agent config argument missing")
			}
			path = string(arguments[index])
		} else if strings.HasPrefix(argument, "-config=") {
			path = strings.TrimPrefix(argument, "-config=")
		} else if strings.HasPrefix(argument, "--config=") {
			path = strings.TrimPrefix(argument, "--config=")
		} else {
			continue
		}
		if matched || !filepath.IsAbs(path) || filepath.Clean(path) != filepath.Clean(configPath) {
			return errors.New("host Agent process uses another configuration")
		}
		matched = true
	}
	if !matched {
		return errors.New("host Agent process has no explicit config binding")
	}
	return nil
}

func validateHostModemSettings(settings hostModemSettings) error {
	if settings.Backend != "auto" && settings.Backend != "serial" {
		return errors.New("invalid host modem backend")
	}
	if len(settings.Profiles) > 128 {
		return errors.New("too many host modem profiles")
	}
	seen := make(map[string]bool)
	for _, profile := range settings.Profiles {
		if len(profile.VID) != 4 || len(profile.PID) != 4 || len(profile.Name) > 128 {
			return errors.New("invalid host modem profile")
		}
		if _, err := strconv.ParseUint(profile.VID, 16, 16); err != nil {
			return errors.New("invalid host modem vendor ID")
		}
		if _, err := strconv.ParseUint(profile.PID, 16, 16); err != nil {
			return errors.New("invalid host modem product ID")
		}
		key := strings.ToLower(profile.VID + ":" + profile.PID)
		if seen[key] {
			return errors.New("duplicate host modem profile")
		}
		seen[key] = true
	}
	return nil
}

// The existing helper mutation lock must cover this save and its later switch.
// A saved mode is not evidence that services have adopted it.
func saveHostModemConfig(path, agentID, coreURL, expectedRevision string, settings hostModemSettings) (hostModemSnapshot, error) {
	if err := validateHostModemSettings(settings); err != nil {
		return hostModemSnapshot{}, err
	}
	previous, info, current, err := readHostModemConfig(path, agentID, coreURL)
	if err != nil {
		return current, err
	}
	if current.Revision != expectedRevision {
		return current, errors.New("host Agent configuration changed")
	}
	oldSettings, _ := json.Marshal(current.Settings)
	newSettings, _ := json.Marshal(settings)
	if bytes.Equal(oldSettings, newSettings) {
		return current, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(previous, &document); err != nil {
		return current, err
	}
	var agent map[string]json.RawMessage
	if err := json.Unmarshal(document["agent"], &agent); err != nil {
		return current, err
	}
	agent["modem_backend"], _ = json.Marshal(settings.Backend)
	agent["modem_profiles"], _ = json.Marshal(settings.Profiles)
	document["agent"], _ = json.Marshal(agent)
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return current, err
	}
	payload = append(payload, '\n')
	if err := writeWebConfig(path+".before-modem-change", previous, info); err != nil {
		return current, err
	}
	latest, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(latest, previous) {
		return current, errors.New("host Agent configuration changed")
	}
	if err := writeWebConfig(path, payload, info); err != nil {
		return current, err
	}
	_, _, saved, err := readHostModemConfig(path, agentID, coreURL)
	return saved, err
}

type hostModemSettings = provideradmin.HostModemSettings
type hostModemSnapshot = provideradmin.HostModemSnapshot

func (service *providerApplyService) hostModemBinding() (*provideradmin.HostModemBinding, error) {
	binding := service.settings.HostModem
	if binding == nil || !filepath.IsAbs(binding.ConfigPath) {
		return nil, &provideradmin.Error{Status: 409, Code: "production_host_agent_not_configured"}
	}
	relative, err := filepath.Rel("/var/lib/mdd-agent", filepath.Clean(binding.ConfigPath))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, &provideradmin.Error{Status: 409, Code: "host_agent_config_outside_managed_state"}
	}
	u, err := url.Parse(binding.CoreURL)
	_, port, listenErr := net.SplitHostPort(service.settings.Public.Listen)
	if err != nil || listenErr != nil || u.Port() != port {
		return nil, &provideradmin.Error{Status: 409, Code: "host_agent_core_mismatch"}
	}
	return binding, nil
}
func (service *providerApplyService) HostModemSettings(ctx context.Context) (provideradmin.HostModemSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return hostModemSnapshot{}, err
	}
	binding, err := service.hostModemBinding()
	if err != nil {
		return hostModemSnapshot{}, err
	}
	payload, _, snapshot, err := readHostModemConfig(binding.ConfigPath, binding.AgentID, binding.CoreURL)
	snapshot.RuntimeState = "not_observed"
	if err != nil {
		return snapshot, err
	}
	snapshot.AgentState = "unavailable"
	loaded := false
	if client, controlErr := hostModemControl(payload); controlErr == nil {
		if status, statusErr := client.Status(ctx); statusErr == nil {
			snapshot.AgentState = string(status.State)
			snapshot.AgentGeneration = status.Generation
			wanted, identityErr := hostModemRuntimeConfig(payload)
			loaded = identityErr == nil && status.State == agentcontrol.StateRunning && status.RuntimeConfig != nil && *status.RuntimeConfig == wanted
		}
	}
	receipt, recordErr := readHostModeReceipt(binding.ConfigPath + ".host-mode-status.json")
	if recordErr != nil {
		return snapshot, recordErr
	}
	if receipt.Held {
		snapshot.RuntimeState = "recovery_required"
		if service.hostSwitching.Load() {
			snapshot.RuntimeState = "switching"
		}
	} else if receipt.SchemaVersion == 1 && receipt.Revision == snapshot.Revision {
		snapshot.RuntimeState = receipt.State
		if receipt.State == "applied" && !loaded {
			snapshot.RuntimeState = "configuration_not_loaded"
		}
		if receipt.State == "applied" && loaded {
			manager := providerdeploy.Systemctl{Path: service.settings.ProviderApply.SystemctlPath}
			if err := manager.Validate(); err != nil {
				snapshot.RuntimeState = "services_unconfirmed"
			} else if mm, err := manager.HostModemServiceState(ctx, "ModemManager.service"); err != nil {
				snapshot.RuntimeState = "services_unconfirmed"
			} else if snapshot.Settings.Backend == "serial" && !(mm.LoadState == "not-found" || mm.ActiveState == "inactive" && (mm.UnitFileState == "disabled" || mm.UnitFileState == "masked")) || snapshot.Settings.Backend == "auto" && (mm.ActiveState != "active" || mm.UnitFileState != "enabled") {
				snapshot.RuntimeState = "services_mismatch"
			}
		}
	}
	return snapshot, err
}
func (service *providerApplyService) SaveHostModemSettings(ctx context.Context, input provideradmin.HostModemRequest) (provideradmin.HostModemSnapshot, error) {
	service.mutation.Lock()
	defer service.mutation.Unlock()
	if err := ctx.Err(); err != nil {
		return hostModemSnapshot{}, err
	}
	binding, err := service.hostModemBinding()
	if err != nil {
		return hostModemSnapshot{}, err
	}
	return service.applyHostModemSettings(ctx, binding, input)
}

// A host can run production and validation Agents. A conventional filename is
// not ownership evidence; callers must supply the configured production target.
func readHostModemConfig(path, agentID, coreURL string) ([]byte, os.FileInfo, hostModemSnapshot, error) {
	var result hostModemSnapshot
	endpoint, err := url.Parse(coreURL)
	if err != nil || agentID == "" || endpoint.Scheme != "wss" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "/v1/agent/ws" {
		return nil, nil, result, errors.New("host Agent ownership configuration required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, nil, result, errors.New("host Agent configuration unavailable")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, result, err
	}
	var document struct {
		Agent struct {
			ID       string             `json:"id"`
			URL      string             `json:"server_url"`
			Backend  string             `json:"modem_backend"`
			Profiles []hostModemProfile `json:"modem_profiles"`
		} `json:"agent"`
	}
	if json.Unmarshal(payload, &document) != nil {
		return nil, nil, result, errors.New("invalid host Agent configuration")
	}
	if document.Agent.ID != agentID || document.Agent.URL != coreURL {
		return nil, nil, result, errors.New("host Agent belongs to another Core or instance")
	}
	backend := document.Agent.Backend
	if backend == "" {
		backend = "auto"
	}
	if backend != "auto" && backend != "serial" {
		return nil, nil, result, errors.New("invalid host modem backend")
	}
	digest := sha256.Sum256(payload)
	result = hostModemSnapshot{Revision: hex.EncodeToString(digest[:]), Settings: hostModemSettings{Backend: backend, Profiles: document.Agent.Profiles}}
	return payload, info, result, nil
}
