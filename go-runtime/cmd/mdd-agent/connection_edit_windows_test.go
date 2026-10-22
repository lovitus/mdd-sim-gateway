package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
	"golang.org/x/sys/windows"
)

func TestConnectionEditPreservesWindowsACL(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	payload := `{"version":1,"username":"setup-user","salt":"00112233445566778899aabbccddeeff","password_hash":"ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302","agent_token":"0123456789abcdef0123456789abcdef"}`
	if err := os.WriteFile(authPath, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := adminauth.NewManager(authPath, true, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := adminauth.NewHandler(manager)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	settings, err := newEditableConfig()
	if err != nil {
		t.Fatal(err)
	}
	settings.Agent.ServerToken, err = manager.CreateAgentToken(settings.Agent.ID, strings.Repeat("a", 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(server.Certificate().Raw)
	settings.Agent.ServerURL = strings.Replace(server.URL, "https:", "wss:", 1) + "/v1/agent/ws"
	settings.Agent.TLSMode, settings.Agent.TLSFingerprint = "pin", hex.EncodeToString(digest[:])
	directory := t.TempDir()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setACL := func(path, acl string) {
		t.Helper()
		sd, err := windows.SecurityDescriptorFromString(acl)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The existing directory permits inherited reads, but this config has a
	// protected ACL including a separate service identity that must be retained.
	setACL(directory, "D:P(A;OICI;FA;;;"+user.User.Sid.String()+")(A;OICI;FA;;;SY)(A;OICI;GR;;;WD)")
	path := filepath.Join(directory, "config.json")
	if err := saveConfig(path, settings); err != nil {
		t.Fatal(err)
	}
	setACL(path, "D:P(A;;FA;;;"+user.User.Sid.String()+")(A;;FA;;;SY)(A;;GR;;;LS)")
	security := func(path string) string {
		t.Helper()
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil || sd == nil {
			t.Fatalf("security descriptor: %v", err)
		}
		return sd.String()
	}
	original := security(path)
	// Restrict this process's token to access also granted to Everyone. This
	// cannot elevate the test and models a reader excluded from the private ACL.
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	var process, restricted, reader windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &process); err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	restriction := windows.SIDAndAttributes{Sid: everyone}
	created, _, createErr := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken").Call(
		uintptr(process), 1, 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&restriction)), uintptr(unsafe.Pointer(&restricted)))
	runtime.KeepAlive(everyone)
	if created == 0 {
		t.Fatalf("restrict reader token: %v", createErr)
	}
	defer restricted.Close()
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &reader); err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	openAsReader := func(name string) error {
		filename, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return err
		}
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.SetThreadToken(nil, reader); err != nil {
			return err
		}
		defer func() {
			if err := windows.RevertToSelf(); err != nil {
				panic(err)
			}
		}()
		handle, err := windows.CreateFile(filename, windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			return windows.CloseHandle(handle)
		}
		return err
	}
	public := filepath.Join(directory, "inherited-readable.txt")
	if err := os.WriteFile(public, []byte("public control"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := openAsReader(public); err != nil {
		t.Fatalf("restricted reader cannot read inherited public control: %v", err)
	}
	if err := openAsReader(path); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("original config is not private to restricted reader: %v", err)
	}
	for _, originalPath := range []string{path, ""} {
		temporary, err := createSetupTemp(directory, originalPath)
		if err != nil {
			t.Fatal(err)
		}
		name := temporary.Name()
		// The file is still empty and open. Sharing is permitted by the creator,
		// so only its creation-time ACL can reject an unauthorized retained handle.
		accessErr := openAsReader(name)
		closeErr := temporary.Close()
		closedAccessErr := openAsReader(name)
		removeErr := os.Remove(name)
		if !errors.Is(accessErr, windows.ERROR_ACCESS_DENIED) || !errors.Is(closedAccessErr, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("creation-time private ACL missing: open=%v closed=%v", accessErr, closedAccessErr)
		}
		if closeErr != nil || removeErr != nil {
			t.Fatalf("temporary cleanup: %v / %v", closeErr, removeErr)
		}
	}
	edit, err := readConnectionEdit(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := edit.save(t.Context(), connectionChange{Server: settings.Agent.ServerURL, TLSMode: "pin"}); err != nil {
		t.Fatal(err)
	}
	if actual := security(path); actual != original {
		t.Fatalf("private owner/service ACL changed: before=%s after=%s", original, actual)
	}
	loaded, err := loadConfig(path)
	if err != nil || loaded.Agent.ServerToken != settings.Agent.ServerToken {
		t.Fatalf("credential not retained: %v", err)
	}
}
