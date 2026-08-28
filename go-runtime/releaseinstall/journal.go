package releaseinstall

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type journal struct {
	directory string
	current   string
	receipt   Receipt
}

func openJournal(directory, releaseID, previous, candidate string) (*journal, error) {
	instance := &journal{directory: directory, current: filepath.Join(directory, "current.json")}
	if err := instance.archive(); err != nil {
		return nil, err
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	instance.receipt = Receipt{
		SchemaVersion: 1, ReceiptID: id, State: StateApplying, ReleaseID: releaseID,
		PreviousTarget: previous, CandidateTarget: candidate,
	}
	if err := instance.persist(); err != nil {
		return nil, err
	}
	return instance, nil
}

func (instance *journal) finish(state ReceiptState, code string) error {
	if instance == nil || (state != StateApplied && state != StateRolledBack && state != StateManualRecovery) {
		return errors.New("invalid release installation terminal state")
	}
	instance.receipt.State, instance.receipt.Code = state, code
	return instance.persist()
}

func (instance *journal) archive() error {
	info, err := os.Lstat(instance.current)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64<<10 {
		return ErrIncompleteInstall
	}
	payload, err := os.ReadFile(instance.current)
	if err != nil {
		return err
	}
	prior, err := decodeReceipt(payload)
	if err != nil || (prior.State != StateApplied && prior.State != StateRolledBack) || prior.ReceiptID == "" {
		return ErrIncompleteInstall
	}
	archive := filepath.Join(instance.directory, prior.ReceiptID+".json")
	if _, err := os.Lstat(archive); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("release installation receipt archive already exists or is unreadable")
	}
	if err := os.Rename(instance.current, archive); err != nil {
		return err
	}
	return syncDirectory(instance.directory)
}

func (instance *journal) persist() error {
	payload, err := json.MarshalIndent(instance.receipt, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(instance.directory, ".receipt-*.json")
	if err != nil {
		return err
	}
	path := temporary.Name()
	complete := false
	defer func() {
		_ = temporary.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(path, instance.current); err != nil {
		return err
	}
	complete = true
	return syncDirectory(instance.directory)
}

func readCurrentReceipt(directory string) (Receipt, error) {
	payload, err := os.ReadFile(filepath.Join(directory, "current.json"))
	if err != nil {
		return Receipt{}, err
	}
	return decodeReceipt(payload)
}

func decodeReceipt(payload []byte) (Receipt, error) {
	var receipt Receipt
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return Receipt{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || receipt.SchemaVersion != 1 ||
		receipt.ReceiptID == "" || receipt.ReleaseID == "" || receipt.CandidateTarget == "" {
		return Receipt{}, errors.New("invalid release installation receipt")
	}
	return receipt, nil
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "install-" + hex.EncodeToString(value), nil
}
