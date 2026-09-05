package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentconnection"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentevents"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agenthost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpin"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentrawusb"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsms"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/pcscmonitor"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/pintls"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawcapture"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

const maximumAgentConfigBytes = 64 << 10

type config struct {
	configPath string
	Version    int `json:"version"`
	Agent      struct {
		ID             string            `json:"id"`
		ServerURL      string            `json:"server_url"`
		ServerToken    string            `json:"server_token"`
		TLSFingerprint string            `json:"tls_sha256"`
		PINs           map[string]string `json:"pins"`
		PINRevisions   map[string]string `json:"pin_revisions,omitempty"`
		ModemEnabled   bool              `json:"modem_enabled"`
		ModemSIMAPDU   bool              `json:"modem_sim_apdu_enabled"`
		RawUSBSource   bool              `json:"raw_usb_source_enabled,omitempty"`
		RawUSBImporter bool              `json:"raw_usb_importer_enabled,omitempty"`
	} `json:"agent"`
	Control struct {
		Listen string `json:"listen"`
		Token  string `json:"token"`
	} `json:"control"`
	ScanIntervalMS          int `json:"scan_interval_ms"`
	RetryBaseMS             int `json:"retry_base_ms"`
	RetryCapMS              int `json:"retry_cap_ms"`
	OperationTimeoutSeconds int `json:"operation_timeout_seconds"`
}

type modemProberOptions struct {
	Enabled        bool
	SIMAPDU        bool
	ManagedRuntime bool
	AgentID        string
	RawRecovery    *agentrawusb.RecoveryStore
	RecoveryOnly   bool
}

func main() {
	arguments := os.Args[1:]
	if len(arguments) == 0 {
		if command := defaultLaunchCommand(); command != "" {
			arguments = []string{command}
		}
	}
	if len(arguments) == 0 {
		fatalf("usage: mdd-agent <config|modem-probe|run|gui|status|topology|raw-modes|raw-mode|start|stop|service|service-install|service-uninstall|service-start|service-stop|service-status>")
	}
	command := arguments[0]
	if command == "cellular-guard" {
		if err := runCellularGuardCommand(arguments[1:]); err != nil {
			fatalf("cellular-guard: %v", err)
		}
		return
	}
	if command == "config" {
		if err := runConfigCommand(arguments[1:], os.Stdin, os.Stdout); err != nil {
			fatalf("config: %v", err)
		}
		return
	}
	if command == "modem-probe" {
		probeFlags := flag.NewFlagSet(command, flag.ContinueOnError)
		simAPDU := probeFlags.Bool("sim-apdu-capability", false, "run only the non-mutating CCHO/CGLA/CCHC test forms")
		simPINStatus := probeFlags.Bool("sim-pin-status", false, "read CPIN/QCCID/QPINC without entering a credential")
		if err := probeFlags.Parse(arguments[1:]); err != nil {
			fatalf("modem-probe: %v", err)
		}
		if probeFlags.NArg() != 0 {
			fatalf("modem-probe: unexpected positional arguments")
		}
		if err := runModemProbe(os.Stdout, *simAPDU, *simPINStatus); err != nil {
			fatalf("modem-probe: %v", err)
		}
		return
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	defaultPath, err := defaultConfigPath()
	if err != nil {
		fatalf("resolve config path: %v", err)
	}
	configPath := flags.String("config", defaultPath, "path to the 0600 Agent JSON configuration")
	if err := flags.Parse(arguments[1:]); err != nil {
		fatalf("parse command: %v", err)
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		fatalf("load config: %v", err)
	}
	if command == "run" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		worker, err := buildWorker(settings)
		if err == nil {
			err = runHost(ctx, settings, worker)
		}
		if err != nil {
			fatalf("run: %v", err)
		}
		return
	}
	if command == "raw-modes" || command == "raw-mode" {
		if err := runRawModeClient(command, flags.Args(), settings, os.Stdout); err != nil {
			fatalf("%s: %v", command, err)
		}
		return
	}
	if command == "gui" {
		if err := runGUI(settings, *configPath); err != nil {
			fatalf("gui: %v", err)
		}
		return
	}
	if command == "service" || strings.HasPrefix(command, "service-") {
		if err := runOSService(command, *configPath, settings, os.Stdout); err != nil {
			fatalf("%s: %v", command, err)
		}
		return
	}
	if command != "status" && command != "topology" && command != "start" && command != "stop" {
		fatalf("unknown command %q", command)
	}
	if err := runClient(command, settings, os.Stdout); err != nil {
		fatalf("%s: %v", command, err)
	}
}

