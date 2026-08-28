// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerfacts"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/agentaka"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/browsermedia"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/service"
)

const maximumConfigBytes = 64 << 10

type config struct {
	LineID     string `json:"line_id"`
	ProviderID string `json:"provider_id"`
	DeviceID   string `json:"device_id"`
	TraceID    string `json:"trace_id"`
	IPC        struct {
		Listen             string `json:"listen"`
		Token              string `json:"token"`
		StatePath          string `json:"state_path"`
		OperationTimeoutMS int    `json:"operation_timeout_ms"`
		ShutdownTimeoutMS  int    `json:"shutdown_timeout_ms"`
		MediaCapacity      int    `json:"media_capacity"`
		CallGuardTimeoutMS int    `json:"call_guard_timeout_ms"`
	} `json:"ipc"`
	Core struct {
		RegistrationURL   string `json:"registration_url"`
		RegistrationToken string `json:"registration_token"`
		RefreshMS         int    `json:"refresh_ms"`
	} `json:"core"`
	Agent struct {
		BrokerURL   string `json:"broker_url"`
		BrokerToken string `json:"broker_token"`
		CardID      string `json:"card_id"`
		TimeoutMS   int    `json:"timeout_ms"`
	} `json:"agent"`
	SIM struct {
		IMSI string `json:"imsi"`
		MCC  string `json:"mcc"`
		MNC  string `json:"mnc"`
		IMEI string `json:"imei"`
		SMSC string `json:"smsc"`
	} `json:"sim"`
	Network struct {
		EPDGAddress    string   `json:"epdg_address"`
		PCSCF          []string `json:"pcscf"`
		IKETimeoutMS   int      `json:"ike_timeout_ms"`
		CloseTimeoutMS int      `json:"close_timeout_ms"`
		MTU            int      `json:"mtu"`
	} `json:"network"`
	IMS struct {
		IMPI             string `json:"impi"`
		IMPU             string `json:"impu"`
		Domain           string `json:"domain"`
		AKAAppPreference string `json:"aka_app_preference"`
		Network          string `json:"network"`
		Server           string `json:"server"`
		TimeoutMS        int    `json:"timeout_ms"`
		Expires          int    `json:"expires"`
	} `json:"ims"`
}

func main() {
	path := flag.String("config", "", "path to the 0600 JSON configuration file")
	flag.Parse()
	if *path == "" {
		log.Fatal("-config is required")
	}
	settings, err := loadConfig(*path)
	if err != nil {
		log.Fatal(err)
	}
	if err := run(settings); err != nil {
		log.Fatal(err)
	}
}

func run(settings config) error {
	factory, err := service.NewUpstreamFactory(settings.upstream())
	if err != nil {
		return err
	}
	return runWithFactory(settings, factory)
}

