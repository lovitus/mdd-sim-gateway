//go:build !windows

package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
)

const bootstrapTestPassword = "a private bootstrap password"

func TestBootstrapHostCreatesCompatiblePrivateMaterial(t *testing.T) {
	layout, uid, gid := bootstrapTestLayout(t)
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)
	receipt, err := bootstrapHost(hostBootstrapOptions{
		Layout: layout, PublicListen: "0.0.0.0:8443", Username: "fanli",
		Password: []byte(bootstrapTestPassword), ServiceUID: uid, ServiceGID: gid,
		RootUID: uid, RootGID: gid, Now: func() time.Time { return now },
		Hostname: func() (string, error) { return "MDD-Test-Host", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "created" || receipt.ConfigPath != layout.configPath() ||
		receipt.AgentTokenPath != layout.authPath() || receipt.AgentTokenJSONField != "agent_token" ||
		receipt.AgentTokenReadAs != "root" {
		t.Fatalf("receipt=%+v", receipt)
	}
	for _, path := range []string{layout.authPath(), layout.tlsCertPath(), layout.tlsKeyPath(), layout.configPath()} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("file %s info=%v err=%v", path, info, err)
		}
	}
	for path, mode := range map[string]os.FileMode{
		layout.StateDirectory: 0o700, filepath.Join(layout.StateDirectory, "providers"): 0o700,
		layout.SystemStateDirectory: 0o700, layout.providerReceiptPath(): 0o700,
		layout.tlsDirectory(): 0o700, layout.providerCandidateRoot(): 0o755,
		layout.EgressConfigDirectory: 0o750,
	} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != mode {
			t.Fatalf("directory %s mode=%v err=%v", path, info, err)
		}
	}

	settings, err := loadConfig(layout.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Public.Listen != "0.0.0.0:8443" || settings.Local.Listen != "127.0.0.1:19444" ||
		len(settings.Local.Token) < 32 || settings.AuthPath != layout.authPath() ||
		settings.AllowancePath != filepath.Join(layout.StateDirectory, "allowance.db") ||
		settings.NotificationsPath != filepath.Join(layout.StateDirectory, "notifications.db") ||
		settings.PreferencesPath != filepath.Join(layout.StateDirectory, "preferences.db") ||
		!settings.ProviderApply.Enabled || settings.ProviderApply.CandidateRoot != layout.providerCandidateRoot() ||
		settings.ProviderApply.EgressDesiredPath != filepath.Join(layout.EgressConfigDirectory, "desired.json") {
		t.Fatalf("settings=%+v", settings)
	}
	rawConfig, err := os.ReadFile(layout.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawConfig, []byte(`"notifications_path"`)) {
		t.Fatal("fresh bootstrap wrote a field that the rollback Core cannot decode")
	}
	auth, err := adminauth.NewManager(layout.authPath(), true, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if len(auth.AgentToken()) < 32 {
		t.Fatal("generated Agent token is missing")
	}
	if _, err := auth.Login("fanli", bootstrapTestPassword, "127.0.0.1"); err != nil {
		t.Fatalf("generated administrator credential cannot log in: %v", err)
	}

	identity, err := tls.LoadX509KeyPair(layout.tlsCertPath(), layout.tlsKeyPath())
	if err != nil || len(identity.Certificate) != 1 {
		t.Fatalf("TLS identity certificates=%d err=%v", len(identity.Certificate), err)
	}
	certificate, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(certificate.IPAddresses) != 2 || !reflect.DeepEqual(certificate.DNSNames, []string{"localhost", "mdd-test-host"}) ||
		!reflect.DeepEqual([]string{certificate.IPAddresses[0].String(), certificate.IPAddresses[1].String()}, []string{"127.0.0.1", "::1"}) {
		t.Fatalf("unexpected SANs dns=%v ip=%v", certificate.DNSNames, certificate.IPAddresses)
	}
	if err := certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature); err != nil {
		t.Fatalf("self-signed certificate signature: %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	certificateHash := sha256.Sum256(certificate.Raw)
	publicKeyHash := sha256.Sum256(publicKeyDER)
	if receipt.TLSCertificateSHA256 != hex.EncodeToString(certificateHash[:]) ||
		receipt.TLSSPKISHA256 != "sha256/"+base64.StdEncoding.EncodeToString(publicKeyHash[:]) {
		t.Fatalf("certificate pins=%+v", receipt)
	}
	receiptPayload, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{bootstrapTestPassword, auth.AgentToken(), settings.Local.Token} {
		if strings.Contains(string(receiptPayload), secret) {
			t.Fatal("bootstrap output disclosed secret material")
		}
	}
}

func TestBootstrapHostnameSANIsSafeAndBounded(t *testing.T) {
	for input, want := range map[string]string{
		" MDD-Host.Example. ":                "mdd-host.example",
		"localhost":                          "localhost",
		"127.0.0.1":                          "",
		"bad/name":                           "",
		"-bad.example":                       "",
		"bad-.example":                       "",
		strings.Repeat("a", 64) + ".example": "",
		strings.Repeat("a", 254):             "",
	} {
		if got := validBootstrapHostname(input); got != want {
			t.Fatalf("hostname %q=%q want %q", input, got, want)
		}
	}
	if got := bootstrapHostname(func() (string, error) { return "ignored", errors.New("unavailable") }); got != "" {
		t.Fatalf("unavailable hostname=%q", got)
	}
}

func TestBootstrapHostRefusesAnyExistingTargetWithoutMutation(t *testing.T) {
	layout, uid, gid := bootstrapTestLayout(t)
	if err := os.Mkdir(layout.ConfigDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("existing configuration\n")
	if err := os.WriteFile(layout.configPath(), sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := bootstrapHost(hostBootstrapOptions{
		Layout: layout, PublicListen: "0.0.0.0:8443", Username: "admin", Password: []byte("replacement"),
		ServiceUID: uid, ServiceGID: gid, RootUID: uid, RootGID: gid,
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing target err=%v", err)
	}
	current, readErr := os.ReadFile(layout.configPath())
	if readErr != nil || !bytes.Equal(current, sentinel) {
		t.Fatalf("existing file changed payload=%q err=%v", current, readErr)
	}
	for _, path := range []string{layout.authPath(), layout.tlsDirectory(), layout.StateDirectory, layout.SystemStateDirectory} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("bootstrap mutated %s after preflight failure: %v", path, statErr)
		}
	}
}

func TestBootstrapHostRejectsOrphanedDurableStateBeforeGeneratingMaterial(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, hostBootstrapLayout)
	}{
		{"events database", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestFile(t, filepath.Join(layout.StateDirectory, "events.db"))
		}},
		{"messages database", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestFile(t, filepath.Join(layout.StateDirectory, "messages.db"))
		}},
		{"cellular message operations database", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestFile(t, filepath.Join(layout.StateDirectory, "messages.db.cellular-operations"))
		}},
		{"calls database symlink", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestSymlink(t, filepath.Join(layout.StateDirectory, "calls.db"))
		}},
		{"catalog database directory", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestDirectory(t, filepath.Join(layout.StateDirectory, "catalog.db"), 0o700)
		}},
		{"egress database", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestFile(t, filepath.Join(layout.StateDirectory, "egress.db"))
		}},
		{"preferences database", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestFile(t, filepath.Join(layout.StateDirectory, "preferences.db"))
		}},
		{"provider state entry", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestFile(t, filepath.Join(layout.StateDirectory, "providers", "line-1.json"))
		}},
		{"provider state symlink", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestSymlink(t, filepath.Join(layout.StateDirectory, "providers"))
		}},
		{"provider state fake directory", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestFile(t, filepath.Join(layout.StateDirectory, "providers"))
		}},
		{"country exit desired", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestFile(t, filepath.Join(layout.EgressConfigDirectory, "desired.json"))
		}},
		{"provider current symlink", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestSymlink(t, filepath.Join(layout.ConfigDirectory, "providers-current"))
		}},
		{"provider release entry", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestFile(t, filepath.Join(layout.providerCandidateRoot(), "line-1.json"))
		}},
		{"provider releases symlink", func(t *testing.T, layout hostBootstrapLayout) {
			bootstrapTestSymlink(t, layout.providerCandidateRoot())
		}},
		{"provider releases fake directory", func(t *testing.T, layout hostBootstrapLayout) { bootstrapTestFile(t, layout.providerCandidateRoot()) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout, uid, gid := bootstrapTestLayout(t)
			test.setup(t, layout)
			entropy := &bootstrapReadCounter{}
			_, err := bootstrapHost(hostBootstrapOptions{
				Layout: layout, PublicListen: "0.0.0.0:8443", Username: "admin", Password: []byte("private password"),
				ServiceUID: uid, ServiceGID: gid, RootUID: uid, RootGID: gid, Entropy: entropy,
				Hostname: func() (string, error) { return "mdd-test", nil },
			})
			if err == nil || !strings.Contains(err.Error(), "restore its matching core configuration") ||
				!strings.Contains(err.Error(), "archive/remove") {
				t.Fatalf("orphaned state err=%v", err)
			}
			if entropy.reads != 0 {
				t.Fatalf("entropy was consumed %d times before orphaned state rejection", entropy.reads)
			}
			for _, path := range []string{layout.authPath(), layout.tlsCertPath(), layout.tlsKeyPath(), layout.configPath()} {
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("bootstrap generated %s after orphaned state rejection: %v", path, statErr)
				}
			}
		})
	}
}

