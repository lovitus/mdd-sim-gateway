//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
)

const maximumBootstrapPasswordBytes = 4 << 10

type hostBootstrapLayout struct {
	ConfigDirectory        string
	StateDirectory         string
	SystemStateDirectory   string
	EgressConfigDirectory  string
	RuntimeDirectory       string
	EgressRuntimeDirectory string
	CurrentRelease         string
	SystemctlPath          string
}

func defaultHostBootstrapLayout() hostBootstrapLayout {
	return hostBootstrapLayout{
		ConfigDirectory:        "/etc/mdd",
		StateDirectory:         "/var/lib/mdd",
		SystemStateDirectory:   "/var/lib/mdd-system",
		EgressConfigDirectory:  "/var/lib/mdd-egress-config",
		RuntimeDirectory:       "/run/mdd",
		EgressRuntimeDirectory: "/run/mdd-core-egress",
		CurrentRelease:         "/usr/lib/mdd/current",
		SystemctlPath:          "/bin/systemctl",
	}
}

func (layout hostBootstrapLayout) configPath() string {
	return filepath.Join(layout.ConfigDirectory, "core.json")
}

func (layout hostBootstrapLayout) authPath() string {
	return filepath.Join(layout.ConfigDirectory, "auth.json")
}

func (layout hostBootstrapLayout) tlsDirectory() string {
	return filepath.Join(layout.ConfigDirectory, "tls")
}

func (layout hostBootstrapLayout) tlsCertPath() string {
	return filepath.Join(layout.tlsDirectory(), "server.crt")
}

func (layout hostBootstrapLayout) tlsKeyPath() string {
	return filepath.Join(layout.tlsDirectory(), "server.key")
}

func (layout hostBootstrapLayout) providerCandidateRoot() string {
	return filepath.Join(layout.ConfigDirectory, "providers", "releases")
}

func (layout hostBootstrapLayout) providerReceiptPath() string {
	return filepath.Join(layout.SystemStateDirectory, "provider-apply")
}

type hostBootstrapOptions struct {
	Layout       hostBootstrapLayout
	PublicListen string
	Username     string
	Password     []byte
	ServiceUID   int
	ServiceGID   int
	RootUID      int
	RootGID      int
	Entropy      io.Reader
	Now          func() time.Time
	Hostname     func() (string, error)
}

type hostBootstrapReceipt struct {
	Status               string `json:"status"`
	ConfigPath           string `json:"config_path"`
	AgentTokenPath       string `json:"agent_token_path"`
	AgentTokenJSONField  string `json:"agent_token_json_field"`
	AgentTokenReadAs     string `json:"agent_token_read_as"`
	TLSCertificateSHA256 string `json:"tls_certificate_sha256"`
	TLSSPKISHA256        string `json:"tls_spki_sha256"`
}

