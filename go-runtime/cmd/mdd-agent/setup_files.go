package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

type setupPending struct {
	Version        int    `json:"version"`
	ConfigPath     string `json:"config_path"`
	EnrollmentID   string `json:"enrollment_id"`
	OriginalSHA256 string `json:"original_sha256,omitempty"`
	Requested      bool   `json:"requested"`
	Settings       config `json:"settings"`
}

type setupTransaction struct {
	path    string
	lock    *os.File
	pending setupPending
}

func readPendingSetup(path string) (setupPending, error) {
	if !filepath.IsAbs(path) {
		return setupPending{}, os.ErrNotExist
	}
	payload, err := readSetupFile(path + ".setup-pending")
	if err != nil {
		return setupPending{}, err
	}
	var pending setupPending
	if err := strictSetupJSON(payload, &pending); err != nil {
		return setupPending{}, err
	}
	if pending.Version != 1 || len(pending.EnrollmentID) != 32 || pending.ConfigPath != filepath.Clean(path) {
		return setupPending{}, errors.New("invalid pending setup")
	}
	return pending, nil
}

func configDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func setupDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if err = protectSetupPath(path); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("setup directory must be a real directory")
	}
	if err = validateConfigOwner(info); err != nil {
		return err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
		return errors.New("setup directory is writable by other users")
	}
	file, err := os.CreateTemp(path, ".mdd-write-check-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	return errors.Join(file.Sync(), file.Close())
}

func lockConfigMutation(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("configuration path must be absolute")
	}
	if err := setupDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".setup-lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockSetupFile(lock); err != nil {
		lock.Close()
		return nil, errors.New("another configuration setup is using this path")
	}
	return lock, nil
}

func beginSetup(path string) (*setupTransaction, error) {
	lock, err := lockConfigMutation(path)
	if err != nil {
		return nil, err
	}
	tx := &setupTransaction{path: path, lock: lock}
	ok := false
	defer func() {
		if !ok {
			tx.close()
		}
	}()
	state := filepath.Join(filepath.Dir(path), "state")
	if err = setupDirectory(state); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	if _, err = os.Lstat(path + ".setup-pending"); err == nil {
		tx.pending, err = readPendingSetup(path)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		settings, err := readConfigForEdit(path, false)
		if errors.Is(err, os.ErrNotExist) {
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 0 {
				return nil, errors.New("existing state requires its original configuration; setup will not replace it")
			}
			settings, err = newEditableConfig()
			if err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		} else {
			if settings.validate() == nil {
				return nil, errors.New("configuration is already complete; no credential was issued")
			}
			payload, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			tx.pending.OriginalSHA256 = configDigest(payload)
		}
		tx.pending.Version = 1
		tx.pending.ConfigPath = filepath.Clean(path)
		tx.pending.Settings = settings
		tx.pending.EnrollmentID, err = randomHex(16)
		if err != nil {
			return nil, err
		}
	}
	// Publication may have succeeded before directory sync or pending cleanup
	// failed. Only our exact valid final bytes may complete this receipt.
	if tx.pending.Requested && tx.pending.Settings.validate() == nil {
		payload, marshalErr := json.MarshalIndent(tx.pending.Settings, "", "  ")
		actual, readErr := readSetupFile(path)
		if marshalErr == nil && readErr == nil && bytes.Equal(actual, append(payload, '\n')) {
			tx.pending.OriginalSHA256 = configDigest(actual)
		}
	}
	if err = tx.checkOriginal(); err != nil {
		return nil, err
	}
	ok = true
	return tx, nil
}

func (tx *setupTransaction) close() {
	if tx != nil && tx.lock != nil {
		_ = tx.lock.Close()
		tx.lock = nil
	}
}

func (tx *setupTransaction) checkOriginal() error {
	info, err := os.Lstat(tx.path)
	if tx.pending.OriginalSHA256 == "" {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("configuration appeared during setup; refusing to overwrite it")
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("original configuration is unavailable or changed")
	}
	payload, err := os.ReadFile(tx.path)
	if err != nil || configDigest(payload) != tx.pending.OriginalSHA256 {
		return errors.New("configuration changed during setup; no file was overwritten")
	}
	return nil
}

func (tx *setupTransaction) savePending() error {
	payload, err := json.MarshalIndent(tx.pending, "", "  ")
	if err != nil {
		return err
	}
	return writeSetupFile(tx.path+".setup-pending", append(payload, '\n'), false, "")
}

func (tx *setupTransaction) publish() (config, error) {
	settings := tx.pending.Settings
	if err := settings.validate(); err != nil {
		return config{}, err
	}
	if err := tx.checkOriginal(); err != nil {
		return config{}, err
	}
	payload, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return config{}, err
	}
	if err = writeSetupFile(tx.path, append(payload, '\n'), tx.pending.OriginalSHA256 == "", ""); err != nil {
		return config{}, err
	}
	settings.configPath = tx.path
	_ = os.Remove(tx.path + ".setup-pending")
	return settings, nil
}

func writeSetupFile(path string, payload []byte, exclusive bool, preserveAccessFrom string) error {
	file, err := createSetupTemp(filepath.Dir(path), preserveAccessFrom)
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	if _, err = file.Write(payload); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if exclusive {
		err = os.Link(name, path)
	} else {
		err = os.Rename(name, path)
	}
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func readSetupFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumAgentConfigBytes {
		return nil, errors.New("invalid setup file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		return nil, errors.New("setup file must have permissions 0600")
	}
	if err = validateConfigOwner(info); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func strictSetupJSON(payload []byte, target any) error {
	if len(payload) > maximumAgentConfigBytes {
		return errors.New("setup input is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("setup input has trailing JSON")
	}
	return nil
}
