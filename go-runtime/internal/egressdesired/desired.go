// Package egressdesired renders and atomically publishes the narrow legacy
// desired-state contract still consumed by the host network orchestrator.
package egressdesired

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const maximumDocumentBytes = 1 << 20

var ErrRuntimeConfirmationTimeout = errors.New("country exit runtime confirmation timed out")

type Line struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	MCC     string `json:"mcc"`
	MNC     string `json:"mnc"`
	Country string `json:"country"`
	EPDG    string `json:"epdg"`
}

type Document struct {
	Version              int                 `json:"version"`
	Proxy                egressconfig.Config `json:"proxy"`
	Hardware             json.RawMessage     `json:"hardware"`
	Lines                []Line              `json:"lines"`
	EgressConfigRevision uint64              `json:"egress_config_revision"`
	CatalogRevision      uint64              `json:"catalog_revision"`
	Generation           string              `json:"generation"`
	UpdatedAt            int64               `json:"updated_at"`
}

type Applied struct {
	ConfigRevision  uint64
	CatalogRevision uint64
	Generation      string
}

func Render(config egressconfig.Snapshot, catalog linecatalog.Snapshot, hardware json.RawMessage, now time.Time) (Document, error) {
	if config.SchemaVersion != egressconfig.SchemaVersion || config.Revision == 0 ||
		catalog.SchemaVersion != linecatalog.SchemaVersion || catalog.Revision == 0 || len(hardware) == 0 {
		return Document{}, errors.New("invalid country exit desired input")
	}
	var hardwareObject map[string]json.RawMessage
	if json.Unmarshal(hardware, &hardwareObject) != nil {
		return Document{}, errors.New("legacy hardware desired state is invalid")
	}
	lines := make([]Line, 0, len(catalog.Lines))
	for _, line := range catalog.Lines {
		if strings.TrimSpace(line.ID) == "" {
			return Document{}, errors.New("line catalog contains an invalid line")
		}
		country := strings.ToLower(strings.TrimSpace(line.Network.EgressCountry))
		if line.Enabled && len(country) != 2 {
			return Document{}, fmt.Errorf("enabled line %q has no effective egress country", line.ID)
		}
		lines = append(lines, Line{
			ID: line.ID, Name: line.Name, Enabled: line.Enabled, MCC: line.SIM.MCC, MNC: line.SIM.MNC,
			Country: country, EPDG: epdgFor(line),
		})
	}
	canonical := map[string]any{
		"version": 1, "proxy": config.Config, "hardware": hardwareObject, "lines": lines,
		"egress_config_revision": config.Revision, "catalog_revision": catalog.Revision,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return Document{}, err
	}
	digest := sha256.Sum256(payload)
	return Document{
		Version: 1, Proxy: config.Config, Hardware: append(json.RawMessage(nil), hardware...), Lines: lines,
		EgressConfigRevision: config.Revision, CatalogRevision: catalog.Revision,
		Generation: hex.EncodeToString(digest[:]), UpdatedAt: now.UTC().Unix(),
	}, nil
}

func Read(path string) (Document, error) {
	var document Document
	payload, err := readRegular(path)
	if err != nil {
		return document, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&document); err != nil {
		return document, fmt.Errorf("decode country exit desired state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || document.Version != 1 || len(document.Hardware) == 0 {
		return Document{}, errors.New("country exit desired state is invalid")
	}
	return document, nil
}

func Publish(path string, document Document) (bool, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) || len(document.Generation) != 64 {
		return false, errors.New("invalid country exit desired publication")
	}
	if current, err := Read(path); err == nil {
		if current.Generation == document.Generation && current.EgressConfigRevision == document.EgressConfigRevision &&
			current.CatalogRevision == document.CatalogRevision {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil || len(payload) > maximumDocumentBytes {
		return false, errors.New("country exit desired document is too large")
	}
	payload = append(payload, '\n')
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("country exit desired directory is invalid")
	}
	temporary, err := os.CreateTemp(directory, ".desired-*.json")
	if err != nil {
		return false, err
	}
	temporaryName := temporary.Name()
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := errors.Join(temporary.Sync(), temporary.Close()); err != nil {
		return false, err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return false, err
	}
	complete = true
	dir, err := os.Open(directory)
	if err != nil {
		return false, err
	}
	return true, errors.Join(dir.Sync(), dir.Close())
}

func CurrentApplied(path string) (Applied, json.RawMessage, error) {
	document, err := Read(path)
	if err != nil {
		return Applied{}, nil, err
	}
	return Applied{
		ConfigRevision: document.EgressConfigRevision, CatalogRevision: document.CatalogRevision,
		Generation: document.Generation,
	}, append(json.RawMessage(nil), document.Hardware...), nil
}

func StatusGeneration(path string) (string, error) {
	payload, err := readRegular(path)
	if err != nil {
		return "", err
	}
	var status struct {
		DesiredGeneration string `json:"desired_generation"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if decoder.Decode(&status) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(status.DesiredGeneration) != 64 {
		return "", errors.New("country exit runtime status has no valid desired generation")
	}
	return status.DesiredGeneration, nil
}

func WaitForRuntime(ctx context.Context, path, generation string, timeout, interval time.Duration) error {
	if ctx == nil || len(generation) != 64 || timeout <= 0 || interval <= 0 {
		return errors.New("invalid country exit runtime confirmation request")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if current, err := StatusGeneration(path); err == nil && current == generation {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrRuntimeConfirmationTimeout
		case <-ticker.C:
		}
	}
}

func readRegular(path string) ([]byte, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return nil, errors.New("country exit state path is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumDocumentBytes {
		return nil, errors.New("country exit state must be a non-empty regular file no larger than 1 MiB")
	}
	return os.ReadFile(path)
}

func epdgFor(line linecatalog.Line) string {
	if value := strings.TrimSpace(line.Network.EPDGAddress); value != "" {
		return value
	}
	mcc, mnc := strings.TrimSpace(line.SIM.MCC), strings.TrimSpace(line.SIM.MNC)
	if mcc == "" || mnc == "" {
		return ""
	}
	for len(mcc) < 3 {
		mcc = "0" + mcc
	}
	for len(mnc) < 3 {
		mnc = "0" + mnc
	}
	return "epdg.epc.mnc" + mnc + ".mcc" + mcc + ".pub.3gppnetwork.org"
}