func runBootstrapHost(arguments []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("bootstrap-host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	publicListen := flags.String("public-listen", "0.0.0.0:8443", "public HTTPS/WSS listen address")
	username := flags.String("username", "admin", "administrator username")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("usage: mdd-core bootstrap-host [-public-listen address] [-username name] < password")
	}
	if runtime.GOOS != "linux" {
		return errors.New("host bootstrap is supported only on Linux")
	}
	if err := requireProviderApplyPrivileges(); err != nil {
		return errors.New("host bootstrap requires root")
	}
	uid, gid, err := providerServiceIdentity("mdd")
	if err != nil {
		return errors.New("mdd service account was not found; install the release before bootstrapping the host")
	}
	password, err := readBootstrapPassword(input)
	if err != nil {
		return err
	}
	defer clear(password)
	receipt, err := bootstrapHost(hostBootstrapOptions{
		Layout: defaultHostBootstrapLayout(), PublicListen: *publicListen, Username: *username,
		Password: password, ServiceUID: uid, ServiceGID: gid, RootUID: 0, RootGID: 0,
		Entropy: cryptorand.Reader, Now: time.Now, Hostname: os.Hostname,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(receipt)
}

func readBootstrapPassword(input io.Reader) ([]byte, error) {
	if input == nil {
		return nil, errors.New("administrator password is required on stdin")
	}
	reader := bufio.NewReader(io.LimitReader(input, maximumBootstrapPasswordBytes+2))
	password, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("read administrator password from stdin")
	}
	if len(password) > maximumBootstrapPasswordBytes+1 {
		clear(password)
		return nil, errors.New("administrator password from stdin is too large")
	}
	password = bytes.TrimSuffix(password, []byte{'\n'})
	password = bytes.TrimSuffix(password, []byte{'\r'})
	if len(password) == 0 || !utf8.Valid(password) || utf8.RuneCount(password) > 256 {
		clear(password)
		return nil, errors.New("administrator password must contain 1 to 256 valid UTF-8 characters")
	}
	return password, nil
}

func bootstrapHost(options hostBootstrapOptions) (hostBootstrapReceipt, error) {
	var receipt hostBootstrapReceipt
	if options.Entropy == nil {
		options.Entropy = cryptorand.Reader
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Hostname == nil {
		options.Hostname = os.Hostname
	}
	if options.ServiceUID < 1 || options.ServiceGID < 1 || options.RootUID < 0 || options.RootGID < 0 {
		return receipt, errors.New("bootstrap ownership is invalid")
	}
	if err := options.Layout.validate(); err != nil {
		return receipt, err
	}
	targets := []string{
		options.Layout.authPath(), options.Layout.tlsCertPath(), options.Layout.tlsKeyPath(), options.Layout.configPath(),
	}
	for _, target := range targets {
		if _, err := os.Lstat(target); err == nil {
			return receipt, fmt.Errorf("bootstrap target already exists: %s", target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return receipt, err
		}
	}
	if err := validateEmptyBootstrapState(options.Layout); err != nil {
		return receipt, err
	}

	agentToken, err := bootstrapToken(options.Entropy)
	if err != nil {
		return receipt, err
	}
	localToken, err := bootstrapToken(options.Entropy)
	if err != nil {
		return receipt, err
	}
	authPayload, err := adminauth.MarshalBootstrapCredential(options.Username, options.Password, agentToken, options.Entropy)
	if err != nil {
		return receipt, err
	}
	certificatePayload, keyPayload, certificateFingerprint, spkiPin, err := bootstrapTLSIdentity(
		options.Entropy, options.Now().UTC(), bootstrapHostname(options.Hostname),
	)
	if err != nil {
		return receipt, err
	}
	settings := options.Layout.coreConfig(strings.TrimSpace(options.PublicListen), localToken)
	if err := settings.validate(); err != nil {
		return receipt, fmt.Errorf("bootstrap configuration: %w", err)
	}
	// The notification DB has a deterministic events-directory default. Keep
	// the fresh-host JSON readable by the immediately preceding strict Core so
	// switching the immutable release link remains a valid rollback.
	settings.NotificationsPath = ""
	configPayload, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return receipt, err
	}
	configPayload = append(configPayload, '\n')

	directories := []bootstrapDirectory{
		{options.Layout.ConfigDirectory, 0o755, options.RootUID, options.RootGID},
		{filepath.Join(options.Layout.ConfigDirectory, "providers"), 0o755, options.RootUID, options.RootGID},
		{options.Layout.providerCandidateRoot(), 0o755, options.RootUID, options.RootGID},
		{options.Layout.StateDirectory, 0o700, options.ServiceUID, options.ServiceGID},
		{filepath.Join(options.Layout.StateDirectory, "providers"), 0o700, options.ServiceUID, options.ServiceGID},
		{options.Layout.SystemStateDirectory, 0o700, options.RootUID, options.RootGID},
		{options.Layout.providerReceiptPath(), 0o700, options.RootUID, options.RootGID},
		{options.Layout.EgressConfigDirectory, 0o750, options.RootUID, options.ServiceGID},
		{options.Layout.tlsDirectory(), 0o700, options.ServiceUID, options.ServiceGID},
	}
	createdDirectories := make([]string, 0, len(directories))
	createdFiles := make([]string, 0, len(targets))
	committed := false
	defer func() {
		if committed {
			return
		}
		for index := len(createdFiles) - 1; index >= 0; index-- {
			_ = os.Remove(createdFiles[index])
			_ = syncBootstrapDirectory(filepath.Dir(createdFiles[index]))
		}
		for index := len(createdDirectories) - 1; index >= 0; index-- {
			_ = os.Remove(createdDirectories[index])
		}
	}()
	for _, directory := range directories {
		created, err := ensureBootstrapDirectory(directory)
		if err != nil {
			return receipt, err
		}
		if created {
			createdDirectories = append(createdDirectories, directory.path)
		}
	}
	files := []struct {
		path    string
		payload []byte
	}{
		{options.Layout.authPath(), authPayload},
		{options.Layout.tlsCertPath(), certificatePayload},
		{options.Layout.tlsKeyPath(), keyPayload},
		// The configuration is the final publication marker. A service cannot
		// start from a partially published identity or credential set.
		{options.Layout.configPath(), configPayload},
	}
	for _, file := range files {
		published, err := atomicCreateBootstrapFile(file.path, file.payload, options.ServiceUID, options.ServiceGID)
		if published {
			createdFiles = append(createdFiles, file.path)
		}
		if err != nil {
			return receipt, err
		}
	}
	committed = true
	receipt = hostBootstrapReceipt{
		Status: "created", ConfigPath: options.Layout.configPath(),
		AgentTokenPath: options.Layout.authPath(), AgentTokenJSONField: "agent_token",
		AgentTokenReadAs: "root", TLSCertificateSHA256: certificateFingerprint, TLSSPKISHA256: spkiPin,
	}
	return receipt, nil
}

func validateEmptyBootstrapState(layout hostBootstrapLayout) error {
	for _, name := range []string{
		"events.db", "messages.db", "messages.db.cellular-operations", "calls.db", "catalog.db", "egress.db",
		"allowance.db", "notifications.db", "egress-ipc-token",
	} {
		path := filepath.Join(layout.StateDirectory, name)
		if _, err := os.Lstat(path); err == nil {
			return orphanedBootstrapState(path, nil)
		} else if !errors.Is(err, os.ErrNotExist) {
			return orphanedBootstrapState(path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(layout.EgressConfigDirectory, "desired.json"),
		filepath.Join(layout.ConfigDirectory, "providers-current"),
	} {
		if _, err := os.Lstat(path); err == nil {
			return orphanedBootstrapState(path, nil)
		} else if !errors.Is(err, os.ErrNotExist) {
			return orphanedBootstrapState(path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(layout.StateDirectory, "providers"),
		layout.providerCandidateRoot(),
	} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return orphanedBootstrapState(path, err)
		}
		directory, err := os.Open(path)
		if err != nil {
			return orphanedBootstrapState(path, err)
		}
		_, readErr := directory.Readdirnames(1)
		closeErr := directory.Close()
		if readErr == nil {
			return orphanedBootstrapState(path, nil)
		}
		if !errors.Is(readErr, io.EOF) {
			return orphanedBootstrapState(path, readErr)
		}
		if closeErr != nil {
			return orphanedBootstrapState(path, closeErr)
		}
	}
	return nil
}

func orphanedBootstrapState(path string, cause error) error {
	message := fmt.Sprintf(
		"existing durable MDD state at %s blocks host bootstrap; restore its matching core configuration or explicitly archive/remove the state before retrying",
		path,
	)
	if cause != nil {
		return fmt.Errorf("%s: %w", message, cause)
	}
	return errors.New(message)
}

func (layout hostBootstrapLayout) validate() error {
	paths := []string{
		layout.ConfigDirectory, layout.StateDirectory, layout.SystemStateDirectory, layout.EgressConfigDirectory,
		layout.RuntimeDirectory, layout.EgressRuntimeDirectory, layout.CurrentRelease, layout.SystemctlPath,
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return errors.New("bootstrap paths must be clean, absolute, and scoped")
		}
	}
	return nil
}

func (layout hostBootstrapLayout) coreConfig(publicListen, localToken string) config {
	settings := config{}
	settings.Public.Listen = publicListen
	settings.Public.TLSCert = layout.tlsCertPath()
	settings.Public.TLSKey = layout.tlsKeyPath()
	settings.Local.Listen = "127.0.0.1:19444"
	settings.Local.Token = localToken
	settings.AuthPath = layout.authPath()
	settings.EventsPath = filepath.Join(layout.StateDirectory, "events.db")
	settings.MessagesPath = filepath.Join(layout.StateDirectory, "messages.db")
	settings.CallsPath = filepath.Join(layout.StateDirectory, "calls.db")
	settings.CatalogPath = filepath.Join(layout.StateDirectory, "catalog.db")
	settings.EgressPath = filepath.Join(layout.StateDirectory, "egress.db")
	settings.AllowancePath = filepath.Join(layout.StateDirectory, "allowance.db")
	settings.ProviderApply.Enabled = true
	settings.ProviderApply.SocketPath = filepath.Join(layout.RuntimeDirectory, "provider-apply.sock")
	settings.ProviderApply.CandidateRoot = layout.providerCandidateRoot()
	settings.ProviderApply.StatePath = filepath.Join(layout.StateDirectory, "providers")
	settings.ProviderApply.EgressStatusPath = filepath.Join(layout.EgressRuntimeDirectory, "proxy-status.json")
	settings.ProviderApply.EgressDesiredPath = filepath.Join(layout.EgressConfigDirectory, "desired.json")
	settings.ProviderApply.CurrentLink = filepath.Join(layout.ConfigDirectory, "providers-current")
	settings.ProviderApply.ReceiptPath = layout.providerReceiptPath()
	settings.ProviderApply.ProviderBinary = filepath.Join(layout.CurrentRelease, "mdd-vowifi")
	settings.ProviderApply.ProviderUser = "mdd"
	settings.ProviderApply.SystemctlPath = layout.SystemctlPath
	settings.TTLSeconds = 30
	return settings
}

func bootstrapToken(entropy io.Reader) (string, error) {
	payload := make([]byte, 32)
	if _, err := io.ReadFull(entropy, payload); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func bootstrapTLSIdentity(entropy io.Reader, now time.Time, hostname string) ([]byte, []byte, string, string, error) {
	public, private, err := ed25519.GenerateKey(entropy)
	if err != nil {
		return nil, nil, "", "", err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := cryptorand.Int(entropy, serialLimit)
	if err != nil {
		return nil, nil, "", "", err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	dnsNames := []string{"localhost"}
	if hostname = validBootstrapHostname(hostname); hostname != "" && hostname != "localhost" {
		dnsNames = append(dnsNames, hostname)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "MDD Core"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	certificateDER, err := x509.CreateCertificate(entropy, template, template, public, private)
	if err != nil {
		return nil, nil, "", "", err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, nil, "", "", err
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return nil, nil, "", "", err
	}
	certificateFingerprint, spkiPin := bootstrapCertificatePins(certificateDER, publicKeyDER)
	certificatePayload := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	keyPayload := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if len(certificatePayload) == 0 || len(keyPayload) == 0 {
		return nil, nil, "", "", errors.New("encode bootstrap TLS identity")
	}
	return certificatePayload, keyPayload, certificateFingerprint, spkiPin, nil
}

func validBootstrapHostname(hostname string) string {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	hostname = strings.TrimSuffix(hostname, ".")
	if hostname == "" || len(hostname) > 253 || net.ParseIP(hostname) != nil {
		return ""
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return ""
		}
	}
	return hostname
}

func bootstrapHostname(provider func() (string, error)) string {
	if provider == nil {
		return ""
	}
	hostname, err := provider()
	if err != nil {
		return ""
	}
	return validBootstrapHostname(hostname)
}

type bootstrapDirectory struct {
	path     string
	mode     os.FileMode
	uid, gid int
}

func ensureBootstrapDirectory(directory bootstrapDirectory) (bool, error) {
	info, err := os.Lstat(directory.path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory.path, directory.mode); err != nil {
			return false, err
		}
		if err := os.Chown(directory.path, directory.uid, directory.gid); err != nil {
			_ = os.Remove(directory.path)
			return false, err
		}
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			_ = os.Remove(directory.path)
			return false, err
		}
		if err := syncBootstrapDirectory(filepath.Dir(directory.path)); err != nil {
			_ = os.Remove(directory.path)
			return false, err
		}
		return true, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != directory.mode ||
		unixUID(info) != uint32(directory.uid) || unixGID(info) != uint32(directory.gid) {
		return false, fmt.Errorf("bootstrap directory is invalid: %s", directory.path)
	}
	return false, nil
}

func atomicCreateBootstrapFile(path string, payload []byte, uid, gid int) (published bool, returnErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mdd-bootstrap-*")
	if err != nil {
		return false, err
	}
	name := temporary.Name()
	defer func() {
		if err := os.Remove(name); !errors.Is(err, os.ErrNotExist) && returnErr == nil {
			returnErr = err
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if written, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return false, err
	} else if written != len(payload) {
		_ = temporary.Close()
		return false, io.ErrShortWrite
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	// Hard-link publication is atomic on the same filesystem and, unlike
	// Rename, fails rather than replacing a target created after preflight.
	if err := os.Link(name, path); err != nil {
		if _, statErr := os.Lstat(path); statErr == nil {
			return false, fmt.Errorf("bootstrap target already exists: %s", path)
		}
		return false, err
	}
	published = true
	if err := os.Chown(path, uid, gid); err != nil {
		return true, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return true, err
	}
	if err := syncBootstrapFile(path); err != nil {
		return true, err
	}
	if err := os.Remove(name); err != nil {
		return true, err
	}
	if err := syncBootstrapDirectory(filepath.Dir(path)); err != nil {
		return true, err
	}
	return true, nil
}

func syncBootstrapFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	err = file.Sync()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func syncBootstrapDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}

func bootstrapCertificatePins(certificateDER, publicKeyDER []byte) (string, string) {
	certificateHash := sha256.Sum256(certificateDER)
	publicKeyHash := sha256.Sum256(publicKeyDER)
	return hex.EncodeToString(certificateHash[:]), "sha256/" + base64.StdEncoding.EncodeToString(publicKeyHash[:])
}