func runRawModeClient(command string, arguments []string, settings config, output io.Writer) error {
	client, err := agentcontrol.NewClient("http://"+settings.Control.Listen, settings.Control.Token,
		&http.Client{Timeout: time.Duration(settings.OperationTimeoutSeconds) * time.Second})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(settings.OperationTimeoutSeconds)*time.Second)
	defer cancel()
	var snapshot rawcapture.Snapshot
	if command == "raw-modes" {
		if len(arguments) != 0 {
			return errors.New("raw-modes accepts no positional arguments")
		}
		snapshot, err = client.RawModes(ctx)
	} else {
		if len(arguments) != 3 || (arguments[2] != "raw" && arguments[2] != "adapted") {
			return errors.New("usage: mdd-agent raw-mode <equipment-id> <iccid> <raw|adapted>")
		}
		snapshot, err = client.SetRawMode(ctx,
			rawcapture.Pair{EquipmentID: arguments[0], CardID: arguments[1]}, arguments[2] == "raw")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(snapshot)
}

func loadConfig(path string) (config, error) {
	var settings config
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return settings, errors.New("configuration path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return settings, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumAgentConfigBytes {
		return settings, errors.New("configuration must be a non-empty regular file no larger than 64 KiB")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return settings, fmt.Errorf("configuration permissions must be 0600, got %04o", info.Mode().Perm())
	}
	if err := validateConfigOwner(info); err != nil {
		return settings, err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return settings, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return settings, errors.New("configuration has trailing JSON")
	}
	if err := settings.validate(); err != nil {
		return settings, err
	}
	settings.configPath = path
	return settings, nil
}

