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
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentusbip"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/allowance"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulardata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularevents"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmedia"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/core"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressprobe"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressprofiletest"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/euiccprofiles"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linebootstrap"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linedeletion"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/notifications"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/rawmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/runtimereconcile"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/scopedtoken"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systembackup"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systempreferences"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemupdate"
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
	AuthPath          string `json:"auth_path"`
	EventsPath        string `json:"events_path"`
	MessagesPath      string `json:"messages_path,omitempty"`
	CallsPath         string `json:"calls_path,omitempty"`
	CatalogPath       string `json:"catalog_path,omitempty"`
	EgressPath        string `json:"egress_path,omitempty"`
	AllowancePath     string `json:"allowance_path,omitempty"`
	NotificationsPath string `json:"notifications_path,omitempty"`
	PreferencesPath   string `json:"preferences_path,omitempty"`
	SingBoxPath       string `json:"sing_box_path,omitempty"`
	EgressTestPath    string `json:"egress_test_path,omitempty"`
	ProviderApply     struct {
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
	if len(os.Args) > 1 && os.Args[1] == "bootstrap-host" {
		if err := runBootstrapHost(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fatalf("bootstrap host: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "run-egress" {
		if err := runEgress(os.Args[2:]); err != nil {
			fatalf("run country exits: %v", err)
		}
		return
	}
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
	if len(os.Args) > 1 && os.Args[1] == "import-notifications" {
		if err := runNotificationsImport(os.Args[2:], os.Stdout); err != nil {
			fatalf("import legacy notifications: %v", err)
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
	if len(os.Args) > 1 && os.Args[1] == "preflight-release" {
		if err := runReleasePreflight(os.Args[2:], os.Stdout); err != nil {
			fatalf("preflight release: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "recover-release-install" {
		if err := runReleaseRecovery(os.Args[2:], os.Stdout); err != nil {
			fatalf("recover release installation: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "uninstall-release" {
		if err := runReleaseUninstall(os.Args[2:], os.Stdout); err != nil {
			fatalf("uninstall release: %v", err)
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
	if filepath.Clean(settings.NotificationsPath) == filepath.Clean(path) {
		return settings, errors.New("notification path must not be the Core configuration file")
	}
	if filepath.Clean(settings.PreferencesPath) == filepath.Clean(path) {
		return settings, errors.New("preference path must not be the Core configuration file")
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
	settings.CallsPath = strings.TrimSpace(settings.CallsPath)
	if settings.CallsPath == "" {
		settings.CallsPath = settings.EventsPath + ".calls"
	}
	if !filepath.IsAbs(settings.CallsPath) {
		return errors.New("call history path must be absolute")
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
	settings.AllowancePath = strings.TrimSpace(settings.AllowancePath)
	if settings.AllowancePath == "" {
		settings.AllowancePath = filepath.Join(filepath.Dir(settings.EventsPath), "allowance.db")
	}
	if !filepath.IsAbs(settings.AllowancePath) {
		return errors.New("allowance path must be absolute")
	}
	settings.NotificationsPath = strings.TrimSpace(settings.NotificationsPath)
	if settings.NotificationsPath == "" {
		settings.NotificationsPath = filepath.Join(filepath.Dir(settings.EventsPath), "notifications.db")
	}
	if !filepath.IsAbs(settings.NotificationsPath) {
		return errors.New("notification path must be absolute")
	}
	settings.PreferencesPath = strings.TrimSpace(settings.PreferencesPath)
	if settings.PreferencesPath == "" {
		settings.PreferencesPath = filepath.Join(filepath.Dir(settings.EventsPath), "preferences.db")
	}
	if !filepath.IsAbs(settings.PreferencesPath) {
		return errors.New("preference path must be absolute")
	}
	settings.SingBoxPath = filepath.Clean(strings.TrimSpace(settings.SingBoxPath))
	if settings.SingBoxPath == "." {
		settings.SingBoxPath = "/usr/local/bin/sing-box"
	}
	settings.EgressTestPath = filepath.Clean(strings.TrimSpace(settings.EgressTestPath))
	if settings.EgressTestPath == "." {
		settings.EgressTestPath = filepath.Join(filepath.Dir(settings.EgressPath), "egress-profile-tests")
	}
	if !filepath.IsAbs(settings.SingBoxPath) || !filepath.IsAbs(settings.EgressTestPath) ||
		settings.EgressTestPath == string(filepath.Separator) {
		return errors.New("egress profile test paths must be absolute and scoped")
	}
	allowancePath := filepath.Clean(settings.AllowancePath)
	notificationPath := filepath.Clean(settings.NotificationsPath)
	preferencePath := filepath.Clean(settings.PreferencesPath)
	for _, other := range []string{settings.EventsPath, settings.MessagesPath, settings.CallsPath,
		settings.CatalogPath, settings.EgressPath, settings.MessagesPath + ".cellular-operations",
		settings.AuthPath, settings.Public.TLSCert, settings.Public.TLSKey} {
		if allowancePath == filepath.Clean(other) {
			return errors.New("allowance path must not share another state database")
		}
		if notificationPath == filepath.Clean(other) || notificationPath == allowancePath {
			return errors.New("notification path must not share another state or credential file")
		}
		if preferencePath == filepath.Clean(other) || preferencePath == allowancePath || preferencePath == notificationPath {
			return errors.New("preference path must not share another state or credential file")
		}
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
	authOptions := []adminauth.ManagerOption{}
	if settings.ProviderApply.Enabled {
		persister, err := adminauth.NewRemoteCredentialPersister(settings.ProviderApply.SocketPath, settings.Local.Token)
		if err != nil {
			return fmt.Errorf("configure administrator credential persistence: %w", err)
		}
		authOptions = append(authOptions, adminauth.WithCredentialPersister(persister))
	}
	auth, err := adminauth.NewManager(settings.AuthPath, true, nil, authOptions...)
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
	eventLinePurger, err := events.NewLinePurger(store, replay)
	if err != nil {
		return err
	}
	messages, err := providermessages.OpenStore(settings.MessagesPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("open message store: %w", err)
	}
	defer messages.Close()
	calls, err := callhistory.Open(settings.CallsPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("open call history store: %w", err)
	}
	defer calls.Close()
	callAPI, err := callhistory.NewHandler(calls)
	if err != nil {
		return err
	}
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
	statusSampler, err := systemstatus.New(systemstatus.Config{
		Context: ctx, DataPath: filepath.Dir(settings.CatalogPath),
	})
	if err != nil {
		return fmt.Errorf("create system status sampler: %w", err)
	}
	statusAPI, err := systemstatus.NewHandler(statusSampler, time.Now)
	if err != nil {
		return err
	}
	statusSampler.Start()
	defer statusSampler.Close()
	preferenceStore, err := systempreferences.Open(settings.PreferencesPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("open system preference store: %w", err)
	}
	defer preferenceStore.Close()
	preferenceAPI, err := systempreferences.NewHandler(preferenceStore)
	if err != nil {
		return err
	}
	egressStore, err := egressconfig.Open(settings.EgressPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("open country exit store: %w", err)
	}
	defer egressStore.Close()
	allowanceStore, err := allowance.Open(settings.AllowancePath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("open allowance store: %w", err)
	}
	defer allowanceStore.Close()
	notificationStore, err := notifications.Open(settings.NotificationsPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("open notification store: %w", err)
	}
	defer notificationStore.Close()
	notificationEngine, err := notifications.NewEngine(notifications.EngineConfig{
		Context: ctx, Store: notificationStore,
		Sender: notifications.HTTPSender{EgressStatusPath: settings.ProviderApply.EgressStatusPath},
	})
	if err != nil {
		return err
	}
	notificationCoordinator, err := notifications.NewCoordinator(notifications.CoordinatorConfig{
		Context: ctx, Store: notificationStore, Engine: notificationEngine,
		SMS: messages, Calls: calls, SystemStatus: statusSampler, Catalog: catalog,
		Allowance: allowanceStore, Logf: log.Printf,
	})
	if err != nil {
		return err
	}
	if err := notificationEngine.BindVerifier(notificationCoordinator); err != nil {
		return err
	}
	notificationAPI, err := notifications.NewHandler(notificationStore, time.Now,
		func() { notificationEngine.ConfigChanged(); notificationCoordinator.Wake() }, notificationEngine.Wake)
	if err != nil {
		return err
	}
	defer notificationCoordinator.Close()
	imeiPoolAPI := linecatalog.NewIMEIPoolHandler(catalog)
	catalogSnapshot, err := linecatalog.NewSnapshotHandler(catalog, settings.Local.Token)
	if err != nil {
		return err
	}
	egressConfigAPI := egressconfig.NewHandler(egressStore)
	egressProfileTestAPI, err := egressprofiletest.NewHandler(egressStore, settings.SingBoxPath, settings.EgressTestPath)
	if err != nil {
		return err
	}
	egressSnapshot, err := egressconfig.NewSnapshotHandler(egressStore, settings.Local.Token)
	if err != nil {
		return err
	}

	var agentTokens agentlink.TokenResolver = auth
	agents, err := agentlink.NewServer(agentTokens)
	if err != nil {
		return err
	}
	rawAdmission, err := rawmodem.NewAdmission(catalog, time.Now)
	if err != nil {
		return err
	}
	if err := agents.SetModemRouteAdmission(rawAdmission); err != nil {
		return err
	}
	cellularEvents, err := cellularevents.New(catalog, agents, messages, calls)
	if err != nil {
		return err
	}
	if err := agents.SetModemEventSink(cellularEvents); err != nil {
		return err
	}
	lineBootstrap, err := linebootstrap.New(catalog, agents, time.Now)
	if err != nil {
		return err
	}
	lineBootstrapAPI, err := linebootstrap.NewHandler(lineBootstrap)
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
	agentUSBIP, err := agentusbip.NewBroker(agentTokens, nil, 0)
	if err != nil {
		return err
	}
	rawModems, err := rawmodem.New(rawmodem.Config{
		Context: ctx, Catalog: catalog, Agents: agents, Broker: agentUSBIP, Logf: log.Printf,
	})
	if err != nil {
		return err
	}
	defer rawModems.Close()
	rawModemAPI, err := rawmodem.NewHandler(catalog, agents, rawModems.Wake, time.Now)
	if err != nil {
		return err
	}
	cellularData, err := cellulardata.New(cellulardata.Config{Context: ctx, Catalog: catalog, Agents: agents, Broker: agentData})
	if err != nil {
		return err
	}
	defer cellularData.Close()
	egressDataToken, err := scopedtoken.Ensure(filepath.Join(filepath.Dir(settings.EventsPath), "egress-ipc-token"))
	if err != nil {
		return fmt.Errorf("initialize cellular egress IPC token: %w", err)
	}
	cellularDataIPC, err := cellulardata.NewInternalHandler(cellularData, egressDataToken)
	if err != nil {
		return err
	}
	cellularMedia, err := cellularmedia.New(cellularmedia.Config{
		Context: ctx, Auth: auth, Catalog: catalog, Agents: agents, Broker: agentMedia, Calls: calls, Incoming: calls,
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
	facts, err := providerfacts.NewHandler(providers, store, replay, settings.Local.Token, calls)
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
	control, err := providercontrol.NewHandler(providers, catalog, nil,
		providercontrol.WithCallRecorder(calls), providercontrol.WithCardRouteResolver(agents))
	if err != nil {
		return err
	}
	allowanceService, err := allowance.New(allowanceStore, catalog, messages,
		allowance.RouteVerifierFunc(func(ctx context.Context, transport, lineID, cardID string) error {
			switch transport {
			case "vowifi":
				return control.VerifyMessageRoute(ctx, lineID, cardID)
			case "cellular":
				return cellularSMS.VerifyMessageRoute(lineID, cardID)
			default:
				return errors.New("unknown allowance message transport")
			}
		}))
	if err != nil {
		return err
	}
	allowanceAPI, err := allowance.NewHandler(allowanceService)
	if err != nil {
		return err
	}
	if err := control.BindAllowanceDispatchAuthorizer(allowanceService); err != nil {
		return err
	}
	if err := cellularSMS.BindAllowanceDispatchAuthorizer(allowanceService); err != nil {
		return err
	}
	runtimeReconciler, err := runtimereconcile.New(runtimereconcile.Config{
		Context: ctx, Catalog: catalog, Agents: agents, Runtime: control,
		Store: store, Replay: replay, Logf: log.Printf,
	})
	if err != nil {
		return err
	}
	if err := control.BindRuntimeIntentRequester(runtimeReconciler); err != nil {
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
	lifecycleGuard := linecatalog.LifecycleGuardFunc(func(lineID string) (bool, error) {
		if _, current := providers.CurrentGeneration(lineID); current {
			return true, nil
		}
		if active, err := rawModemAPI.ActiveLine(lineID); err != nil || active {
			return active, err
		}
		if active, err := calls.ActiveLine(lineID); err != nil || active {
			return active, err
		}
		if active, err := router.ActiveLine(lineID); err != nil || active {
			return active, err
		}
		if active, err := allowanceStore.ActiveLine(lineID); err != nil || active {
			return active, err
		}
		if active, err := notificationStore.ActiveLine(lineID); err != nil || active {
			return active, err
		}
		return cellularData.ActiveLine(lineID)
	})
	catalogAPI := linecatalog.NewHandler(catalog, lifecycleGuard)
	lineDeletionAPI, err := linedeletion.NewHandler(linedeletion.Config{
		Catalog: catalog, Guard: lifecycleGuard, Notifications: notificationStore,
		Events: eventLinePurger, Allowance: allowanceStore, Messages: messages,
		SMSOperations: cellularSMSOperations, Calls: calls,
	})
	if err != nil {
		return err
	}
	operationAPI := linecatalog.NewOperationHandler(catalog)
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
	authHandler, err := adminauth.NewHandler(auth, adminauth.WithAgentCredentialInvalidator(func(agentID string) {
		agents.DisconnectAgent(agentID)
		agentMedia.DisconnectAgent(agentID)
		agentData.DisconnectAgent(agentID)
		agentUSBIP.DisconnectAgent(agentID)
	}))
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
	var systemMaintenanceAPI http.Handler
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
		baseURL, err := providerCoreAddress(settings.Local.Listen)
		if err != nil {
			return err
		}
		systemMaintenanceAPI, err = core.NewSystemMaintenanceHandler(coreMaintenanceClient{baseURL: baseURL, token: settings.Local.Token, client: &http.Client{Transport: &http.Transport{Proxy: nil}}})
		if err != nil {
			return err
		}
	}
	simPINAPI, err := core.NewSIMPINHandler(agents, catalog)
	if err != nil {
		return err
	}
	modemRecoveryAPI, err := core.NewModemRecoveryHandler(agents, catalog)
	if err != nil {
		fatalf("initialize modem recovery API: %v", err)
	}
	provisionAPI, err := core.NewProvisionHandler(agents, catalog)
	if err != nil {
		return err
	}
	reprovisionAPI, err := core.NewReprovisionHandler(agents, catalog)
	if err != nil {
		return err
	}
	provisionReconcileAPI, err := core.NewProvisionReconcileHandler(agents, catalog)
	if err != nil {
		return err
	}
	provisionReadbackAPI, err := core.NewProvisionReadbackHandler(agents, catalog)
	if err != nil {
		return err
	}
	readerReadbackAPI, err := core.NewReaderReadbackHandler(agents, catalog)
	if err != nil {
		return err
	}
	readerProvisionAPI, err := core.NewReaderProvisionHandler(agents, catalog)
	if err != nil {
		return err
	}
	backupAPI, err := systembackup.NewHandler([]systembackup.Source{
		{Name: "events.db", Path: settings.EventsPath}, {Name: "messages.db", Path: settings.MessagesPath},
		{Name: "calls.db", Path: settings.CallsPath}, {Name: "catalog.json", Read: func() ([]byte, error) {
			snapshot, snapshotErr := catalog.SnapshotIncludingDeleted()
			if snapshotErr != nil {
				return nil, snapshotErr
			}
			for lineIndex := range snapshot.Lines {
				for profileIndex := range snapshot.Lines[lineIndex].Network.APNProfiles {
					snapshot.Lines[lineIndex].Network.APNProfiles[profileIndex].Password = ""
				}
			}
			return json.Marshal(snapshot)
		}},
		{Name: "egress.db", Path: settings.EgressPath}, {Name: "allowance.db", Path: settings.AllowancePath},
		{Name: "notifications.db", Path: settings.NotificationsPath}, {Name: "preferences.db", Path: settings.PreferencesPath},
	}, time.Now)
	if err != nil {
		return err
	}
	repository := strings.TrimSpace(os.Getenv("MDD_UPDATE_REPOSITORY"))
	if repository == "" {
		repository = "lovitus/mdd-sim-gateway"
	}
	updateChecker, err := systemupdate.NewChecker(repository, runtimeInfo.BuildVersion, nil)
	if err != nil {
		return err
	}
	updateStore, err := systemupdate.Open(filepath.Join(filepath.Dir(settings.EventsPath), "update-state"))
	if err != nil {
		return err
	}
	updateAPI, err := systemupdate.NewHandler(updateChecker, updateStore)
	if err != nil {
		return err
	}
	publicHandler := core.NewServer(replay, nil,
		core.WithWebUI(ui),
		core.WithAdminAuth(authHandler),
		core.WithManagementAuth(auth.Middleware),
		core.WithBrowserControl(auth),
		core.WithAgentLink(agents),
		core.WithSIMPIN(simPINAPI),
		core.WithModemRecovery(modemRecoveryAPI),
		core.WithProvision(provisionAPI),
		core.WithReprovision(reprovisionAPI),
		core.WithProvisionReconcile(provisionReconcileAPI),
		core.WithProvisionReadback(provisionReadbackAPI),
		core.WithReaderReadback(readerReadbackAPI),
		core.WithReaderProvision(readerProvisionAPI),
		core.WithSystemBackup(backupAPI),
		core.WithSystemMaintenance(systemMaintenanceAPI),
		core.WithSystemUpdate(updateAPI),
		core.WithAgentMedia(agentMedia),
		core.WithAgentData(agentData),
		core.WithAgentUSBIP(agentUSBIP),
		core.WithCellularData(cellularData),
		core.WithRawModem(rawModemAPI),
		core.WithCellularMedia(cellularMedia),
		core.WithAgentFacts(agents),
		core.WithModemPolicies(agents),
		core.WithProviderFacts(providers),
		core.WithRuntimeInfo(runtimeInfo),
		core.WithSystemStatus(statusAPI),
		core.WithSystemPreferences(preferenceAPI),
		core.WithNotifications(notificationAPI),
		core.WithMediaLeases(leases),
		core.WithBrowserMedia(media),
		core.WithVoWiFiControl(control),
		core.WithMessages(messages, messageAPI),
		core.WithCallHistory(callAPI),
		core.WithCellularMessages(cellularSMS),
		core.WithAllowance(allowanceAPI),
		core.WithEUICCProfiles(euiccProfiles),
		core.WithLineCatalog(catalog, catalogAPI),
		core.WithLineDiagnostics(store),
		core.WithLineDeletion(lineDeletionAPI),
		core.WithOperationStatus(operationAPI),
		core.WithIMEIPool(imeiPoolAPI),
		core.WithLineBootstrap(lineBootstrapAPI),
		core.WithProviderApply(providerApplyAPI),
		core.WithEgressProbe(egressProbeAPI),
		core.WithEgressProfileTest(egressProfileTestAPI),
		core.WithEgressConfig(egressConfigAPI, egressApplyAPI),
	)
	localMux := http.NewServeMux()
	localMux.Handle(linecatalog.SnapshotIPCPath, catalogSnapshot)
	localMux.Handle(egressconfig.SnapshotIPCPath, egressSnapshot)
	localMux.Handle(cellulardata.InternalPath, cellularDataIPC)
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
	rawModems.Start()
	notificationCoordinator.Start()
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

func runNotificationsImport(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("import-notifications", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to the 0600 mdd-core JSON configuration")
	sourcePath := flags.String("source", "", "path to the private legacy config.yaml")
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
	legacy, err := notifications.ReadLegacy(*sourcePath, time.Now().UTC())
	if err != nil {
		return err
	}
	store, err := notifications.Open(settings.NotificationsPath, 5*time.Second)
	if err != nil {
		return err
	}
	defer store.Close()
	config, imported, err := store.ImportLegacy(legacy)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{
		"status": "imported", "created": imported, "revision": config.Revision,
		"source_sha256": legacy.Proof.SourceSHA256, "warnings": legacy.Proof.Warnings,
		"channels": map[string]bool{
			"webhook": config.Webhook.Enabled, "telegram": config.Telegram.Enabled, "pushplus": config.PushPlus.Enabled,
		},
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
