package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/moby/sys/atomicwriter"
)

const defaultAgentControlAddress = "127.0.0.1:35964"

type configView struct {
	Path     string `json:"path"`
	Ready    bool   `json:"ready"`
	Problem  string `json:"problem,omitempty"`
	Settings config `json:"settings"`
}

func defaultConfigPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("MDD_AGENT_CONFIG")); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("MDD_AGENT_CONFIG must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" {
		return filepath.Join(root, "mdd-agent", "config.json"), nil
	}
	return filepath.Join(root, "MDD Agent", "config.json"), nil
}

func runConfigCommand(arguments []string, input io.Reader, output io.Writer) error {
	path, arguments, err := extractConfigPath(arguments)
	if err != nil {
		return err
	}
	if len(arguments) == 0 {
		return errors.New("usage: mdd-agent config <init|show|set>")
	}
	switch arguments[0] {
	case "init":
		if len(arguments) != 1 {
			return errors.New("config init accepts only an optional -config path")
		}
		if _, err := os.Lstat(path); err == nil {
			return errors.New("configuration already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		settings, err := newEditableConfig()
		if err != nil {
			return err
		}
		if err := saveConfig(path, settings); err != nil {
			return err
		}
		return writeConfigView(output, path, settings)
	case "show":
		if len(arguments) != 1 {
			return errors.New("config show accepts only an optional -config path")
		}
		settings, err := readConfigForEdit(path, false)
		if err != nil {
			return err
		}
		return writeConfigView(output, path, settings)
	case "set":
		return setConfigValue(path, arguments[1:], input, output)
	default:
		return fmt.Errorf("unknown config command %q", arguments[0])
	}
}

func extractConfigPath(arguments []string) (string, []string, error) {
	path, err := defaultConfigPath()
	if err != nil {
		return "", nil, err
	}
	cleaned := make([]string, 0, len(arguments))
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "-config" || argument == "--config":
			index++
			if index >= len(arguments) {
				return "", nil, errors.New("-config requires a path")
			}
			path = arguments[index]
		case strings.HasPrefix(argument, "-config="):
			path = strings.TrimPrefix(argument, "-config=")
		case strings.HasPrefix(argument, "--config="):
			path = strings.TrimPrefix(argument, "--config=")
		default:
			cleaned = append(cleaned, argument)
		}
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return "", nil, errors.New("configuration path must be absolute")
	}
	return filepath.Clean(path), cleaned, nil
}

