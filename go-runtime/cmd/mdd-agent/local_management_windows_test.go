//go:build windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	winio "github.com/Microsoft/go-winio"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/core"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// This fixture has no Providers; any request to drain one is a test failure.
type managementEmptyProviders struct{}

func (managementEmptyProviders) Request(context.Context, providerapply.DrainRequest, bool) (providerapply.DrainResult, error) {
	return providerapply.DrainResult{}, errors.New("unexpected provider maintenance")
}

func TestWindowsLocalManagementJourney(t *testing.T) {
	binary := os.Getenv("MDD_MANAGEMENT_BINARY")
	if binary == "" {
		t.Skip("requires the hosted Windows production Agent binary and an unused SCM")
	}
	manager, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Disconnect()
	if existing, err := manager.OpenService(windowsServiceName); err == nil {
		existing.Close()
		t.Fatal("will not replace an existing Agent service")
	} else if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		t.Fatal(err)
	}
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, managementRegistry, registry.QUERY_VALUE); err == nil {
		key.Close()
		t.Fatal("will not replace existing management authorization")
	} else if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		t.Fatal(err)
	}
	nonce, err := randomHex(8)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(os.Getenv("ProgramData"), "MDD-management-test-"+nonce)
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	removeFiles := true
	defer func() {
		if removeFiles {
			_ = os.RemoveAll(root)
		}
	}()
	protectManagementFixture(t, root, false)
	image := filepath.Join(root, "mdd-agent.exe")
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, payload, 0600); err != nil {
		t.Fatal(err)
	}
	protectManagementFixture(t, image, false)
	data := filepath.Join(root, "private")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	protectManagementFixture(t, data, true)
	// This is a real SCM process, not the accelerated in-process worker fixture.
	settings, err := newEditableConfig()
	if err != nil {
		t.Fatal(err)
	}
	settings.Control.Listen = availableAgentAddress(t)
	settings.Agent.ID = "management-journey"
	settings.configPath = filepath.Join(data, "agent.json")
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"version":1,"username":"setup-user","salt":"00112233445566778899aabbccddeeff","password_hash":"ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302","agent_token":"0123456789abcdef0123456789abcdef"}`), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := adminauth.NewManager(authPath, true, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	settings.Agent.ServerToken, err = auth.CreateAgentToken(settings.Agent.ID, strings.Repeat("a", 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	authHandler, err := adminauth.NewHandler(auth)
	if err != nil {
		t.Fatal(err)
	}
	agents, err := agentlink.NewServer(auth)
	if err != nil {
		t.Fatal(err)
	}
	defer agents.DisconnectAgent(settings.Agent.ID)
	catalog, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	replay, err := events.NewReplay(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := core.NewSystemMaintenanceHandler(managementEmptyProviders{})
	if err != nil {
		t.Fatal(err)
	}
	coreHandler := core.NewServer(replay, nil, core.WithAdminAuth(authHandler), core.WithManagementAuth(auth.Middleware),
		core.WithAgentLink(agents), core.WithAgentFacts(agents), core.WithLineCatalog(catalog, nil),
		core.WithSystemMaintenance(maintenance), core.WithAgentRestartBusyCheck(func(string) (bool, error) { return false, nil }))
	var coreAttempts atomic.Uint32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/agent/ws" {
			coreAttempts.Add(1)
		}
		coreHandler.ServeHTTP(w, r)
	}))
	defer peer.Close()
	pin := sha256.Sum256(peer.Certificate().Raw)
	settings.Agent.ServerURL = strings.Replace(peer.URL, "https:", "wss:", 1) + "/v1/agent/ws"
	settings.Agent.TLSMode, settings.Agent.TLSFingerprint = "pin", hex.EncodeToString(pin[:])
	if err := saveConfig(settings.configPath, settings); err != nil {
		t.Fatal(err)
	}
	protectManagementFixture(t, settings.configPath, true)
	service, err := manager.CreateService(windowsServiceName, image, mgr.Config{StartType: mgr.StartManual}, "service", "-config", settings.configPath)
	if err != nil {
		t.Fatal(err)
	}
	removeFiles = false
	defer func() {
		defer service.Close()
		_, _ = service.Control(svc.Stop)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			status, err := service.Query()
			if err != nil || status.State == svc.Stopped {
				break
			}
			time.Sleep(time.Second)
		}
		if status, err := service.Query(); err != nil || status.State != svc.Stopped {
			t.Error("owned service did not stop; retained its files")
			return
		}
		if err := service.Delete(); err != nil {
			t.Error(err)
			return
		}
		removeFiles = true
		_ = registry.DeleteKey(registry.LOCAL_MACHINE, managementRegistry)
	}()
	if err := configureWindowsServiceRecovery(); err != nil {
		t.Fatal(err)
	}
	// A pipe with the expected name is not sufficient. No application bytes may
	// reach a peer that is not the exact running SCM process.
	fake, err := winio.ListenPipe(managementPipe, nil)
	if err != nil {
		t.Fatal(err)
	}
	fakeRead := make(chan int, 1)
	go func() {
		conn, err := fake.Accept()
		if err != nil {
			fakeRead <- -1
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var buffer [1]byte
		n, _ := conn.Read(buffer[:])
		fakeRead <- n
	}()
	falseInstallation, err := installedManagementService()
	if err != nil {
		fake.Close()
		t.Fatal(err)
	}
	_, fakeErr := (&windowsManagementClient{installation: falseInstallation}).request(t.Context(), nil)
	if fakeErr == nil {
		fake.Close()
		t.Fatal("counterfeit pipe accepted")
	}
	select {
	case n := <-fakeRead:
		if n != 0 {
			fake.Close()
			t.Fatalf("counterfeit pipe received application bytes: %d", n)
		}
	case <-time.After(6 * time.Second):
		fake.Close()
		t.Fatal("counterfeit pipe check did not finish")
	}
	fake.Close()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var grantReceipt bytes.Buffer
	if err := runServiceAccess([]string{"-operator-sid", user.User.Sid.String(), "-confirm-connection-authority"}, &grantReceipt); err != nil {
		t.Fatal(err)
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	// A single bounded readiness window. No hardware, carrier or paid operation.
	var installed managementInstallation
	var initial localManagementState
	var readiness []map[string]any
	for _, delay := range []time.Duration{30 * time.Second, 30 * time.Second} {
		time.Sleep(delay)
		initial = localManagementState{}
		installed, err = installedManagementService()
		evidence := map[string]any{"sample_at": time.Now().UTC(), "scm_state": installed.state, "scm_pid": installed.pid, "scm_query_failed": err != nil, "management_requested": false}
		if err == nil && installed.state == svc.Running {
			evidence["management_requested"] = true
			initial, err = (&windowsManagementClient{installation: installed}).request(t.Context(), nil)
			evidence["management_succeeded"] = err == nil
		}
		evidence["response_at"], evidence["response_valid"] = time.Now().UTC(), initial.Version == localManagementVersion
		evidence["runtime_present"], evidence["runtime_unavailable"] = initial.Runtime != nil, initial.RuntimeUnavailable
		evidence["topology_present"], evidence["topology_unavailable"] = initial.Topology != nil, initial.TopologyUnavailable
		if initial.Runtime != nil {
			evidence["runtime_state"], evidence["runtime_code"] = initial.Runtime.State, initial.Runtime.Code
			evidence["runtime_generation"], evidence["runtime_changed_at"] = initial.Runtime.Generation, initial.Runtime.ChangedAt
			evidence["core_connection"] = initial.Runtime.CoreConnection
		}
		if initial.Topology != nil {
			evidence["reader_condition"], evidence["modem_condition"] = initial.Topology.ReaderCondition, initial.Topology.ModemCondition
			evidence["reader_count"], evidence["modem_count"] = len(initial.Topology.Readers), len(initial.Topology.Modems)
		}
		current, found := agents.Status(settings.Agent.ID)
		evidence["core_has_agent"], evidence["core_path_requests_cumulative"] = found, coreAttempts.Load()
		if found {
			evidence["core_has_topology"], evidence["core_last_report"] = current.Topology != nil, current.LastReport
			evidence["core_connected_at"], evidence["core_last_seen"] = current.ConnectedAt, current.LastSeen
		}
		scardName, _ := windows.UTF16PtrFromString("SCardSvr")
		if handle, openErr := windows.OpenService(manager.Handle, scardName, windows.SERVICE_QUERY_STATUS); openErr == nil {
			smartcards := &mgr.Service{Name: "SCardSvr", Handle: handle}
			status, queryErr := smartcards.Query()
			evidence["pcsc_service_state"], evidence["pcsc_query_failed"] = status.State, queryErr != nil
			smartcards.Close()
		} else {
			evidence["pcsc_query_failed"] = true
		}
		readiness = append(readiness, evidence)
		if err == nil && initial.Runtime != nil && initial.Runtime.CoreConnection != nil && initial.Runtime.CoreConnection.State == "connected" {
			break
		}
	}
	if err != nil || initial.Runtime == nil || initial.Runtime.CoreConnection == nil || initial.Runtime.CoreConnection.State != "connected" {
		// Only typed states/counts leave the isolated fixture; no raw Detail.
		payload, _ := json.Marshal(readiness)
		t.Logf("service readiness evidence: %s", payload)
		t.Fatalf("actual service failed readiness: %v", err)
	}
	status, ok := agents.Status(settings.Agent.ID)
	if !ok || status.Topology == nil || status.Topology.ReaderCondition != agentlink.ReaderReady || len(status.Topology.Readers) != 0 || len(status.Topology.Modems) != 0 {
		t.Fatal("isolated service does not have a fresh empty hardware inventory")
	}
	before, err := os.ReadFile(settings.configPath)
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := auth.IssueAgentToken(settings.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(localManagementEdit{Version: 1, Session: initial.Session, Revision: initial.Revision, Change: connectionChange{Server: settings.Agent.ServerURL, TLSMode: "pin", Token: newToken}})
	ordinary := managementRestrictedToken(t, false)
	defer ordinary.Close()
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestManagement(OrdinaryUser|GUI)Helper$", "-test.v")
	child.Env = append(os.Environ(), "MDD_MANAGEMENT_HELPER=edit")
	child.Stdin = bytes.NewReader(input)
	child.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(ordinary)}
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("ordinary-user journey: %v %s", err, output)
	}
	after, err := os.ReadFile(settings.configPath)
	if err != nil || bytes.Equal(before, after) {
		t.Fatal("ordinary-user save not persisted")
	}
	pending, err := (&windowsManagementClient{installation: installed}).request(t.Context(), nil)
	if err != nil || pending.Loaded || pending.Session != initial.Session {
		t.Fatal("save restarted or falsely reported loaded")
	}
	// The real Core guard must reject an already-owned maintenance lease.
	admin := &setupHTTP{client: peer.Client(), origin: peer.URL}
	admin.client.Timeout = 2 * time.Minute
	var login struct {
		Token string `json:"token"`
		CSRF  string `json:"csrf"`
	}
	if err := admin.request(t.Context(), http.MethodPost, "/api/auth/login", map[string]string{"username": "setup-user", "password": "correct horse battery staple"}, &login); err != nil {
		t.Fatal(err)
	}
	admin.session, admin.csrf = login.Token, login.CSRF
	defer admin.request(context.Background(), http.MethodPost, "/api/auth/logout", nil, nil)
	if err := agents.BeginHostMaintenance(settings.Agent.ID, status.ProcessGeneration, "busy-fixture"); err != nil {
		t.Fatal(err)
	}
	restart := agentlink.AgentRestartRequest{OperationID: "managed-save-restart", ProcessGeneration: status.ProcessGeneration}
	if err := admin.request(t.Context(), http.MethodPost, "/v1/agents/"+settings.Agent.ID+"/restart", restart, nil); err == nil {
		t.Fatal("Core restarted during another maintenance ownership")
	}
	if current, err := installedManagementService(); err != nil || current.pid != installed.pid {
		t.Fatal("busy rejection changed original service")
	}
	if err := agents.EndHostMaintenance(settings.Agent.ID, "busy-fixture"); err != nil {
		t.Fatal(err)
	}
	var restarted struct {
		State      string `json:"state"`
		Generation string `json:"process_generation"`
	}
	if err := admin.request(t.Context(), http.MethodPost, "/v1/agents/"+settings.Agent.ID+"/restart", restart, &restarted); err != nil {
		t.Fatal(err)
	}
	if restarted.State != "restarted" || restarted.Generation == status.ProcessGeneration {
		t.Fatal("Core did not confirm a fresh service generation")
	}
	loaded, err := (&windowsManagementClient{installation: installed}).request(t.Context(), nil)
	if err != nil || !loaded.Loaded || loaded.Session == initial.Session || loaded.Revision != pending.Revision {
		t.Fatal("restarted service did not load the confirmed saved revision")
	}
	// Keep a verified handle open across revocation, then try to use it.
	open, err := dialManagement(t.Context(), installed)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if err := runServiceAccess([]string{"-revoke"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	_ = open.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(open, "GET /v1/connection HTTP/1.1\r\nHost: mdd-local\r\nConnection: close\r\n\r\n"); err == nil {
		response, readErr := http.ReadResponse(bufio.NewReader(open), nil)
		if readErr == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				t.Fatal("revoked open connection remained authorized")
			}
		}
	}
	if _, err := (&windowsManagementClient{installation: installed}).request(t.Context(), nil); err == nil {
		t.Fatal("revoked live listener still serves requests")
	}
	if current, err := installedManagementService(); err != nil || current.state != svc.Running {
		t.Fatal("revocation stopped the hardware owner")
	}
}

func protectManagementFixture(t *testing.T, path string, secret bool) {
	t.Helper()
	sddl := "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if !secret {
		sddl += "(A;OICI;GRGX;;;BU)"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, _ := sd.Owner()
	acl, _, _ := sd.DACL()
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func managementRestrictedToken(t *testing.T, denied bool) windows.Token {
	t.Helper()
	var original, restricted windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY, &original); err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	var disabled []windows.SIDAndAttributes
	for _, kind := range []windows.WELL_KNOWN_SID_TYPE{windows.WinBuiltinAdministratorsSid, windows.WinLocalSystemSid} {
		sid, err := windows.CreateWellKnownSid(kind)
		if err != nil {
			t.Fatal(err)
		}
		disabled = append(disabled, windows.SIDAndAttributes{Sid: sid})
	}
	var restrictedCount, restrictedPointer uintptr
	var restriction windows.SIDAndAttributes
	if denied {
		sid, err := windows.CreateWellKnownSid(windows.WinWorldSid)
		if err != nil {
			t.Fatal(err)
		}
		restriction.Sid = sid
		restrictedCount, restrictedPointer = 1, uintptr(unsafe.Pointer(&restriction))
	}
	ok, _, err := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken").Call(uintptr(original), 1, uintptr(len(disabled)), uintptr(unsafe.Pointer(&disabled[0])), 0, 0, restrictedCount, restrictedPointer, uintptr(unsafe.Pointer(&restricted)))
	runtime.KeepAlive(disabled)
	runtime.KeepAlive(restriction)
	if ok == 0 {
		t.Fatal(err)
	}
	return restricted
}

func TestManagementOrdinaryUserHelper(t *testing.T) {
	if os.Getenv("MDD_MANAGEMENT_HELPER") != "edit" {
		return
	}
	groups, err := windows.GetCurrentProcessToken().GetTokenGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.AllGroups() {
		if trustedManagementSID(group.Sid) && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			t.Fatal("helper retained an enabled privileged group")
		}
	}
	installed, err := installedManagementService()
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(installed.path); !errors.Is(err, os.ErrPermission) || len(data) != 0 {
		t.Fatal("ordinary user can read service secrets")
	}
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseServiceHandle(manager)
	name, _ := windows.UTF16PtrFromString(windowsServiceName)
	for _, rights := range []uint32{windows.SERVICE_START, windows.SERVICE_STOP, windows.SERVICE_CHANGE_CONFIG, windows.WRITE_DAC} {
		handle, err := windows.OpenService(manager, name, rights)
		if err == nil {
			windows.CloseServiceHandle(handle)
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("unexpected SCM access result: %x %v", rights, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
	defer cancel()
	client := &windowsManagementClient{installation: installed}
	state, err := client.request(ctx, nil)
	if err != nil || !state.Loaded {
		t.Fatalf("ordinary user cannot read existing service: %v", err)
	}
	// Individual read/write rights must succeed without instance-creation rights.
	conn, err := winio.DialPipeAccess(ctx, managementPipe, windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err == nil {
		conn.Close()
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatal("PIPE_INSTANCE_RIGHT: generic write was not denied", err)
	}
	var request localManagementEdit
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 8192)).Decode(&request); err != nil {
		t.Fatal(err)
	}
	saved, err := client.request(ctx, &request)
	if err != nil || saved.Loaded || saved.Session != state.Session {
		t.Fatalf("ordinary-user save failed: %v", err)
	}
	if _, err := client.request(ctx, &request); err == nil {
		t.Fatal("stale revision replay succeeded")
	}
}

func TestManagementProtectedDescriptor(t *testing.T) {
	for _, test := range []struct {
		name, sddl string
		forbidden  uint32
		allow      bool
	}{
		{"private", "O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)", windows.FILE_READ_DATA | windows.FILE_WRITE_DATA, true},
		{"public-image-read", "O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;GRGX;;;BU)", windows.FILE_WRITE_DATA | windows.DELETE | windows.WRITE_DAC, true},
		{"config-readable", "O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;GR;;;BU)", windows.FILE_READ_DATA | windows.GENERIC_READ, false},
		{"replace-child", "O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x40;;;BU)", managementDeleteChild, false},
		{"writable-image", "O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;GW;;;BU)", windows.GENERIC_WRITE, false},
		{"untrusted-owner", "O:BUG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)", windows.FILE_WRITE_DATA, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := managementDescriptorProtected(sd, test.forbidden); (err == nil) != test.allow {
				t.Fatalf("descriptor allow=%t err=%v", test.allow, err)
			}
		})
	}
}
