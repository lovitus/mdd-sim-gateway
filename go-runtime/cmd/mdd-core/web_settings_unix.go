//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
)

// Source contract: ec620942 control/run.py reads these fields at startup.
// Only this existing privileged helper writes them; no service restart occurs.
func (service *providerApplyService) WebSettings(ctx context.Context) (provideradmin.WebSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return provideradmin.WebSnapshot{}, err
	}
	_, _, snapshot, err := readWebConfig(service.configPath)
	return snapshot, err
}

func readWebConfig(path string) ([]byte, os.FileInfo, provideradmin.WebSnapshot, error) {
	var snapshot provideradmin.WebSnapshot
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, nil, snapshot, &provideradmin.Error{Status: 503, Code: "web_configuration_unavailable"}
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, snapshot, &provideradmin.Error{Status: 503, Code: "web_configuration_unavailable"}
	}
	var config struct {
		Public provideradmin.WebSettings `json:"public"`
	}
	if json.Unmarshal(payload, &config) != nil || config.Public.Validate() != nil {
		return nil, nil, snapshot, &provideradmin.Error{Status: 503, Code: "web_configuration_invalid"}
	}
	digest := sha256.Sum256(payload)
	snapshot = provideradmin.WebSnapshot{SchemaVersion: 1, Revision: hex.EncodeToString(digest[:]), Settings: config.Public}
	return payload, info, snapshot, nil
}

func (service *providerApplyService) SaveWebSettings(ctx context.Context, input provideradmin.WebRequest) (provideradmin.WebSnapshot, error) {
	service.mutation.Lock()
	defer service.mutation.Unlock()
	if err := ctx.Err(); err != nil {
		return provideradmin.WebSnapshot{}, err
	}
	if input.SchemaVersion != 1 || input.Settings.Validate() != nil {
		return provideradmin.WebSnapshot{}, &provideradmin.Error{Status: 400, Code: "invalid_web_settings_request"}
	}
	previous, info, current, err := readWebConfig(service.configPath)
	if err != nil {
		return current, err
	}
	if input.ExpectedRevision != current.Revision {
		return current, &provideradmin.Error{Status: 412, Code: "web_configuration_changed"}
	}
	if input.Settings == current.Settings {
		return current, nil
	}
	if _, err := tls.LoadX509KeyPair(input.Settings.TLSCert, input.Settings.TLSKey); err != nil {
		return current, &provideradmin.Error{Status: 400, Code: "web_tls_pair_unreadable_or_invalid"}
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(previous, &document); err != nil {
		return current, err
	}
	var public map[string]json.RawMessage
	if err := json.Unmarshal(document["public"], &public); err != nil {
		return current, err
	}
	for key, value := range map[string]string{"listen": input.Settings.Listen, "tls_cert": input.Settings.TLSCert, "tls_key": input.Settings.TLSKey} {
		public[key], _ = json.Marshal(value)
	}
	document["public"], _ = json.Marshal(public)
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return current, err
	}
	payload = append(payload, '\n')
	if err := writeWebConfig(service.configPath+".before-web-change", previous, info); err != nil {
		return current, &provideradmin.Error{Status: 500, Code: "web_configuration_backup_failed"}
	}
	latest, err := os.ReadFile(service.configPath)
	if err != nil || !bytes.Equal(latest, previous) {
		return current, &provideradmin.Error{Status: 412, Code: "web_configuration_changed"}
	}
	if err := writeWebConfig(service.configPath, payload, info); err != nil {
		return current, &provideradmin.Error{Status: 500, Code: "web_configuration_write_failed"}
	}
	_, _, result, err := readWebConfig(service.configPath)
	return result, err
}

func writeWebConfig(path string, payload []byte, original os.FileInfo) error {
	owner, ok := original.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("configuration ownership unavailable")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mdd-web-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(original.Mode().Perm()); err != nil {
		return err
	}
	if err := file.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
