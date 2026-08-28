package main

import (
	"bytes"
	"context"
	"crypto/tls"
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
	"strings"
	"syscall"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/core"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providercontrol"
)

const (
	maximumConfigBytes = 64 << 10
	mediaLeaseTTL      = 12 * time.Hour
	shutdownTimeout    = 10 * time.Second
)

type config struct {
	Public struct {
		Listen  string `json:"listen"`
		TLSCert string `json:"tls_cert"`
		TLSKey  string `json:"tls_key"`
	} `json:"public"`
	Local struct {
		Listen string `json:"listen"`
		Token  string `json:"token"`
	} `json:"local"`
	AuthPath   string `json:"auth_path"`
	EventsPath string `json:"events_path"`
	TTLSeconds int    `json:"ttl_seconds"`
}

func main() {
	configPath := flag.String("config", "", "path to the 0600 mdd-core JSON configuration")
	flag.Parse()
	if strings.TrimSpace(*configPath) == "" {
		fatalf("-config is required")
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		fatalf("load config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, settings); err != nil {
		fatalf("run: %v", err)
	}
}

func loadConfig(path string) (config, error) {
	var settings config
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return settings, errors.New("configuration path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return settings, err
	}
	if !info.Mode().IsRegular() {
		return settings, errors.New("configuration must be a regular file")
	}
	if info.Size() < 1 || info.Size() > maximumConfigBytes {
		return settings, errors.New("configuration size is invalid")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return settings, fmt.Errorf("configuration permissions must be 0600, got %04o", info.Mode().Perm())
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return settings, err
	}
	if len(payload) == 0 || len(payload) > maximumConfigBytes {
		return settings, errors.New("configuration size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, fmt.Errorf("decode configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return settings, errors.New("configuration has trailing JSON")
	}
	if err := settings.validate(); err != nil {
		return settings, err
	}
	return settings, nil
}

func (settings *config) validate() error {
	settings.Public.Listen = strings.TrimSpace(settings.Public.Listen)
	settings.Local.Listen = strings.TrimSpace(settings.Local.Listen)
	settings.Local.Token = strings.TrimSpace(settings.Local.Token)
	for _, path := range []*string{&settings.Public.TLSCert, &settings.Public.TLSKey, &settings.AuthPath, &settings.EventsPath} {
		*path = strings.TrimSpace(*path)
		if !filepath.IsAbs(*path) {
			return errors.New("TLS, authentication and event paths must be absolute")
		}
	}
	if _, _, err := net.SplitHostPort(settings.Public.Listen); err != nil {
		return errors.New("public listen address must contain a valid port")
	}
	if !core.ValidateListenAddress(settings.Local.Listen) {
		return errors.New("local IPC listen address must use literal loopback or localhost")
	}
	if len(settings.Local.Token) < 32 {
		return errors.New("local IPC token must contain at least 32 bytes")
	}
	if settings.TTLSeconds == 0 {
		settings.TTLSeconds = 30
	}
	if settings.TTLSeconds < 1 || settings.TTLSeconds > 3600 {
		return errors.New("ttl_seconds must be between 1 and 3600")
	}
	return nil
}

func run(ctx context.Context, settings config) error {
	certificate, err := tls.LoadX509KeyPair(settings.Public.TLSCert, settings.Public.TLSKey)
	if err != nil {
		return fmt.Errorf("load TLS identity: %w", err)
	}
	auth, err := adminauth.NewManager(settings.AuthPath, true, nil)
	if err != nil {
		return fmt.Errorf("load administrator authentication: %w", err)
	}
	if len(auth.AgentToken()) < 32 {
		return errors.New("administrator authentication file has no valid Agent token")
	}
	replay, err := events.NewReplay(time.Duration(settings.TTLSeconds) * time.Second)
	if err != nil {
		return fmt.Errorf("create state replay: %w", err)
	}
	store, err := events.OpenBoltStore(settings.EventsPath, 5*time.Second)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.ReplayInto(replay); err != nil {
		return fmt.Errorf("replay event store: %w", err)
	}

	agents, err := agentlink.NewServer(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) {
		return auth.AgentToken(), nil
	}))
	if err != nil {
		return err
	}
	broker, err := agentlink.NewBrokerAPI(agents, settings.Local.Token, 30*time.Second)
	if err != nil {
		return err
	}
	providers, err := mediaauth.NewProviderDirectoryWithClock(time.Now, 30*time.Second)
	if err != nil {
		return err
	}
	registration, err := mediaauth.NewRegistrationHandler(providers, settings.Local.Token)
	if err != nil {
		return err
	}
	router, err := mediaauth.NewRouter(auth, providers, nil, 0)
	if err != nil {
		return err
	}
	media, err := mediaproxy.NewHandler(router, nil, 5*time.Second, 0)
	if err != nil {
		return err
	}
	leases, err := mediaauth.NewLeaseHandler(router, providers, auth, mediaLeaseTTL)
	if err != nil {
		return err
	}
	control, err := providercontrol.NewHandler(providers, nil)
	if err != nil {
		return err
	}
	authHandler, err := adminauth.NewHandler(auth)
	if err != nil {
		return err
	}
	publicHandler := core.NewServer(replay, nil,
		core.WithAdminAuth(authHandler),
		core.WithManagementAuth(auth.Middleware),
		core.WithBrowserControl(auth),
		core.WithAgentLink(agents),
		core.WithAgentFacts(agents),
		core.WithMediaLeases(leases),
		core.WithBrowserMedia(media),
		core.WithVoWiFiControl(control),
	)
	localMux := http.NewServeMux()
	localMux.Handle("/v1/agent/aka", broker)
	localMux.Handle("/v1/media/providers", registration)

	publicListener, err := net.Listen("tcp", settings.Public.Listen)
	if err != nil {
		return fmt.Errorf("listen public HTTPS: %w", err)
	}
	defer publicListener.Close()
	localListener, err := net.Listen("tcp", settings.Local.Listen)
	if err != nil {
		return errors.Join(fmt.Errorf("listen local IPC: %w", err), publicListener.Close())
	}
	defer localListener.Close()

	publicServer := &http.Server{
		Handler:           publicHandler,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	localServer := &http.Server{Handler: localMux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	errorsFromServers := make(chan error, 2)
	go serve(errorsFromServers, func() error { return publicServer.ServeTLS(publicListener, "", "") })
	go serve(errorsFromServers, func() error { return localServer.Serve(localListener) })

	var serveFailure error
	select {
	case <-ctx.Done():
	case serveFailure = <-errorsFromServers:
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	publicErr := publicServer.Shutdown(shutdownContext)
	localErr := localServer.Shutdown(shutdownContext)
	cancel()
	if serveFailure != nil {
		return errors.Join(serveFailure, publicErr, localErr)
	}
	return errors.Join(publicErr, localErr)
}

func serve(result chan<- error, function func() error) {
	err := function()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	result <- err
}

func fatalf(format string, arguments ...any) {
	log.Printf("mdd-core: "+format, arguments...)
	os.Exit(2)
}