// runWithFactory keeps the process/IPC path testable without a production
// fake mode. The shipped binary always calls run and constructs UpstreamFactory.
func runWithFactory(settings config, factory service.Factory) error {
	if factory == nil {
		return errors.New("VoWiFi runtime factory is nil")
	}
	generation, err := newGeneration()
	if err != nil {
		return err
	}
	operations, err := service.OpenBoltOperationStore(settings.IPC.StatePath)
	if err != nil {
		return fmt.Errorf("open VoWiFi operation store: %w", err)
	}
	defer operations.Close()
	reporter, err := newMessageReporter(settings, generation, operations)
	if err != nil {
		return fmt.Errorf("configure provider message reporter: %w", err)
	}
	if reporter != nil {
		setter, ok := factory.(interface {
			SetMessageSink(service.MessageSink, string, string) error
		})
		if ok {
			err = setter.SetMessageSink(reporter, settings.ProviderID, generation)
		} else {
			// Test-only factories intentionally implement only the narrow runtime
			// contract and have no IMS listener to bind.
			reporter = nil
		}
		if err != nil {
			return err
		}
	}
	media, err := browsermedia.NewRegistry(settings.IPC.Token, settings.IPC.MediaCapacity)
	if err != nil {
		return err
	}
	backend, err := service.NewBackendWithMediaStore(
		settings.LineID, settings.ProviderID, generation, factory, operations,
		mediaDirectory{registry: media}, durationMS(settings.IPC.CallGuardTimeoutMS, 10*time.Second),
	)
	if err != nil {
		return err
	}
	api, err := vowifiipc.NewAPI(backend, settings.IPC.Token, durationMS(settings.IPC.OperationTimeoutMS, 60*time.Second))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/media/", media)
	mux.Handle("/", api)
	listener, err := net.Listen("tcp", settings.IPC.Listen)
	if err != nil {
		return fmt.Errorf("listen on VoWiFi IPC endpoint: %w", err)
	}
	defer listener.Close()
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()
	registration, err := providerRegistration(settings, generation, listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return err
	}
	var registrationCancel context.CancelFunc
	var registrationDone chan struct{}
	var registrationFailure error
	var reporterCancel context.CancelFunc
	var reporterDone chan struct{}
	if registration != nil {
		registerContext, cancelRegister := context.WithTimeout(context.Background(), 5*time.Second)
		registrationFailure = registration.initial(registerContext, backend)
		cancelRegister()
		if registrationFailure == nil {
			registrationContext, cancel := context.WithCancel(context.Background())
			registrationCancel = cancel
			registrationDone = make(chan struct{})
			go func() {
				defer close(registrationDone)
				registration.maintain(registrationContext, backend)
			}()
			if reporter != nil {
				reporterContext, cancelReporter := context.WithCancel(context.Background())
				reporterCancel = cancelReporter
				reporterDone = make(chan struct{})
				go func() {
					defer close(reporterDone)
					reporter.maintain(reporterContext)
				}()
			}
		}
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	var serveFailure error
	if registrationFailure != nil {
		serveFailure = fmt.Errorf("register VoWiFi provider with Core: %w", registrationFailure)
	} else {
		select {
		case err := <-serveErr:
			serveFailure = err
		case <-signals:
		}
	}
	if reporterCancel != nil {
		flushContext, cancelFlush := context.WithTimeout(context.Background(), 3*time.Second)
		reporter.flush(flushContext)
		cancelFlush()
		reporterCancel()
		<-reporterDone
	}
	if registrationCancel != nil {
		registrationCancel()
		<-registrationDone
		removeContext, cancelRemove := context.WithTimeout(context.Background(), 3*time.Second)
		_ = registration.client.Remove(removeContext, registration.provider.LineID, registration.provider.Generation)
		cancelRemove()
	}
	shutdownTimeout := durationMS(settings.IPC.ShutdownTimeoutMS, 10*time.Second)
	_ = listener.Close()
	stopContext, cancelStop := context.WithTimeout(context.Background(), shutdownTimeout)
	stopResult, stopErr := backend.Stop(stopContext, vowifiipc.LifecycleRequest{OperationID: "shutdown-" + generation})
	cancelStop()
	_ = stopResult
	media.CloseAll()
	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	serverErr := server.Shutdown(shutdownContext)
	cancel()
	return errors.Join(serveFailure, serverErr, stopErr)
}

type registrationLoop struct {
	client   mediaauth.RegistrationClient
	facts    providerfacts.Client
	provider mediaauth.Provider
	refresh  time.Duration
}

type snapshotSource interface {
	Status(context.Context) (vowifiipc.Snapshot, error)
}

func providerRegistration(settings config, generation, listenAddress string) (*registrationLoop, error) {
	if strings.TrimSpace(settings.Core.RegistrationURL) == "" {
		return nil, nil
	}
	client := mediaauth.RegistrationClient{URL: settings.Core.RegistrationURL, Token: settings.Core.RegistrationToken}
	if err := client.Validate(); err != nil {
		return nil, err
	}
	refresh := durationMS(settings.Core.RefreshMS, 10*time.Second)
	parsed, err := url.Parse(settings.Core.RegistrationURL)
	if err != nil {
		return nil, err
	}
	parsed.Path = "/v1/provider/facts"
	parsed.RawPath = ""
	facts := providerfacts.Client{URL: parsed.String(), Token: settings.Core.RegistrationToken}
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	return &registrationLoop{client: client, facts: facts, refresh: refresh, provider: mediaauth.Provider{
		LineID: settings.LineID, ProviderID: settings.ProviderID, Generation: generation,
		BaseURL: "ws://" + listenAddress, Token: settings.IPC.Token,
	}}, nil
}

func (registration *registrationLoop) registerAndReport(ctx context.Context, source snapshotSource) error {
	if err := registration.client.Register(ctx, registration.provider); err != nil {
		return err
	}
	snapshot, err := source.Status(ctx)
	if err != nil {
		return err
	}
	return registration.facts.Report(ctx, snapshot)
}

func (registration *registrationLoop) initial(ctx context.Context, source snapshotSource) error {
	err := registration.registerAndReport(ctx, source)
	if err == nil {
		return nil
	}
	removeContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = registration.client.Remove(removeContext, registration.provider.LineID, registration.provider.Generation)
	cancel()
	return err
}

func (registration *registrationLoop) maintain(ctx context.Context, source snapshotSource) {
	ticker := time.NewTicker(registration.refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			attempt, cancel := context.WithTimeout(ctx, min(registration.refresh, 5*time.Second))
			_ = registration.registerAndReport(attempt, source)
			cancel()
		}
	}
}

type mediaDirectory struct{ registry *browsermedia.Registry }