func setConfigValue(path string, arguments []string, input io.Reader, output io.Writer) error {
	if len(arguments) < 1 {
		return errors.New("usage: mdd-agent config set <agent_id|server|token|tls_sha256|sim_pin|sim_pin_remove|modem_enabled|modem_sim_apdu_enabled|raw_usb_source_enabled|raw_usb_importer_enabled> <value>")
	}
	settings, err := readConfigForEdit(path, true)
	if err != nil {
		return err
	}
	field := strings.ToLower(strings.TrimSpace(arguments[0]))
	valueArguments := arguments[1:]
	switch field {
	case "agent_id":
		if len(valueArguments) != 1 {
			return errors.New("agent_id requires one value")
		}
		settings.Agent.ID = strings.TrimSpace(valueArguments[0])
	case "server":
		if len(valueArguments) != 1 {
			return errors.New("server requires one host:port or exact wss URL")
		}
		settings.Agent.ServerURL, err = normalizeAgentServer(valueArguments[0])
	case "token":
		if len(valueArguments) != 1 || valueArguments[0] != "--stdin" {
			return errors.New("token must be supplied as --stdin so it is not exposed in process arguments")
		}
		var payload []byte
		payload, err = io.ReadAll(io.LimitReader(input, maximumAgentConfigBytes+1))
		if err == nil {
			settings.Agent.ServerToken = strings.TrimSpace(string(payload))
			if len(payload) > maximumAgentConfigBytes {
				err = errors.New("token input is too large")
			}
		}
	case "tls_sha256":
		if len(valueArguments) != 1 {
			return errors.New("tls_sha256 requires one SHA-256 certificate fingerprint")
		}
		settings.Agent.TLSFingerprint, err = normalizeSHA256(valueArguments[0])
	case "sim_pin":
		if len(valueArguments) != 2 || valueArguments[1] != "--stdin" || !digits(valueArguments[0], 1, 64) {
			return errors.New("sim_pin requires one numeric ICCID and --stdin")
		}
		var payload []byte
		payload, err = io.ReadAll(io.LimitReader(input, 64))
		pin := strings.TrimSpace(string(payload))
		if err == nil && (len(payload) > 64 || !digits(pin, 4, 8)) {
			err = errors.New("SIM PIN must contain 4 to 8 digits")
		}
		if err == nil {
			if settings.Agent.PINs == nil {
				settings.Agent.PINs = map[string]string{}
			}
			if settings.Agent.PINRevisions == nil {
				settings.Agent.PINRevisions = map[string]string{}
			}
			settings.Agent.PINs[valueArguments[0]] = pin
			settings.Agent.PINRevisions[valueArguments[0]], err = randomHex(16)
		}
	case "sim_pin_remove":
		if len(valueArguments) != 1 || !digits(valueArguments[0], 1, 64) {
			return errors.New("sim_pin_remove requires one numeric ICCID")
		}
		delete(settings.Agent.PINs, valueArguments[0])
		delete(settings.Agent.PINRevisions, valueArguments[0])
	case "modem_enabled":
		if len(valueArguments) != 1 {
			return errors.New("modem_enabled requires true or false")
		}
		switch strings.ToLower(valueArguments[0]) {
		case "true":
			if runtime.GOOS != "windows" && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
				return errors.New("modem_enabled is currently available only on Windows, macOS, and Linux")
			}
			settings.Agent.ModemEnabled = true
		case "false":
			settings.Agent.ModemEnabled = false
			settings.Agent.ModemSIMAPDU = false
			settings.Agent.RawUSBSource = false
			settings.Agent.RawUSBImporter = false
		default:
			return errors.New("modem_enabled requires true or false")
		}
	case "modem_sim_apdu_enabled":
		if len(valueArguments) != 1 {
			return errors.New("modem_sim_apdu_enabled requires true or false")
		}
		switch strings.ToLower(valueArguments[0]) {
		case "true":
			if runtime.GOOS != "windows" && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
				return errors.New("modem_sim_apdu_enabled is currently available only on Windows, macOS, and Linux")
			}
			if !settings.Agent.ModemEnabled {
				return errors.New("modem_sim_apdu_enabled requires modem_enabled")
			}
			settings.Agent.ModemSIMAPDU = true
		case "false":
			settings.Agent.ModemSIMAPDU = false
		default:
			return errors.New("modem_sim_apdu_enabled requires true or false")
		}
	case "raw_usb_source_enabled", "raw_usb_importer_enabled":
		if len(valueArguments) != 1 {
			return fmt.Errorf("%s requires true or false", field)
		}
		if field == "raw_usb_source_enabled" && runtime.GOOS != "windows" && runtime.GOOS != "linux" {
			return errors.New("raw USB modem source mode is available only on Windows and Linux")
		}
		if field == "raw_usb_importer_enabled" && runtime.GOOS != "linux" {
			return errors.New("raw USB modem importer mode is available only on Linux")
		}
		if !settings.Agent.ModemEnabled && strings.EqualFold(valueArguments[0], "true") {
			return errors.New("raw USB modem mode requires modem_enabled")
		}
		var enabled bool
		switch strings.ToLower(valueArguments[0]) {
		case "true":
			enabled = true
		case "false":
		default:
			return fmt.Errorf("%s requires true or false", field)
		}
		if field == "raw_usb_source_enabled" {
			settings.Agent.RawUSBSource = enabled
		} else {
			settings.Agent.RawUSBImporter = enabled
		}
	default:
		return fmt.Errorf("unknown configuration field %q", field)
	}
	if err != nil {
		return err
	}
	if err := validateEditedField(field, settings); err != nil {
		return err
	}
	if err := saveConfig(path, settings); err != nil {
		return err
	}
	return writeConfigView(output, path, settings)
}