func TestBootstrapHostAllowsEmptyOwnedStateAndIgnoresReleaseInstallReceipts(t *testing.T) {
	layout, uid, gid := bootstrapTestLayout(t)
	for _, directory := range []bootstrapDirectory{
		{layout.ConfigDirectory, 0o755, uid, gid},
		{filepath.Join(layout.ConfigDirectory, "providers"), 0o755, uid, gid},
		{layout.providerCandidateRoot(), 0o755, uid, gid},
		{layout.StateDirectory, 0o700, uid, gid},
		{filepath.Join(layout.StateDirectory, "providers"), 0o700, uid, gid},
		{layout.SystemStateDirectory, 0o700, uid, gid},
		{layout.providerReceiptPath(), 0o700, uid, gid},
		{layout.EgressConfigDirectory, 0o750, uid, gid},
	} {
		created, err := ensureBootstrapDirectory(directory)
		if err != nil || !created {
			t.Fatalf("prepare empty directory %s created=%v err=%v", directory.path, created, err)
		}
	}
	releaseReceipts := filepath.Join(layout.SystemStateDirectory, "release-install")
	bootstrapTestDirectory(t, releaseReceipts, 0o700)
	if err := os.WriteFile(filepath.Join(releaseReceipts, "current.json"), []byte("historical receipt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrapHost(hostBootstrapOptions{
		Layout: layout, PublicListen: "0.0.0.0:8443", Username: "admin", Password: []byte("private password"),
		ServiceUID: uid, ServiceGID: gid, RootUID: uid, RootGID: gid,
		Hostname: func() (string, error) { return "mdd-test", nil },
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{layout.authPath(), layout.tlsCertPath(), layout.tlsKeyPath(), layout.configPath()} {
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("generated file %s info=%v err=%v", path, info, err)
		}
	}
}

func TestAtomicBootstrapCreateDoesNotReplaceExistingFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "auth.json")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	published, err := atomicCreateBootstrapFile(path, []byte("replacement"), os.Getuid(), os.Getgid())
	if err == nil || published {
		t.Fatalf("published=%v err=%v", published, err)
	}
	payload, readErr := os.ReadFile(path)
	if readErr != nil || string(payload) != "original" {
		t.Fatalf("payload=%q err=%v", payload, readErr)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestBootstrapPasswordComesFromOneBoundedStdinLine(t *testing.T) {
	password, err := readBootstrapPassword(strings.NewReader("private value\r\n"))
	if err != nil || string(password) != "private value" {
		t.Fatalf("password=%q err=%v", password, err)
	}
	clear(password)
	for _, input := range []string{"", "\n", strings.Repeat("x", 257) + "\n"} {
		if _, err := readBootstrapPassword(strings.NewReader(input)); err == nil {
			t.Fatalf("invalid input of length %d was accepted", len(input))
		}
	}
	if err := runBootstrapHost([]string{"-password=secret"}, strings.NewReader("ignored\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("password argv flag was accepted")
	}
}

func bootstrapTestLayout(t *testing.T) (hostBootstrapLayout, int, int) {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "etc"), filepath.Join(root, "var"), filepath.Join(root, "var", "lib")} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	layout := hostBootstrapLayout{
		ConfigDirectory: filepath.Join(root, "etc", "mdd"), StateDirectory: filepath.Join(root, "var", "lib", "mdd"),
		SystemStateDirectory:  filepath.Join(root, "var", "lib", "mdd-system"),
		EgressConfigDirectory: filepath.Join(root, "var", "lib", "mdd-egress-config"),
		RuntimeDirectory:      filepath.Join(root, "run", "mdd"), EgressRuntimeDirectory: filepath.Join(root, "run", "mdd-core-egress"),
		CurrentRelease: filepath.Join(root, "usr", "lib", "mdd", "current"), SystemctlPath: filepath.Join(root, "bin", "systemctl"),
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid = 1
	}
	if gid == 0 {
		gid = 1
	}
	return layout, uid, gid
}

type bootstrapReadCounter struct{ reads int }

func (counter *bootstrapReadCounter) Read([]byte) (int, error) {
	counter.reads++
	return 0, io.ErrUnexpectedEOF
}

func bootstrapTestDirectory(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func bootstrapTestFile(t *testing.T, path string) {
	t.Helper()
	bootstrapTestDirectory(t, filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte("orphaned state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func bootstrapTestSymlink(t *testing.T, path string) {
	t.Helper()
	bootstrapTestDirectory(t, filepath.Dir(path), 0o700)
	if err := os.Symlink(filepath.Join(filepath.Dir(path), "missing-target"), path); err != nil {
		t.Fatal(err)
	}
}
