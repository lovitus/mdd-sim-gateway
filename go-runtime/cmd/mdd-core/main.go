package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
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
	"strings"
	"syscall"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulardata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/core"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressprobe"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/euiccprofiles"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/runtimereconcile"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/webui"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providercontrol"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerfacts"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
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
	AuthPath      string `json:"auth_path"`
	EventsPath    string `json:"events_path"`
	MessagesPath  string `json:"messages_path,omitempty"`
	CatalogPath   string `json:"catalog_path,omitempty"`
	EgressPath    string `json:"egress_path,omitempty"`
	ProviderApply struct {
		Enabled           bool   `json:"enabled,omitempty"`
		SocketPath        string `json:"socket_path,omitempty"`
		CandidateRoot     string `json:"candidate_root,omitempty"`
		StatePath         string `json:"state_path,omitempty"`
		EgressStatusPath  string `json:"egress_status_path,omitempty"`
		EgressDesiredPath string `json:"egress_desired_path,omitempty"`
		CurrentLink       string `json:"current_link,omitempty"`
		ReceiptPath       string `json:"receipt_path,omitempty"`
		ProviderBinary    string `json:"provider_binary,omitempty"`
		ProviderUser      string `json:"provider_user,omitempty"`
		SystemctlPath     string `json:"systemctl_path,omitempty"`
	} `json:"provider_apply,omitempty"`
	TTLSeconds int `json:"ttl_seconds"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "import-legacy" {
		if err := runLegacyImport(os.Args[2:], os.Stdout); err != nil {
			fatalf("import legacy configuration: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "import-egress" {
		if err := runEgressImport(os.Args[2:], os.Stdout); err != nil {
			fatalf("import legacy country exits: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "render-provider-configs" {
		if err := runProviderRender(os.Args[2:], os.Stdout); err != nil {
			fatalf("render provider configurations: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "plan-provider-apply" {
		if err := runProviderPlan(os.Args[2:], os.Stdout); err != nil {
			fatalf("plan provider apply: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "apply-provider-configs" {
		if err := runProviderApply(os.Args[2:], os.Stdout); err != nil {
			fatalf("apply provider configurations: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "provider-apply-helper" {
		if err := runProviderApplyHelper(os.Args[2:]); err != nil {
			fatalf("run provider apply helper: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "install-release" {
		if err := runReleaseInstall(os.Args[2:], os.Stdout); err != nil {
			fatalf("install release: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "recover-release-install" {
		if err := runReleaseRecovery(os.Args[2:], os.Stdout); err != nil {
			fatalf("recover release installation: %v", err)
		}
		return
	}
	flags := flag.NewFlagSet("mdd-core", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to the 0600 mdd-core JSON configuration")
	if err := flags.Parse(os.Args[1:]); err != nil {
		fatalf("parse flags: %v", err)
	}
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
	settings.MessagesPath = strings.TrimSpace(settings.MessagesPath)
	if settings.MessagesPath == "" {
		settings.MessagesPath = settings.EventsPath + ".messages"
	}
	if !filepath.IsAbs(settings.MessagesPath) {
		return errors.New("message path must be absolute")
	}
	settings.CatalogPath = strings.TrimSpace(settings.CatalogPath)
	if settings.CatalogPath == "" {
		settings.CatalogPath = settings.EventsPath + ".lines"
	}
	if !filepath.IsAbs(settings.CatalogPath) {
		return errors.New("catalog path must be absolute")
	}
	settings.EgressPath = strings.TrimSpace(settings.EgressPath)
	if settings.EgressPath == "" {
		settings.EgressPath = settings.EventsPath + ".egress"
	}
	if !filepath.IsAbs(settings.EgressPath) {
		return errors.New("country exit configuration path must be absolute")
	}
	if settings.ProviderApply.Enabled {
		settings.ProviderApply.ProviderUser = strings.TrimSpace(settings.ProviderApply.ProviderUser)
		if settings.ProviderApply.ProviderUser == "" {
			settings.ProviderApply.ProviderUser = "mdd"
		}
		settings.ProviderApply.SystemctlPath = strings.TrimSpace(settings.ProviderApply.SystemctlPath)
		if settings.ProviderApply.SystemctlPath == "" {
			settings.ProviderApply.SystemctlPath = "/bin/systemctl"
		}
		paths := []*string{
			&settings.ProviderApply.SocketPath, &settings.ProviderApply.CandidateRoot,
			&settings.ProviderApply.StatePath, &settings.ProviderApply.EgressStatusPath,
			&settings.ProviderApply.EgressDesiredPath,
			&settings.ProviderApply.CurrentLink, &settings.ProviderApply.ReceiptPath,
			&settings.ProviderApply.ProviderBinary, &settings.ProviderApply.SystemctlPath,
		}
		for _, path := range paths {
			*path = filepath.Clean(strings.TrimSpace(*path))
			if !filepath.IsAbs(*path) || *path == string(filepath.Separator) {
				return errors.New("provider apply paths must be absolute and scoped")
			}
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
	messages, err := providermessages.OpenStore(settings.MessagesPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("open message store: %w", err)
	}
	defer messages.Close()
	cellularSMSOperations, err := cellularmessages.OpenOperationStore(settings.MessagesPath+".cellular-operations", 5*time.Second)
	if err != nil {
		return fmt.Errorf("open cellular SMS operation store: %w", err)
	}
	defer cellularSMSOperations.Close()
	catalog, err := linecatalog.Open(settings.CatalogPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("open line catalog: %w", err)
	}
	defer catalog.Close()
	egressStore, err := egressconfig.Open(settings.EgressPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("open country exit store: %w", err)
	}
	defer egressStore.Close()
	catalogAPI := linecatalog.NewHandler(catalog)
	catalogSnapshot, err := linecatalog.NewSnapshotHandler(catalog, settings.Local.Token)
	if err != nil {
		return err
	}
	egressConfigAPI := egressconfig.NewHandler(egressStore)
	egressSnapshot, err := egressconfig.NewSnapshotHandler(egressStore, settings.Local.Token)
	if err != nil {
		return err
	}

	agentTokens := agentlink.TokenResolverFunc(func(context.Context, string) (string, error) {
		return auth.AgentToken(), nil
	})
	agents, err := agentlink.NewServer(agentTokens)
	if err != nil {
		return err
	}
	agentMedia, err := agentmedia.NewBroker(agentTokens, nil, 0)
	if err != nil {
		return err
	}
	agentData, err := agentdata.NewBroker(agentTokens, nil)
	if err != nil {
		return err
	}
	cellularData, err := cellulardata.New(cellulardata.Config{Context: ctx, Catalog: catalog, Agents: agents, Broker: agentData})
	if err != nil {
		return err
	}
	defer cellularData.Close()
	cellularMedia, err := cellularmedia.New(cellularmedia.Config{
		Context: ctx, Auth: auth, Catalog: catalog, Agents: agents, Broker: agentMedia,
	})
	if err != nil {
		return err
	}
	defer cellularMedia.Close()
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
	facts, err := providerfacts.NewHandler(providers, store, replay, settings.Local.Token)
	if err != nil {
		return err
	}
	messageIngress, err := providermessages.NewHandler(providers, messages, settings.Local.Token)
	if err != nil {
		return err
	}
	messageAPI, err := providermessages.NewPublicHandler(messages)
	if err != nil {
		return err
	}
	cellularSMS, err := cellularmessages.New(catalog, agents, messages, cellularSMSOperations)
	if err != nil {
		return err
	}
	control, err := providercontrol.NewHandler(providers, catalog, nil, providercontrol.WithRuntimeIntent(catalog))
	if err != nil {
		return err
	}
	runtimeReconciler, err := runtimereconcile.New(runtimereconcile.Config{
		Context: ctx, Catalog: catalog, Agents: agents, Runtime: control,
		Store: store, Replay: replay, Logf: log.Printf,
	})
	if err != nil {
		return err
	}
	defer runtimeReconciler.Close()
	euiccProfiles, err := euiccprofiles.New(agents, euiccprofiles.WithDownloadSafety(catalog, control))
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
	applyPreflight, err := providerapply.NewHandler(catalog, providers, settings.Local.Token, nil)
	if err != nil {
		return err
	}
	authHandler, err := adminauth.NewHandler(auth)
	if err != nil {
		return err
	}
	ui, err := webui.New()
	if err != nil {
		return fmt.Errorf("load embedded WebUI: %w", err)
	}
	runtimeInfo := core.RuntimeInfoForBuild()
	runtimeInfo.StateTTL = settings.TTLSeconds
	runtimeInfo.Public.Listen = settings.Public.Listen
	if len(certificate.Certificate) == 0 {
		return errors.New("TLS identity contains no certificate")
	}
	fingerprint := sha256.Sum256(certificate.Certificate[0])
	runtimeInfo.Public.TLSFingerprintSHA256 = hex.EncodeToString(fingerprint[:])
	var providerApplyAPI http.Handler
	var egressProbeAPI http.Handler
	var egressApplyAPI http.Handler
	if settings.ProviderApply.Enabled {
		client, err := provideradmin.NewClient(settings.ProviderApply.SocketPath, settings.Local.Token)
		if err != nil {
			return err
		}
		providerApplyAPI, err = provideradmin.NewHandler(client)
		if err != nil {
			return err
		}
		egressProbeAPI, err = egressprobe.NewHandler(settings.ProviderApply.EgressStatusPath, 8*time.Second)
		if err != nil {
			return err
		}
		egressClient, err := egressconfig.NewApplyClient(settings.ProviderApply.SocketPath, settings.Local.Token)
		if err != nil {
			return err
		}
		egressApplyAPI, err = egressconfig.NewApplyHandler(egressClient)
		if err != nil {
			return err
		}
	}
	publicHandler := core.NewServer(replay, nil,
		core.WithWebUI(ui),
		core.WithAdminAuth(authHandler),
		core.WithManagementAuth(auth.Middleware),
		core.WithBrowserControl(auth),
		core.WithAgentLink(agents),
		core.WithAgentMedia(agentMedia),
		core.WithAgentData(agentData),
		core.WithCellularData(cellularData),
		core.WithCellularMedia(cellularMedia),
		core.WithAgentFacts(agents),
		core.WithProviderFacts(providers),
		core.WithRuntimeInfo(runtimeInfo),
		core.WithMediaLeases(leases),
		core.WithBrowserMedia(media),
		core.WithVoWiFiControl(control),
		core.WithMessages(messages, messageAPI),
		core.WithCellularMessages(cellularSMS),
		core.WithEUICCProfiles(euiccProfiles),
		core.WithLineCatalog(catalog, catalogAPI),
		core.WithProviderApply(providerApplyAPI),
		core.WithEgressProbe(egressProbeAPI),
		core.WithEgressConfig(egressConfigAPI, egressApplyAPI),
	)
	localMux := http.NewServeMux()
	localMux.Handle(linecatalog.SnapshotIPCPath, catalogSnapshot)
	localMux.Handle(egressconfig.SnapshotIPCPath, egressSnapshot)
	localMux.Handle("/v1/agent/aka", broker)
	localMux.Handle(providerapply.Path, applyPreflight)
	localMux.Handle(providerapply.DrainPath, applyPreflight)
	localMux.Handle(providerapply.ResumePath, applyPreflight)
	localMux.Handle("/v1/media/providers", registration)
	localMux.Handle("/v1/provider/facts", facts)
	localMux.Handle("/v1/provider/messages", messageIngress)

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
	runtimeReconciler.Start()
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

func runLegacyImport(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("import-legacy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to the 0600 mdd-core JSON configuration")
	sourcePath := flags.String("source", "", "path to the legacy config.yaml")
	egressDesiredPath := flags.String("egress-desired", "", "path to the legacy orchestrator desired.json")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*sourcePath) == "" ||
		strings.TrimSpace(*egressDesiredPath) == "" || flags.NArg() != 0 {
		return errors.New("-config, -source, and -egress-desired are required")
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	lines, receipt, err := linecatalog.ReadLegacy(*sourcePath)
	if err != nil {
		return err
	}
	lines, receipt.EgressSourceSHA256, err = linecatalog.ApplyLegacyDesiredEgress(lines, *egressDesiredPath)
	if err != nil {
		return err
	}
	catalog, err := linecatalog.Open(settings.CatalogPath, 5*time.Second)
	if err != nil {
		return err
	}
	defer catalog.Close()
	if err := catalog.ImportEmpty(lines, receipt); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{
		"status": "imported", "lines": receipt.LineCount, "source_sha256": receipt.SourceSHA256,
	})
}

func runEgressImport(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("import-egress", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to the 0600 mdd-core JSON configuration")
	sourcePath := flags.String("source", "", "path to the legacy orchestrator desired.json")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*sourcePath) == "" || flags.NArg() != 0 {
		return errors.New("-config and -source are required")
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	desired, receipt, err := egressconfig.ReadLegacy(*sourcePath)
	if err != nil {
		return err
	}
	store, err := egressconfig.Open(settings.EgressPath, 5*time.Second)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.ImportEmpty(desired, receipt); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{
		"status": "imported", "profiles": len(desired.Profiles), "exits": len(desired.Exits),
		"source_sha256": receipt.SourceSHA256,
	})
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
