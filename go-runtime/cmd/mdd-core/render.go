package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
)

func runProviderRender(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("render-provider-configs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to the 0600 mdd-core JSON configuration")
	outputPath := flags.String("output", "", "new directory for rendered provider configurations")
	statePath := flags.String("state-dir", "", "absolute provider state directory")
	egressStatusPath := flags.String("egress-status", "", "path to the host country-exit status JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*outputPath) == "" ||
		strings.TrimSpace(*statePath) == "" || strings.TrimSpace(*egressStatusPath) == "" || flags.NArg() != 0 {
		return errors.New("-config, -output, -state-dir, and -egress-status are required")
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	coreAddress, err := providerCoreAddress(settings.Local.Listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	snapshot, err := linecatalog.FetchSnapshot(ctx, coreAddress+linecatalog.SnapshotIPCPath, settings.Local.Token,
		&http.Client{Transport: &http.Transport{Proxy: nil}})
	cancel()
	if err != nil {
		return err
	}
	exits, err := egressstatus.Load(*egressStatusPath)
	if err != nil {
		return err
	}
	manifest, err := renderProviderDirectory(settings, snapshot, exits, *outputPath, *statePath)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{
		"status": "rendered", "catalog_revision": manifest.CatalogRevision,
		"providers": len(manifest.Providers), "output": filepath.Clean(*outputPath),
	})
}

func renderProviderDirectory(settings config, snapshot linecatalog.Snapshot, exits egressstatus.Snapshot, outputPath, statePath string) (providerconfig.Manifest, error) {
	var empty providerconfig.Manifest
	outputPath = filepath.Clean(strings.TrimSpace(outputPath))
	statePath = filepath.Clean(strings.TrimSpace(statePath))
	if !filepath.IsAbs(outputPath) || !filepath.IsAbs(statePath) || outputPath == string(filepath.Separator) {
		return empty, errors.New("provider output and state directories must be absolute and scoped")
	}
	if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return empty, errors.New("provider output directory already exists")
		}
		return empty, err
	}
	coreAddress, err := providerCoreAddress(settings.Local.Listen)
	if err != nil {
		return empty, err
	}
	manifest := providerconfig.Manifest{SchemaVersion: 1, CatalogRevision: snapshot.Revision, Providers: []providerconfig.ManifestEntry{}}
	type artifact struct {
		name    string
		payload []byte
	}
	artifacts := make([]artifact, 0, len(snapshot.Lines)+1)
	for _, line := range snapshot.Lines {
		if !line.Enabled {
			continue
		}
		instance := providerconfig.UnitInstance(line.ID)
		provider := providerConfigForLine(settings, line, coreAddress, statePath, instance)
		proxyURL, err := exits.ProxyURL(line.Network.EgressCountry)
		if err != nil {
			return empty, fmt.Errorf("line %q egress: %w", line.ID, err)
		}
		provider.Network.ProxyURL = proxyURL
		if err := provider.Validate(); err != nil {
			return empty, fmt.Errorf("line %q provider config: %w", line.ID, err)
		}
		payload, err := json.MarshalIndent(provider, "", "  ")
		if err != nil {
			return empty, err
		}
		name := instance + ".json"
		payload = append(payload, '\n')
		digest := sha256.Sum256(payload)
		artifacts = append(artifacts, artifact{name: name, payload: payload})
		manifest.Providers = append(manifest.Providers, providerconfig.ManifestEntry{
			LineID: line.ID, UnitInstance: instance, ConfigFile: name, ConfigSHA256: hex.EncodeToString(digest[:]),
		})
	}
	manifestPayload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return empty, err
	}
	artifacts = append(artifacts, artifact{name: "manifest.json", payload: append(manifestPayload, '\n')})
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return empty, err
	}
	if err := os.Mkdir(outputPath, 0o700); err != nil {
		return empty, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(outputPath)
		}
	}()
	for _, item := range artifacts {
		if err := writeSyncedFile(filepath.Join(outputPath, item.name), item.payload); err != nil {
			return empty, err
		}
	}
	directory, err := os.Open(outputPath)
	if err != nil {
		return empty, err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err != nil {
		return empty, err
	}
	complete = true
	return manifest, nil
}

func providerConfigForLine(settings config, line linecatalog.Line, coreAddress, statePath, instance string) providerconfig.Config {
	var result providerconfig.Config
	result.LineID = line.ID
	result.ProviderID = "native"
	result.DeviceID = "mdd-vowifi-" + instance
	result.IPC.Listen = "127.0.0.1:0"
	result.IPC.Token = providerToken(settings.Local.Token, line.ID)
	result.IPC.StatePath = filepath.Join(statePath, instance+".db")
	result.Core.RegistrationURL = coreAddress + "/v1/media/providers"
	result.Core.RegistrationToken = settings.Local.Token
	result.Core.RefreshMS = 10_000
	result.Agent.BrokerURL = coreAddress + "/v1/agent/aka"
	result.Agent.BrokerToken = settings.Local.Token
	result.Agent.CardID = line.CardID
	result.SIM.IMSI, result.SIM.MCC, result.SIM.MNC = line.SIM.IMSI, line.SIM.MCC, line.SIM.MNC
	result.SIM.IMEI, result.SIM.SMSC = line.SIM.IMEI, line.SIM.SMSC
	result.Network.EPDGAddress = line.Network.EPDGAddress
	result.Network.PCSCF = append([]string(nil), line.Network.PCSCF...)
	result.IMS.IMPI, result.IMS.IMPU, result.IMS.Domain = line.IMS.IMPI, line.IMS.IMPU, line.IMS.Domain
	result.IMS.AKAAppPreference = line.IMS.AKAAppPreference
	result.IMS.Network, result.IMS.Server, result.IMS.Expires = line.IMS.Network, line.IMS.Server, line.IMS.Expires
	return result
}

func providerCoreAddress(listen string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", errors.New("invalid Core local listen address")
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

func providerToken(master, lineID string) string {
	mac := hmac.New(sha256.New, []byte(master))
	_, _ = mac.Write([]byte("mdd-provider-ipc-v1\x00" + lineID))
	return hex.EncodeToString(mac.Sum(nil))
}

func writeSyncedFile(path string, payload []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(payload)
	return errors.Join(writeErr, file.Sync(), file.Close())
}