func normalizeAgentServer(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		if _, _, err := net.SplitHostPort(value); err != nil {
			return "", errors.New("server must use host:port or an exact wss URL")
		}
		value = "wss://" + value + "/v1/agent/ws"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "wss" || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() == "" ||
		parsed.Path == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("server must be an exact wss URL with host, port, and path")
	}
	return parsed.String(), nil
}

func normalizeSHA256(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), ":", "")
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("tls_sha256 must contain exactly 32 SHA-256 bytes")
	}
	return strings.ToLower(value), nil
}

func validateEditedField(field string, settings config) error {
	switch field {
	case "agent_id":
		if err := (agentlink.Hello{SchemaVersion: agentlink.SchemaVersion, AgentID: settings.Agent.ID, ProcessGeneration: "validation"}).Validate(); err != nil {
			return errors.New("agent_id contains unsupported characters")
		}
	case "token":
		if len(settings.Agent.ServerToken) < 32 {
			return errors.New("Agent server token must contain at least 32 bytes")
		}
	}
	return nil
}

func newEditableConfig() (config, error) {
	agentSuffix, err := randomHex(8)
	if err != nil {
		return config{}, err
	}
	controlToken, err := randomHex(32)
	if err != nil {
		return config{}, err
	}
	settings := config{Version: 1, ScanIntervalMS: 1000, RetryBaseMS: 1000, RetryCapMS: 30000, OperationTimeoutSeconds: 30}
	settings.Agent.ID = "agent-" + agentSuffix
	settings.Agent.PINs = map[string]string{}
	settings.Agent.PINRevisions = map[string]string{}
	settings.Agent.ModemEnabled = false
	settings.Control.Listen = defaultAgentControlAddress
	settings.Control.Token = controlToken
	return settings, nil
}

func randomHex(size int) (string, error) {
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		return "", err
	}
	return hex.EncodeToString(payload), nil
}

func readConfigForEdit(path string, create bool) (config, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		return newEditableConfig()
	}
	if err != nil {
		return config{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumAgentConfigBytes {
		return config{}, errors.New("configuration must be a non-empty regular file no larger than 64 KiB")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return config{}, fmt.Errorf("configuration permissions must be 0600, got %04o", info.Mode().Perm())
	}
	if err := validateConfigOwner(info); err != nil {
		return config{}, err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return config{}, err
	}
	var settings config
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return config{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return config{}, errors.New("configuration has trailing JSON")
	}
	if settings.Version != 1 {
		return config{}, errors.New("unsupported Agent configuration version")
	}
	return settings, nil
}

func saveConfig(path string, settings config) error {
	directory := filepath.Dir(path)
	directoryCreated := false
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		directoryCreated = true
	} else if err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("configuration directory must be a real directory")
		}
		if err := validateConfigOwner(info); err != nil {
			return err
		}
		if directoryCreated {
			if err := os.Chmod(directory, 0o700); err != nil {
				return err
			}
		} else if info.Mode().Perm()&0o022 != 0 {
			return errors.New("configuration directory must not be writable by group or other users")
		}
	}
	payload, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := atomicwriter.WriteFile(path, payload, 0o600); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		directoryFile, err := os.Open(directory)
		if err != nil {
			return err
		}
		err = directoryFile.Sync()
		if closeErr := directoryFile.Close(); err == nil {
			err = closeErr
		}
		return err
	}
	return nil
}

func writeConfigView(output io.Writer, path string, settings config) error {
	viewSettings := settings
	if viewSettings.Agent.ServerToken != "" {
		viewSettings.Agent.ServerToken = "<redacted>"
	}
	viewSettings.Agent.PINs = make(map[string]string, len(settings.Agent.PINs))
	for cardID := range settings.Agent.PINs {
		viewSettings.Agent.PINs[cardID] = "<configured>"
	}
	if viewSettings.Control.Token != "" {
		viewSettings.Control.Token = "<redacted>"
	}
	view := configView{Path: path, Settings: viewSettings}
	if candidate := settings; candidate.validate() == nil {
		view.Ready = true
	} else {
		view.Problem = candidate.validate().Error()
	}
	return json.NewEncoder(output).Encode(view)
}
