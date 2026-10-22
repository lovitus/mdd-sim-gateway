//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const managementPipe = `\\.\pipe\MDD.Agent.Management.v1`
const managementRegistry = `SYSTEM\CurrentControlSet\Services\MDDAgent\Parameters\LocalManagement`
const managementValue = "LocalManagement"

// go-winio calls GetNamedPipeInfo, which requires the read/attribute rights.
// FILE_APPEND_DATA is also FILE_CREATE_PIPE_INSTANCE: never grant generic write.
const managementPipeAccess = windows.FILE_GENERIC_READ | windows.FILE_WRITE_DATA
const managementDeleteChild = 0x00000040 // FILE_DELETE_CHILD, not exported by x/sys/windows.

type managementGrant struct {
	Version     int    `json:"version"`
	OperatorSID string `json:"operator_sid"`
	Revision    string `json:"revision"`
	ConfigPath  string `json:"config_path"`
}

type managementInstallation struct {
	image, path string
	pid         uint32
	state       svc.State
}

func installedManagementService() (managementInstallation, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return managementInstallation{}, err
	}
	defer windows.CloseServiceHandle(manager)
	name, _ := windows.UTF16PtrFromString(windowsServiceName)
	handle, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return managementInstallation{}, err
	}
	current := &mgr.Service{Name: windowsServiceName, Handle: handle}
	defer current.Close()
	installed, err := current.Config()
	if err != nil {
		return managementInstallation{}, err
	}
	args, err := windows.DecomposeCommandLine(installed.BinaryPathName)
	if err != nil || len(args) != 4 || args[1] != "service" || args[2] != "-config" ||
		!filepath.IsAbs(args[0]) || !filepath.IsAbs(args[3]) || !strings.EqualFold(filepath.Base(args[0]), "mdd-agent.exe") {
		return managementInstallation{}, errors.New("installed Agent service target is unsupported; administrator review required")
	}
	status, err := current.Query()
	if err != nil {
		return managementInstallation{}, err
	}
	return managementInstallation{filepath.Clean(args[0]), filepath.Clean(args[3]), status.ProcessId, status.State}, nil
}

func sameWindowsPath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func trustedManagementSID(sid *windows.SID) bool {
	if sid == nil {
		return false
	}
	switch sid.String() {
	case "S-1-5-18", "S-1-5-32-544", "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464":
		return true // LocalSystem, Administrators, TrustedInstaller.
	}
	return false
}

func managementDescriptorProtected(sd *windows.SECURITY_DESCRIPTOR, forbidden uint32) error {
	if sd == nil {
		return errors.New("management target security descriptor is unavailable")
	}
	owner, _, err := sd.Owner()
	if err != nil || !trustedManagementSID(owner) {
		return errors.New("management target has an untrusted owner")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return errors.New("management target has no protected access list")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("management target has unsupported access rules")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trustedManagementSID(sid) && uint32(ace.Mask)&forbidden != 0 {
			return errors.New("management target permits untrusted access")
		}
	}
	return nil
}

// Check every replaceable ancestor, not just the current file. This deliberately
// refuses legacy user-owned installs; it never changes their ACLs or moves state.
func managementPathProtected(path string, secret bool) error {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) {
		return errors.New("management requires a local absolute path")
	}
	current := filepath.Clean(path)
	first, parent := true, true
	for {
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(name)
		if err != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("management target is missing or redirected")
		}
		sd, err := windows.GetNamedSecurityInfo(current, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		mask := uint32(windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL | windows.GENERIC_WRITE)
		if first || parent {
			mask |= windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES
		}
		if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
			mask |= managementDeleteChild
		}
		if first && secret {
			mask |= windows.FILE_READ_DATA | windows.GENERIC_READ
		}
		if err := managementDescriptorProtected(sd, mask); err != nil {
			return err
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		parent, first, current = first, false, next
	}
	return nil
}

func readManagementGrant() (managementGrant, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, managementRegistry, registry.QUERY_VALUE|windows.READ_CONTROL)
	if err != nil {
		return managementGrant{}, err
	}
	defer key.Close()
	sd, err := windows.GetSecurityInfo(windows.Handle(key), windows.SE_REGISTRY_KEY, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return managementGrant{}, err
	}
	if err := managementDescriptorProtected(sd, windows.KEY_SET_VALUE|windows.KEY_CREATE_SUB_KEY|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_WRITE|windows.GENERIC_ALL); err != nil {
		return managementGrant{}, err
	}
	value, _, err := key.GetStringValue(managementValue)
	if err != nil {
		return managementGrant{}, err
	}
	var grant managementGrant
	if len(value) > 4096 || strictSetupJSON([]byte(value), &grant) != nil || grant.Version != localManagementVersion || len(grant.Revision) != 32 || grant.OperatorSID == "" || !filepath.IsAbs(grant.ConfigPath) {
		return managementGrant{}, errors.New("invalid local management grant")
	}
	sid, err := windows.StringToSid(grant.OperatorSID)
	if err != nil {
		return managementGrant{}, err
	}
	_, _, kind, err := sid.LookupAccount("")
	if err != nil || kind != windows.SidTypeUser || trustedManagementSID(sid) {
		return managementGrant{}, errors.New("local management requires one explicit user SID")
	}
	return grant, nil
}