func (settings *config) validate() error {
	if settings.Version != 1 {
		return errors.New("unsupported Agent configuration version")
	}
	settings.Agent.ID = strings.TrimSpace(settings.Agent.ID)
	settings.Agent.ServerURL = strings.TrimSpace(settings.Agent.ServerURL)
	settings.Agent.ServerToken = strings.TrimSpace(settings.Agent.ServerToken)
	settings.Agent.TLSFingerprint = strings.TrimSpace(settings.Agent.TLSFingerprint)
	settings.Control.Listen = strings.TrimSpace(settings.Control.Listen)
	settings.Control.Token = strings.TrimSpace(settings.Control.Token)
	if err := (agentlink.Hello{SchemaVersion: 1, AgentID: settings.Agent.ID, ProcessGeneration: "validation"}).Validate(); err != nil {
		return err
	}
	if len(settings.Agent.ServerToken) < 32 || len(settings.Control.Token) < 32 {
		return errors.New("Agent server and control tokens must each contain at least 32 bytes")
	}
	if settings.Agent.ModemEnabled && runtime.GOOS != "windows" && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return errors.New("modem_enabled is currently available only on Windows, macOS, and Linux")
	}
	if settings.Agent.ModemSIMAPDU && !settings.Agent.ModemEnabled {
		return errors.New("modem_sim_apdu_enabled requires modem_enabled")
	}
	if (settings.Agent.RawUSBSource || settings.Agent.RawUSBImporter) && !settings.Agent.ModemEnabled {
		return errors.New("raw USB modem mode requires modem_enabled")
	}
	if settings.Agent.RawUSBSource && runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		return errors.New("raw USB modem source mode is available only on Windows and Linux")
	}
	if settings.Agent.RawUSBImporter && runtime.GOOS != "linux" {
		return errors.New("raw USB modem importer mode is available only on Linux")
	}
	if settings.ScanIntervalMS == 0 {
		settings.ScanIntervalMS = 1000
	}
	if settings.RetryBaseMS == 0 {
		settings.RetryBaseMS = 1000
	}
	if settings.RetryCapMS == 0 {
		settings.RetryCapMS = 30000
	}
	if settings.OperationTimeoutSeconds == 0 {
		settings.OperationTimeoutSeconds = 30
	}
	if settings.ScanIntervalMS < 100 || settings.ScanIntervalMS > 60000 || settings.RetryBaseMS < 100 ||
		settings.RetryCapMS < settings.RetryBaseMS || settings.RetryCapMS > 300000 ||
		settings.OperationTimeoutSeconds < 1 || settings.OperationTimeoutSeconds > 60 {
		return errors.New("Agent interval or timeout configuration is out of range")
	}
	for cardID, pin := range settings.Agent.PINs {
		if !digits(cardID, 1, 64) || pin != "" && !digits(pin, 4, 8) {
			return errors.New("PIN map must use numeric card IDs and 4-8 digit PINs")
		}
	}
	for cardID, revision := range settings.Agent.PINRevisions {
		decoded, err := hex.DecodeString(revision)
		if _, exists := settings.Agent.PINs[cardID]; !exists || !digits(cardID, 1, 64) || err != nil || len(decoded) != 16 {
			return errors.New("PIN revisions must match configured ICCIDs and contain 16 random bytes")
		}
	}
	if _, err := pintls.NewHTTPClient(settings.Agent.ServerURL, settings.Agent.TLSFingerprint, 10*time.Second); err != nil {
		return err
	}
	if _, err := agentcontrol.NewClient("http://"+settings.Control.Listen, settings.Control.Token, nil); err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(settings.Control.Listen)
	parsedPort, portErr := strconv.ParseUint(port, 10, 16)
	if portErr != nil || parsedPort == 0 {
		return errors.New("Agent control address requires a fixed nonzero port")
	}
	return nil
}