func (directory mediaDirectory) Lookup(id string) (service.BrowserMediaSession, bool) {
	session, found := directory.registry.Session(id)
	return session, found
}

func loadConfig(path string) (config, error) {
	var settings config
	info, err := os.Stat(path)
	if err != nil {
		return settings, fmt.Errorf("stat VoWiFi config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumConfigBytes {
		return settings, errors.New("VoWiFi config must be a non-empty regular file no larger than 64 KiB")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return settings, errors.New("VoWiFi config contains tokens and must not be accessible by group or others")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return settings, fmt.Errorf("read VoWiFi config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, fmt.Errorf("decode VoWiFi config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return settings, errors.New("VoWiFi config has trailing JSON")
	}
	if err := settings.validate(); err != nil {
		return settings, err
	}
	return settings, nil
}

func (settings config) validate() error {
	if strings.TrimSpace(settings.LineID) == "" || strings.TrimSpace(settings.ProviderID) == "" ||
		strings.TrimSpace(settings.DeviceID) == "" || len(settings.IPC.Token) < 32 ||
		len(settings.Agent.BrokerToken) < 32 || strings.TrimSpace(settings.IPC.StatePath) == "" {
		return errors.New("VoWiFi config is missing runtime identity or token")
	}
	host, port, err := net.SplitHostPort(settings.IPC.Listen)
	if err != nil || port == "" {
		return errors.New("VoWiFi IPC listen address must include a port")
	}
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !address.IsLoopback() {
		return errors.New("VoWiFi IPC must listen on a literal loopback address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("VoWiFi IPC listen port must be between 1 and 65535")
	}
	if !filepath.IsAbs(settings.IPC.StatePath) {
		return errors.New("VoWiFi operation state path must be absolute")
	}
	registrationURL := strings.TrimSpace(settings.Core.RegistrationURL)
	registrationToken := strings.TrimSpace(settings.Core.RegistrationToken)
	if (registrationURL == "") != (registrationToken == "") {
		return errors.New("Core provider registration URL and token must be configured together")
	}
	if registrationURL != "" {
		if settings.Core.RefreshMS != 0 && (settings.Core.RefreshMS < 1000 || settings.Core.RefreshMS > 25_000) {
			return errors.New("Core provider registration refresh must be between 1 and 25 seconds")
		}
		if err := (mediaauth.RegistrationClient{URL: registrationURL, Token: registrationToken}).Validate(); err != nil {
			return err
		}
	}
	for _, value := range []int{
		settings.IPC.OperationTimeoutMS, settings.IPC.ShutdownTimeoutMS, settings.Agent.TimeoutMS,
		settings.Network.IKETimeoutMS, settings.Network.CloseTimeoutMS, settings.IMS.TimeoutMS,
	} {
		if value < 0 || value > 120_000 {
			return errors.New("VoWiFi timeout must be between 0 and 120000 ms")
		}
	}
	if settings.IPC.CallGuardTimeoutMS < 0 || settings.IPC.CallGuardTimeoutMS > 60_000 {
		return errors.New("call guard timeout must be between 0 and 60000 ms")
	}
	return nil
}

func (settings config) upstream() service.UpstreamConfig {
	return service.UpstreamConfig{
		LineID: settings.LineID, DeviceID: settings.DeviceID, TraceID: settings.TraceID,
		Profile: identity.Profile{
			IMSI: settings.SIM.IMSI, MCC: settings.SIM.MCC, MNC: settings.SIM.MNC,
			IMEI: settings.SIM.IMEI, SMSC: settings.SIM.SMSC,
		},
		EPDGAddress: settings.Network.EPDGAddress, PCSCF: settings.Network.PCSCF,
		IMPI: settings.IMS.IMPI, IMPU: settings.IMS.IMPU, IMSDomain: settings.IMS.Domain,
		AKAAppPreference: settings.IMS.AKAAppPreference,
		Agent: agentaka.Config{
			CardID:  settings.Agent.CardID,
			Timeout: durationMS(settings.Agent.TimeoutMS, 15*time.Second),
		},
		BrokerURL: settings.Agent.BrokerURL, BrokerToken: settings.Agent.BrokerToken,
		IKETimeout:   durationMS(settings.Network.IKETimeoutMS, 30*time.Second),
		SIPTimeout:   durationMS(settings.IMS.TimeoutMS, 15*time.Second),
		CloseTimeout: durationMS(settings.Network.CloseTimeoutMS, 5*time.Second),
		MTU:          settings.Network.MTU, SIPNetwork: settings.IMS.Network, SIPServer: settings.IMS.Server,
		SIPExpires: settings.IMS.Expires,
	}
}

func durationMS(value int, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}

func newGeneration() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate provider process generation: %w", err)
	}
	return "vowifi-" + hex.EncodeToString(value[:]), nil
}