func runServiceAccess(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("service-access", flag.ContinueOnError)
	operator := flags.String("operator-sid", "", "explicit ordinary-user SID")
	confirm := flags.Bool("confirm-connection-authority", false, "authorize this account to choose the Core and replace its connection credential")
	revoke := flags.Bool("revoke", false, "revoke local management without stopping the service")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*revoke && (*operator != "" || *confirm)) || (!*revoke && (*operator == "" || !*confirm)) {
		return errors.New("specify -operator-sid SID -confirm-connection-authority, or -revoke")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("administrator elevation is required to change local management authorization")
	}
	installed, err := installedManagementService()
	if err != nil {
		return err
	}
	if err = managementPathProtected(installed.path, true); err != nil {
		return err
	}
	lock, err := lockConfigMutation(installed.path)
	if err != nil {
		return err
	}
	defer lock.Close()
	key, existing, err := registry.CreateKey(registry.LOCAL_MACHINE, managementRegistry, registry.SET_VALUE|registry.QUERY_VALUE|windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER)
	if err != nil {
		return err
	}
	defer key.Close()
	if !existing {
		private, err := windows.SecurityDescriptorFromString("O:BAG:BAD:P(A;;KA;;;SY)(A;;KA;;;BA)")
		if err != nil {
			return err
		}
		owner, _, _ := private.Owner()
		acl, _, _ := private.DACL()
		if err := windows.SetSecurityInfo(windows.Handle(key), windows.SE_REGISTRY_KEY, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, acl, nil); err != nil {
			return err
		}
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(key), windows.SE_REGISTRY_KEY, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if err = managementDescriptorProtected(sd, windows.KEY_SET_VALUE|windows.KEY_CREATE_SUB_KEY|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_WRITE|windows.GENERIC_ALL); err != nil {
		return err
	}
	if *revoke {
		err = key.DeleteValue(managementValue)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			err = nil
		}
		if err == nil {
			_, err = fmt.Fprintln(output, "Local management revoked. Existing connections cannot publish another edit; service and isolation remain running.")
		}
		return err
	}
	if err = managementPathProtected(installed.image, false); err != nil {
		return err
	}
	sid, err := windows.StringToSid(*operator)
	if err != nil {
		return err
	}
	account, domain, kind, err := sid.LookupAccount("")
	if err != nil || kind != windows.SidTypeUser || trustedManagementSID(sid) {
		return errors.New("specify an actual ordinary-user account SID, not a group or service")
	}
	// A new nonce also invalidates previously open sessions for the same SID.
	revision, err := randomHex(16)
	if err != nil {
		return err
	}
	grant := managementGrant{localManagementVersion, sid.String(), revision, installed.path}
	payload, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	if err = key.SetStringValue(managementValue, string(payload)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Authorized %s\\%s (%s) to view and configure this service's Core connection. No SCM or file permissions granted. Apply authorization using the existing maintenance-controlled service restart; no restart was performed.\n", domain, account, sid.String())
	return err
}

func startLocalManagement(ctx context.Context, settings config) (io.Closer, error) {
	grant, err := readManagementGrant()
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	installed, err := installedManagementService()
	if err != nil {
		return nil, err
	}
	image, err := os.Executable()
	if err != nil || !sameWindowsPath(image, installed.image) || !sameWindowsPath(settings.configPath, installed.path) ||
		!sameWindowsPath(grant.ConfigPath, installed.path) {
		return nil, errors.New("local management service identity mismatch")
	}
	authorize := func() error {
		current, err := readManagementGrant()
		if err != nil || current != grant {
			return errors.New("local management authorization changed")
		}
		if err := managementPathProtected(installed.image, false); err != nil {
			return err
		}
		return managementPathProtected(installed.path, true)
	}
	if err := authorize(); err != nil {
		return nil, err
	}
	listener, err := winio.ListenPipe(managementPipe, &winio.PipeConfig{
		SecurityDescriptor: fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x%x;;;%s)", managementPipeAccess, grant.OperatorSID),
		InputBufferSize:    8192, OutputBufferSize: 8192,
	})
	if err != nil {
		return nil, err
	}
	nonce, err := randomHex(16)
	if err != nil {
		listener.Close()
		return nil, err
	}
	handler := &localManagementHandler{settings: settings, session: nonce, authorize: authorize, slots: make(chan struct{}, 4)}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 50 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	server.SetKeepAlivesEnabled(false)
	go func() {
		_ = server.Serve(&boundedManagementListener{Listener: listener, slots: make(chan struct{}, 8)})
	}()
	return server, nil
}

