package cellulardata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const internalTestToken = "0123456789abcdef0123456789abcdef"

type fakeAgents struct {
	stopErr  error
	prepare  func()
	mu       sync.Mutex
	requests []agentlink.ModemDataRequest
	manager  *agentdata.Manager
}

func (fake *fakeAgents) ResolveModemDataTargetForCard(cardID string) (agentlink.ModemTarget, error) {
	return agentlink.ModemTarget{AgentID: "agent-a", ProcessGeneration: "process-a", AttachmentID: "attachment-a", EquipmentID: "862547055201716", CardID: cardID}, nil
}
func (fake *fakeAgents) ResolveRenewableModemDataTargetForCard(cardID string) (agentlink.ModemTarget, error) {
	return fake.ResolveModemDataTargetForCard(cardID)
}
func (fake *fakeAgents) ExecuteModemData(_ context.Context, _, _ string, request agentlink.ModemDataRequest) (agentlink.ModemDataResponse, error) {
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	stopErr := fake.stopErr
	fake.mu.Unlock()
	if request.Action == agentlink.ModemDataPrepare && fake.prepare != nil {
		fake.prepare()
	}
	if request.Action == agentlink.ModemDataStop && stopErr != nil {
		return agentlink.ModemDataResponse{}, stopErr
	}
	if fake.manager != nil {
		response := fake.manager.ExecuteModemData(context.Background(), request)
		if response.Failure != nil {
			return response, response.Failure
		}
		return response, nil
	}
	state, profile := "stopped", ""
	var expiresAt *time.Time
	if request.Action == agentlink.ModemDataPrepare {
		state, profile = "ready", "profile-a"
		if request.Purpose != "" {
			expiresAt = &request.ExpiresAt
		}
	} else if request.Action == agentlink.ModemDataRenew {
		state, expiresAt = "ready", &request.ExpiresAt
	}
	return agentlink.ModemDataResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, SIMSessionGeneration: request.SIMSessionGeneration,
		SessionID: request.SessionID, StreamID: request.StreamID, State: state, Profile: profile, ExpiresAt: expiresAt}, nil
}