func buildWorker(settings config) (*agenthost.Worker, error) {
	httpClient, err := pintls.NewHTTPClient(settings.Agent.ServerURL, settings.Agent.TLSFingerprint, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var rawRecovery *agentrawusb.RecoveryStore
	recoveryOnly := false
	if runtime.GOOS == "windows" || runtime.GOOS == "linux" {
		rawRecovery, err = agentrawusb.OpenRecoveryStore(
			filepath.Join(filepath.Dir(settings.configPath), "state", "raw-modem-handoffs.db"), time.Second)
		if err != nil {
			return nil, err
		}
		records, recordsErr := rawRecovery.Records()
		if recordsErr != nil {
			_ = rawRecovery.Close()
			return nil, recordsErr
		}
		for _, record := range records {
			if record.SourceAgentID != settings.Agent.ID {
				_ = rawRecovery.Close()
				return nil, errors.New("configured Agent ID differs from an unresolved raw modem handoff")
			}
		}
		recoveryOnly = !settings.Agent.ModemEnabled && len(records) != 0
	}
	modems, err := newModemProber(modemProberOptions{
		Enabled: settings.Agent.ModemEnabled || recoveryOnly, SIMAPDU: settings.Agent.ModemSIMAPDU,
		ManagedRuntime: true, AgentID: settings.Agent.ID, RawRecovery: rawRecovery, RecoveryOnly: recoveryOnly,
	})
	if err != nil {
		if rawRecovery != nil {
			_ = rawRecovery.Close()
		}
		return nil, err
	}
	if modems == nil && rawRecovery != nil {
		_ = rawRecovery.Close()
	}
	var operations agentmodem.ManagedOperator
	var media agentmodem.MediaOperator
	var data agentdata.Backend
	var connection *agentconnection.Manager
	var modemSIMs agentmodem.SIMAuthenticator
	var modemPINRuntime agentmodem.SIMPINRuntime
	var auxiliary agentmodem.AuxiliaryCoordinator
	var provisionHardware agenthost.ProvisionHardware
	var rawUSBSource agentrawusb.SourceBackend
	var rawUSBImportGuard agentrawusb.ImportGuard
	var rawCapture *rawcapture.Controller
	var pinRecovery agentmodem.PINRecoverer
	var modemEvents *agentevents.Store
	var modemEventOperator agentmodem.Operator
	var modemEventCoordinator agentmodem.BackgroundScanCoordinator
	var modemPolicies *agentpolicy.Manager
	if modems != nil {
		modemPINRuntime, _ = modems.(agentmodem.SIMPINRuntime)
		if provider, ok := modems.(interface {
			ProvisionHardware() agenthost.ProvisionHardware
		}); ok {
			provisionHardware = provider.ProvisionHardware()
		}
	}
	ownershipTransferred := false
	defer func() {
		if ownershipTransferred {
			return
		}
		if closer, ok := modems.(io.Closer); ok {
			_ = closer.Close()
		}
		if modemEvents != nil {
			_ = modemEvents.Close()
		}
		if modemPolicies != nil {
			_ = modemPolicies.Close()
		}
	}()
	if modems != nil && settings.Agent.ModemEnabled {
		operator, ok := modems.(agentmodem.Operator)
		if !ok {
			return nil, errors.New("enabled modem does not support operations")
		}
		if settings.configPath == "" {
			return nil, errors.New("enabled modem requires a loaded configuration path")
		}
		var openErr error
		modemEvents, openErr = agentevents.Open(
			filepath.Join(filepath.Dir(settings.configPath), "state", "modem-events.db"), time.Second)
		if openErr != nil {
			return nil, openErr
		}
		store, openErr := agentcall.Open(filepath.Join(filepath.Dir(settings.configPath), "state", "paid-calls.db"), time.Second)
		if openErr != nil {
			return nil, openErr
		}
		callManager, managerErr := agentcall.NewManager(store, operator)
		if managerErr != nil {
			_ = store.Close()
			return nil, managerErr
		}
		if managerErr = callManager.BindIncomingCallVerifier(modemEvents); managerErr != nil {
			_ = callManager.Close()
			return nil, managerErr
		}
		policyRuntime, ok := modems.(agentpolicy.Runtime)
		if !ok {
			_ = callManager.Close()
			return nil, errors.New("enabled modem does not support persistent policy operations")
		}
		policyStore, openErr := agentpolicy.Open(
			filepath.Join(filepath.Dir(settings.configPath), "state", "modem-policies.db"), time.Second)
		if openErr != nil {
			_ = callManager.Close()
			return nil, openErr
		}
		modemPolicies, managerErr = agentpolicy.New(agentpolicy.Config{Store: policyStore, Runtime: policyRuntime,
			Coordinator: callManager, Recovery: recovery.Policy{Base: time.Duration(settings.RetryBaseMS) * time.Millisecond,
				Cap: time.Duration(settings.RetryCapMS) * time.Millisecond}})
		if managerErr != nil {
			_ = policyStore.Close()
			_ = callManager.Close()
			return nil, managerErr
		}
		smsStore, openErr := agentsms.Open(filepath.Join(filepath.Dir(settings.configPath), "state", "sms-operations.db"), time.Second)
		if openErr != nil {
			_ = callManager.Close()
			return nil, openErr
		}
		smsManager, managerErr := agentsms.NewManager(smsStore, callManager)
		if managerErr != nil {
			_ = smsStore.Close()
			_ = callManager.Close()
			return nil, managerErr
		}
		operations = smsManager
		auxiliary = callManager
		modemEventOperator = operator
		modemEventCoordinator = callManager
		media, _ = modems.(agentmodem.MediaOperator)
		data, _ = modems.(agentdata.Backend)
		if data != nil {
			connection, managerErr = agentconnection.New(data)
			if managerErr != nil {
				_ = operations.(interface{ Close() error }).Close()
				return nil, managerErr
			}
			data = connection
			if managerErr = modemPolicies.BindConnection(connection); managerErr != nil {
				_ = operations.(interface{ Close() error }).Close()
				return nil, managerErr
			}
		}
		if len(settings.Agent.PINs) != 0 {
			pinRuntime, ok := modems.(agentmodem.SIMPINRuntime)
			if !ok {
				_ = operations.(interface{ Close() error }).Close()
				return nil, errors.New("enabled modem does not support typed SIM PIN recovery")
			}
			pinStore, openErr := agentpin.Open(filepath.Join(filepath.Dir(settings.configPath), "state", "sim-pin-attempts.db"), time.Second)
			if openErr != nil {
				_ = operations.(interface{ Close() error }).Close()
				return nil, openErr
			}
			pinManager, managerErr := agentpin.NewManager(pinStore, pinRuntime, callManager,
				settings.Agent.PINs, settings.Agent.PINRevisions)
			if managerErr != nil {
				_ = pinStore.Close()
				_ = operations.(interface{ Close() error }).Close()
				return nil, managerErr
			}
			pinRecovery = pinManager
		}
		if settings.Agent.ModemSIMAPDU {
			var ok bool
			modemSIMs, ok = modems.(agentmodem.SIMAuthenticator)
			if !ok {
				if closer, closeOK := pinRecovery.(interface{ Close() error }); closeOK {
					_ = closer.Close()
				}
				_ = operations.(interface{ Close() error }).Close()
				return nil, errors.New("enabled modem SIM APDU does not support typed AKA")
			}
		}
		if settings.Agent.RawUSBSource {
			captureBackend, ok := modems.(rawcapture.Backend)
			if !ok {
				if closer, closeOK := pinRecovery.(interface{ Close() error }); closeOK {
					_ = closer.Close()
				}
				_ = operations.(interface{ Close() error }).Close()
				return nil, errors.New("enabled modem does not support exact raw USB source ownership")
			}
			captureStore, openErr := rawcapture.Open(
				filepath.Join(filepath.Dir(settings.configPath), "state", "raw-captures.db"), time.Second)
			if openErr != nil {
				return nil, openErr
			}
			rawCapture, openErr = rawcapture.New(rawcapture.Config{Store: captureStore, Backend: captureBackend})
			if openErr != nil {
				_ = captureStore.Close()
				return nil, openErr
			}
			rawUSBSource = rawCapture
		}
		if settings.Agent.RawUSBImporter {
			var ok bool
			rawUSBImportGuard, ok = modems.(agentrawusb.ImportGuard)
			if !ok {
				if closer, closeOK := pinRecovery.(interface{ Close() error }); closeOK {
					_ = closer.Close()
				}
				_ = operations.(interface{ Close() error }).Close()
				return nil, errors.New("enabled modem does not support guarded raw USB import")
			}
		}
	}
	if settings.configPath == "" {
		if closer, ok := pinRecovery.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		if closer, ok := operations.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return nil, errors.New("Agent eUICC downloads require a loaded configuration path")
	}
	downloadStore, err := agentsim.OpenDownloadStore(
		filepath.Join(filepath.Dir(settings.configPath), "state", "euicc-downloads.db"), time.Second)
	if err != nil {
		if closer, ok := pinRecovery.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		if closer, ok := operations.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return nil, err
	}
	worker, err := agenthost.New(agenthost.Config{
		ServerURL: settings.Agent.ServerURL, ServerToken: settings.Agent.ServerToken,
		AgentID: settings.Agent.ID, HTTPClient: httpClient,
		Monitors: pcscmonitor.Factory{}, Connector: agentsim.PCSCConnector{}, Modems: modems, Operations: operations, Media: media, Data: data,
		ModemSIMs: modemSIMs, ModemPINRuntime: modemPINRuntime, ModemAuxiliary: auxiliary,
		ProvisionHardware: provisionHardware,
		ModemEvents:       modemEvents, ModemPolicies: modemPolicies, ModemEventOperator: modemEventOperator,
		ModemEventCoordinator: modemEventCoordinator,
		RawUSBSource:          rawUSBSource, RawUSBImportGuard: rawUSBImportGuard,
		RawCapture: rawCapture,
		ModemPINs:  pinRecovery, EUICCDownloads: downloadStore,
		PINs:      settings.Agent.PINs,
		ScanEvery: time.Duration(settings.ScanIntervalMS) * time.Millisecond,
		Recovery:  recovery.Policy{Base: time.Duration(settings.RetryBaseMS) * time.Millisecond, Cap: time.Duration(settings.RetryCapMS) * time.Millisecond},
	})
	if err != nil && operations != nil {
		if closer, ok := pinRecovery.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		_ = operations.(interface{ Close() error }).Close()
	}
	if err != nil {
		_ = downloadStore.Close()
	}
	ownershipTransferred = err == nil
	return worker, err
}

func runModemProbe(output io.Writer, simAPDU, simPINStatus bool) error {
	// The diagnostic remains read-only. Persistent data quarantine is installed
	// only by the managed Agent runtime, never by modem-probe.
	prober, err := newModemProber(modemProberOptions{Enabled: true, SIMAPDU: simAPDU})
	if err != nil {
		return err
	}
	if closer, ok := prober.(io.Closer); ok {
		defer closer.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var facts []agentmodem.Fact
	if simPINStatus {
		if diagnostic, ok := prober.(interface {
			ProbeSIMPINStatus(context.Context) ([]agentmodem.Fact, error)
		}); ok {
			facts, err = diagnostic.ProbeSIMPINStatus(ctx)
		} else {
			err = errors.New("SIM PIN status diagnostic is unavailable on this platform")
		}
	} else {
		facts, err = prober.Probe(ctx)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{
		"condition": "ready", "modems": facts,
	})
}

func runHost(ctx context.Context, settings config, worker agentcontrol.Worker) error {
	return runHostWithReady(ctx, settings, worker, nil)
}

func runHostWithReady(ctx context.Context, settings config, worker agentcontrol.Worker, ready func()) error {
	if closer, ok := worker.(io.Closer); ok {
		defer closer.Close()
	}
	controller, err := agentcontrol.New(worker, nil)
	if err != nil {
		return err
	}
	timeout := time.Duration(settings.OperationTimeoutSeconds) * time.Second
	topology, _ := worker.(agentcontrol.TopologyProvider)
	api, err := agentcontrol.NewAPI(controller, settings.Control.Token, timeout, topology)
	if err != nil {
		return err
	}
	listener, err := agentcontrol.ListenLoopback(settings.Control.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: api, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	if ready != nil {
		ready()
	}
	startContext, cancelStart := context.WithTimeout(ctx, timeout)
	_, startErr := controller.Start(startContext)
	cancelStart()
	if startErr != nil && ctx.Err() == nil {
		log.Printf("mdd-agent runtime start: %v", startErr)
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serveDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}
	stopContext, cancelStop := context.WithTimeout(context.Background(), timeout)
	_, stopErr := controller.Stop(stopContext)
	cancelStop()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(shutdownContext)
	cancelShutdown()
	return errors.Join(serveErr, stopErr, shutdownErr)
}

func runClient(command string, settings config, output io.Writer) error {
	client, err := agentcontrol.NewClient("http://"+settings.Control.Listen, settings.Control.Token, &http.Client{Timeout: time.Duration(settings.OperationTimeoutSeconds) * time.Second})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(settings.OperationTimeoutSeconds)*time.Second)
	defer cancel()
	var result any
	switch command {
	case "status":
		result, err = client.Status(ctx)
	case "topology":
		result, err = client.Topology(ctx)
	case "start":
		result, err = client.Start(ctx)
	case "stop":
		result, err = client.Stop(ctx)
	}
	if encodeErr := json.NewEncoder(output).Encode(result); err == nil {
		err = encodeErr
	}
	return err
}

func digits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func fatalf(format string, arguments ...any) {
	log.Printf("mdd-agent: "+format, arguments...)
	os.Exit(2)
}
