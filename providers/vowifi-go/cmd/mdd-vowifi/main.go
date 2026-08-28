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
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerfacts"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/agentaka"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/browsermedia"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/service"
)

const maximumConfigBytes = 64 << 10

type config providerconfig.Config

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
	return providerconfig.Config(settings).Validate()
}

func (settings config) upstream() service.UpstreamConfig {
	return service.UpstreamConfig{
		LineID: settings.LineID, DeviceID: settings.DeviceID, TraceID: settings.TraceID,
		Profile: identity.Profile{
			IMSI: settings.SIM.IMSI, MCC: settings.SIM.MCC, MNC: settings.SIM.MNC,
			IMEI: settings.SIM.IMEI, SMSC: settings.SIM.SMSC,
		},
		EPDGAddress: settings.Network.EPDGAddress, PCSCF: settings.Network.PCSCF,
		IMSAPN: settings.Network.IMSAPN, PDNFamily: settings.Network.PDNFamily,
		ProxyURL: settings.Network.ProxyURL,
		IMPI:     settings.IMS.IMPI, IMPU: settings.IMS.IMPU, IMSDomain: settings.IMS.Domain,
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