type boundedManagementListener struct {
	net.Listener
	slots chan struct{}
}
type managedAcceptedConn struct {
	net.Conn
	release func()
	once    sync.Once
	err     error
}

func (conn *managedAcceptedConn) Close() error {
	conn.once.Do(func() { conn.err = conn.Conn.Close(); conn.release() })
	return conn.err
}
func (listener *boundedManagementListener) Accept() (net.Conn, error) {
	for {
		conn, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case listener.slots <- struct{}{}:
			return &managedAcceptedConn{Conn: conn, release: func() { <-listener.slots }}, nil
		default:
			conn.Close()
		}
	}
}

// Retain the verified process handle until the request finishes. Every HTTP
// request uses one freshly verified pipe, never a checked-then-reconnected peer.
type verifiedManagementConn struct {
	net.Conn
	process windows.Handle
	once    sync.Once
	err     error
}

func (conn *verifiedManagementConn) Close() error {
	conn.once.Do(func() { conn.err = conn.Conn.Close(); windows.CloseHandle(conn.process) })
	return conn.err
}

func dialManagement(ctx context.Context, expected managementInstallation) (net.Conn, error) {
	conn, err := winio.DialPipeAccess(ctx, managementPipe, managementPipeAccess)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	fd, valid := conn.(interface{ Fd() uintptr })
	if !valid {
		return nil, errors.New("management pipe handle unavailable")
	}
	var pid uint32
	if err = windows.GetNamedPipeServerProcessId(windows.Handle(fd.Fd()), &pid); err != nil {
		return nil, err
	}
	current, err := installedManagementService()
	if err != nil || current.state != svc.Running || current.pid == 0 || pid != current.pid ||
		!sameWindowsPath(expected.image, current.image) || !sameWindowsPath(expected.path, current.path) {
		return nil, errors.New("management pipe is not the current Agent service")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			windows.CloseHandle(process)
		}
	}()
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return nil, err
	}
	if created == (windows.Filetime{}) || exited != (windows.Filetime{}) {
		return nil, errors.New("management service lifetime is not active")
	}
	buffer := make([]uint16, 32768)
	length := uint32(len(buffer))
	if err = windows.QueryFullProcessImageName(process, 0, &buffer[0], &length); err != nil {
		return nil, err
	}
	if !sameWindowsPath(windows.UTF16ToString(buffer[:length]), current.image) {
		return nil, errors.New("management service image differs from SCM")
	}
	if err = managementPathProtected(current.image, false); err != nil {
		return nil, err
	}
	// Recheck the same connected handle and SCM after acquiring the process handle.
	var again uint32
	latest, err := installedManagementService()
	if err != nil || windows.GetNamedPipeServerProcessId(windows.Handle(fd.Fd()), &again) != nil || latest != current || again != pid {
		return nil, errors.New("management service changed while connecting")
	}
	if result, err := windows.WaitForSingleObject(process, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
		return nil, errors.New("management service exited")
	}
	ok = true
	return &verifiedManagementConn{Conn: conn, process: process}, nil
}

type windowsManagementClient struct{ installation managementInstallation }

func (client *windowsManagementClient) request(ctx context.Context, edit *localManagementEdit) (localManagementState, error) {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialManagement(ctx, client.installation)
	}}
	defer transport.CloseIdleConnections()
	method := http.MethodGet
	var body io.Reader
	if edit != nil {
		payload, err := json.Marshal(edit)
		if err != nil {
			return localManagementState{}, err
		}
		method, body = http.MethodPut, bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://mdd-local/v1/connection", body)
	if err != nil {
		return localManagementState{}, err
	}
	response, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("management redirect refused") }}).Do(request)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return localManagementState{}, errors.New("本账户没有本机管理访问权限；请由管理员明确授权操作账户。")
		}
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return localManagementState{}, errors.New("服务没有提供本机管理入口；可能是旧版本或授权尚未加载。未回退读取秘密配置。")
		}
		return localManagementState{}, errors.New("本机服务管理不可用或请求结果未确认；未重试保存。请检查服务、授权和版本。")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure)
		return localManagementState{}, managementMessage(failure.Code)
	}
	var state localManagementState
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF || state.Version != localManagementVersion || len(state.Session) != 32 || len(state.Revision) != 64 {
		return localManagementState{}, errors.New("本机管理响应无效；没有重试操作。")
	}
	return state, nil
}