func TestPreparingDonorCannotRenewOrSettleBeforePrepareCompletes(t *testing.T) {
	catalog, err := linecatalog.Open(t.TempDir()+"/catalog.db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	const card = "8985200000000000001"
	_, err = catalog.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-a", Name: "Data SIM", Enabled: true, CardID: card,
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: "862547055201716"}})
	if err != nil {
		t.Fatal(err)
	}
	budgets, err := egressconfig.Open(t.TempDir()+"/egress.db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer budgets.Close()
	broker, err := agentdata.NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return internalTestToken, nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	agents := &fakeAgents{stopErr: io.ErrUnexpectedEOF, prepare: func() { close(started); <-release }}
	service, err := New(Config{Context: context.Background(), Catalog: catalog, Agents: agents, Broker: broker, Budgets: budgets})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	defer unblock()
	created := make(chan error, 1)
	go func() {
		_, err := service.create(context.Background(), "line-a", "egress:one", "", time.Minute, 4096)
		created <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("prepare did not reach Agent boundary")
	}
	renewed := make(chan error, 1)
	go func() {
		_, err := service.EnsureOwned(context.Background(), "line-a", "egress:one", "", time.Now().Add(time.Minute), 4096, "renew-during-prepare")
		renewed <- err
	}()
	select {
	case err := <-renewed:
		if err == nil {
			t.Fatal("preparing owner renewed before preparation completed")
		}
	case <-time.After(time.Second):
		t.Fatal("renew was admitted while the owner was still preparing")
	}
	view, ok := service.Owned("line-a", "egress:one")
	if !ok || view.State != "preparing" || view.Password != "" || view.ListenPort != 0 {
		t.Fatal("preparing owner became usable", view)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- service.StopOwned("line-a", view.SessionID, "egress:one") }()
	select {
	case err := <-stopped:
		t.Fatal("stop raced ahead of the pending Prepare", err)
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case <-created:
	case <-time.After(2 * time.Second):
		t.Fatal("prepare did not finish")
	}
	select {
	case err := <-stopped:
		if err == nil {
			t.Fatal("unknown stop was reported as settled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not finish")
	}
	b, err := budgets.DonorBudget(card)
	if err != nil || b.Grant == nil || b.Grant.SessionID != view.SessionID {
		t.Fatal("unknown stop lost the pending reservation", err)
	}
	agents.mu.Lock()
	agents.stopErr = nil
	for _, request := range agents.requests {
		if request.Action == agentlink.ModemDataRenew {
			t.Error("renew reached Agent during Prepare")
		}
	}
	agents.mu.Unlock()
}

type dialBackend struct {
	mu          sync.Mutex
	stops       int
	lastAddress string
}

type passDataCoordinator struct{}
type passDataAdmission struct{}

func (passDataCoordinator) DoAuxiliary(ctx context.Context, _ string, callback func(context.Context) error) error {
	return callback(ctx)
}

func (passDataAdmission) ResolveDataProfile(_ context.Context, _ agentdata.Target, requested, _, _ string) (agentdata.Profile, error) {
	if requested == "" {
		requested = "profile-e2e"
	}
	return agentdata.Profile{Name: requested, AllowRoaming: true}, nil
}
func (passDataAdmission) ValidateDataTarget(context.Context, agentdata.Target) error { return nil }
func (passDataAdmission) DataPrepared(agentdata.Target, string, string)              {}
func (passDataAdmission) DataCleanup(agentdata.Target, string, string)               {}
func (passDataAdmission) DataReleased(agentdata.Target, string)                      {}

func (*dialBackend) PrepareData(_ context.Context, _ agentdata.Target, profile agentdata.Profile) (string, error) {
	if profile.Name != "" {
		return profile.Name, nil
	}
	return "profile-e2e", nil
}
func (backend *dialBackend) DialData(ctx context.Context, _ agentdata.Target, network, address string) (net.Conn, error) {
	backend.mu.Lock()
	backend.lastAddress = address
	backend.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
func (backend *dialBackend) StopData(context.Context, agentdata.Target) error {
	backend.mu.Lock()
	backend.stops++
	backend.mu.Unlock()
	return nil
}

func TestExplicitSessionIsMemoryOnlyAndRevocable(t *testing.T) {
	catalog, err := linecatalog.Open(t.TempDir()+"/catalog.db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	_, err = catalog.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-a", Name: "Data SIM", Enabled: true, CardID: "8985200000000000001",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: "862547055201716"}})
	if err != nil {
		t.Fatal(err)
	}
	tokens := agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return "0123456789abcdef0123456789abcdef", nil })
	broker, err := agentdata.NewBroker(tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	agents := &fakeAgents{}
	budgets, err := egressconfig.Open(t.TempDir()+"/egress.db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer budgets.Close()
	service, err := New(Config{Context: context.Background(), Catalog: catalog, Agents: agents, Broker: broker, Budgets: budgets, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/lines/line-a/cellular/data/sessions",
		strings.NewReader(`{"ttl_seconds":60,"max_bytes":1048576}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create HTTP %d: %s", create.Code, create.Body.String())
	}
	var created sessionView
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	current := service.byID[created.SessionID]
	service.mu.Unlock()
	if current == nil {
		t.Fatal("created session was not registered")
	}
	if current.profile != "profile-a" || current.port == 0 || current.username == "" || current.password == "" {
		t.Fatalf("session=%+v", current.view(true))
	}
	status := httptest.NewRecorder()
	service.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v1/lines/line-a/cellular/data/sessions", nil))
	var public sessionView
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &public) != nil || public.Username != "" || public.Password != "" {
		t.Fatalf("status HTTP %d exposed invalid state: %s", status.Code, status.Body.String())
	}
	agents.mu.Lock()
	agents.stopErr = io.ErrUnexpectedEOF
	agents.mu.Unlock()
	unknown := httptest.NewRecorder()
	service.ServeHTTP(unknown, httptest.NewRequest(http.MethodDelete, "/v1/lines/line-a/cellular/data/sessions/"+created.SessionID, nil))
	budget, budgetErr := budgets.DonorBudget(current.target.CardID)
	active, activeErr := service.ActiveLine("line-a")
	if unknown.Code != http.StatusConflict || !active || activeErr != nil || budgetErr != nil || budget.Grant == nil || current.ctx.Err() == nil {
		t.Fatalf("unknown stop released owner: HTTP %d active=%v activeErr=%v budget=%+v err=%v", unknown.Code, active, activeErr, budget, budgetErr)
	}
	reset := httptest.NewRecorder()
	service.ServeHTTP(reset, httptest.NewRequest(http.MethodPut, "/v1/lines/line-a/cellular/data/budget", strings.NewReader(
		`{"expected_card_id":"8985200000000000001","revision":1,"limit_bytes":4096,"reset":true,"confirm_reset":true}`)))
	if reset.Code != http.StatusConflict || !strings.Contains(reset.Body.String(), "donor_budget_owner_unconfirmed") {
		t.Fatal("reset bypassed unknown owner", reset.Body.String())
	}
	if _, err := service.create(context.Background(), "line-a", "egress:renamed", "", time.Minute, 4096); err == nil {
		t.Fatal("profile change duplicated donor allowance")
	}
	agents.mu.Lock()
	agents.stopErr = nil
	agents.mu.Unlock()
	stop := httptest.NewRecorder()
	service.ServeHTTP(stop, httptest.NewRequest(http.MethodDelete,
		"/v1/lines/line-a/cellular/data/sessions/"+created.SessionID, nil))
	if stop.Code != http.StatusNoContent {
		t.Fatalf("stop HTTP %d: %s", stop.Code, stop.Body.String())
	}
	service.mu.Lock()
	remaining := len(service.byID)
	service.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("remaining sessions=%d", remaining)
	}
	agents.mu.Lock()
	requests := append([]agentlink.ModemDataRequest(nil), agents.requests...)
	agents.mu.Unlock()
	if len(requests) != 3 || requests[0].Action != agentlink.ModemDataPrepare || requests[1].Action != agentlink.ModemDataStop || requests[2].Action != agentlink.ModemDataStop {
		t.Fatalf("requests=%+v", requests)
	}
	budget, budgetErr = budgets.DonorBudget(current.target.CardID)
	if budgetErr != nil || budget.Grant != nil {
		t.Fatal("confirmed stop did not settle reservation", budgetErr)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSOCKSConnectTraversesCoreBrokerAndAgentManager(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		conn, acceptErr := echo.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	tokens := agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return "0123456789abcdef0123456789abcdef", nil })
	broker, err := agentdata.NewBroker(tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	dataServer := httptest.NewServer(broker)
	defer dataServer.Close()
	backend := &dialBackend{}
	manager, err := agentdata.NewManager(agentdata.Config{Context: context.Background(), ServerURL: dataServer.URL,
		ServerToken: "0123456789abcdef0123456789abcdef", AgentID: "agent-a", ProcessGeneration: "process-a",
		HTTPClient: &http.Client{Timeout: 5 * time.Second}, Backend: backend,
		Coordinator: passDataCoordinator{}, Admission: passDataAdmission{}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	catalog, err := linecatalog.Open(t.TempDir()+"/catalog.db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	_, err = catalog.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-a", Name: "Data SIM", Enabled: true, CardID: "8985200000000000001",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: "862547055201716"}})
	if err != nil {
		t.Fatal(err)
	}
	agents := &fakeAgents{manager: manager}
	budgets, err := egressconfig.Open(t.TempDir()+"/egress.db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer budgets.Close()
	if _, err := budgets.ConfigureDonor("8985200000000000001", 1, 4096, false); err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{Context: context.Background(), Catalog: catalog, Agents: agents, Broker: broker, Budgets: budgets, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	current, err := service.create(context.Background(), "line-a", "manual", "", time.Minute, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if current.maxBytes != 4096 {
		t.Fatalf("Agent authorization exceeded donor budget: %d", current.maxBytes)
	}

	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(current.port)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := authenticateSOCKS(client, current.username, current.password); err != nil {
		t.Fatal(err)
	}
	echoAddress := echo.Addr().(*net.TCPAddr)
	host := "localhost"
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	request = append(request, byte(echoAddress.Port>>8), byte(echoAddress.Port))
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[0] != 5 || reply[1] != 0 {
		t.Fatalf("SOCKS connect reply=%v", reply)
	}
	payload := []byte("mdd-cellular-data-e2e")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo=%q", got)
	}
	backend.mu.Lock()
	lastAddress := backend.lastAddress
	backend.mu.Unlock()
	if lastAddress != net.JoinHostPort(host, strconv.Itoa(echoAddress.Port)) {
		t.Fatalf("Agent received %q, want unresolved SOCKS domain", lastAddress)
	}
	udpEcho, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close()
	go func() {
		packet := make([]byte, 2048)
		n, peer, err := udpEcho.ReadFromUDP(packet)
		if err == nil {
			_, _ = udpEcho.WriteToUDP(packet[:n], peer)
		}
	}()
	control, err := net.Dial("tcp", current.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(5 * time.Second))
	if err := authenticateSOCKS(control, current.username, current.password); err != nil {
		t.Fatal(err)
	}
	_, err = control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(control, header); err != nil || header[1] != 0 {
		t.Fatal("UDP association", header, err)
	}
	addressBytes := 4
	if header[3] == 4 {
		addressBytes = 16
	} else if header[3] != 1 {
		t.Fatal("unexpected relay address", header)
	}
	relay := make([]byte, addressBytes+2)
	if _, err := io.ReadFull(control, relay); err != nil {
		t.Fatal(err)
	}
	relayPort := int(relay[addressBytes])<<8 | int(relay[addressBytes+1])
	udp, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: relayPort})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	_ = udp.SetDeadline(time.Now().Add(5 * time.Second))
	echoPort := udpEcho.LocalAddr().(*net.UDPAddr).Port
	datagram := append([]byte{0, 0, 0, 1, 127, 0, 0, 1, byte(echoPort >> 8), byte(echoPort)}, payload...)
	if _, err := udp.Write(datagram); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 2048)
	n, err := udp.Read(packet)
	if err != nil || n < len(payload) || string(packet[n-len(payload):n]) != string(payload) {
		t.Fatalf("UDP echo: %x %v", packet[:n], err)
	}
	backend.mu.Lock()
	lastAddress = backend.lastAddress
	backend.mu.Unlock()
	if lastAddress != udpEcho.LocalAddr().String() {
		t.Fatal("UDP bypassed Agent data dial", lastAddress)
	}
	current.stop("test")
	backend.mu.Lock()
	stops := backend.stops
	backend.mu.Unlock()
	if stops != 1 {
		t.Fatalf("backend stop count=%d, want exactly one", stops)
	}
	budget, err := budgets.DonorBudget("8985200000000000001")
	if err != nil || budget.Grant != nil || budget.UsedBytes != uint64(4*len(payload)) {
		t.Fatalf("actual SOCKS traffic was not settled: %+v %v", budget, err)
	}
}

func TestInternalEgressLeaseIsLoopbackCredentialedAndRenewedInPlace(t *testing.T) {
	catalog, err := linecatalog.Open(t.TempDir()+"/catalog.db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	_, err = catalog.Put(linecatalog.Line{SchemaVersion: 1, ID: "line-a", Enabled: true, CardID: "8985200000000000001",
		SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: "862547055201716"}})
	if err != nil {
		t.Fatal(err)
	}
	broker, _ := agentdata.NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) { return internalTestToken, nil }), nil)
	agents := &fakeAgents{}
	service, err := New(Config{Context: context.Background(), Catalog: catalog, Agents: agents, Broker: broker})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	handler, err := NewInternalHandler(service, internalTestToken)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(40 * time.Second)
	body := fmt.Sprintf(`{"card_id":"8985200000000000001","purpose":"egress:gb","operation_id":"ensure-a","expires_at":%q,"max_bytes":1048576}`, expires.Format(time.RFC3339Nano))
	request := httptest.NewRequest(http.MethodPost, InternalPath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+internalTestToken)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request)
	if first.Code != http.StatusOK {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	var initial sessionView
	if json.Unmarshal(first.Body.Bytes(), &initial) != nil || initial.Username == "" || initial.Password == "" || initial.Purpose != "egress:gb" {
		t.Fatalf("initial=%+v", initial)
	}
	service.mu.Lock()
	current := service.byID[initial.SessionID]
	service.mu.Unlock()
	if current == nil || current.listener.Addr().(*net.TCPAddr).IP.String() != "127.0.0.1" {
		t.Fatalf("listener=%v", current)
	}
	extended := time.Now().UTC().Add(70 * time.Second)
	body = fmt.Sprintf(`{"card_id":"8985200000000000001","purpose":"egress:gb","operation_id":"renew-a","expires_at":%q,"max_bytes":1048576}`, extended.Format(time.RFC3339Nano))
	request = httptest.NewRequest(http.MethodPost, InternalPath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+internalTestToken)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request)
	var renewed sessionView
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &renewed) != nil || renewed.SessionID != initial.SessionID || !renewed.ExpiresAt.Equal(extended) {
		t.Fatalf("renewed=%d %+v", second.Code, renewed)
	}
	agents.mu.Lock()
	requests := append([]agentlink.ModemDataRequest(nil), agents.requests...)
	agents.mu.Unlock()
	if len(requests) != 2 || requests[0].Action != agentlink.ModemDataPrepare || requests[1].Action != agentlink.ModemDataRenew {
		t.Fatalf("requests=%+v", requests)
	}
}

func authenticateSOCKS(conn net.Conn, username, password string) error {
	if len(username) > 255 || len(password) > 255 {
		return io.ErrShortBuffer
	}
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != 2 {
		return io.ErrUnexpectedEOF
	}
	request := append([]byte{1, byte(len(username))}, username...)
	request = append(request, byte(len(password)))
	request = append(request, password...)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 1 || reply[1] != 0 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
