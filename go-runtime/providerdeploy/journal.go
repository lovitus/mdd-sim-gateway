// Package providerdeploy owns the explicit Linux process/config deployment
// transaction. It never derives actions from registration or health facts.
package providerdeploy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

const maximumReceiptBytes = 1 << 20

var ErrIncompleteReceipt = errors.New("an incomplete provider apply receipt requires manual recovery")

type ReceiptState string

const (
	StatePrepared                ReceiptState = "prepared"
	StateApplying                ReceiptState = "applying"
	StateApplied                 ReceiptState = "applied"
	StateRolledBack              ReceiptState = "rolled_back"
	StateAppliedResumeIncomplete ReceiptState = "applied_resume_incomplete"
	StateManualRecovery          ReceiptState = "manual_recovery_required"
)

type StepState string

const (
	StepPending   StepState = "pending"
	StepSucceeded StepState = "succeeded"
	StepFailed    StepState = "failed"
)

type Receipt struct {
	SchemaVersion   int                `json:"schema_version"`
	ApplyID         string             `json:"apply_id"`
	State           ReceiptState       `json:"state"`
	Code            string             `json:"code,omitempty"`
	CatalogRevision uint64             `json:"catalog_revision"`
	LeaseID         string             `json:"lease_id"`
	PreviousTarget  string             `json:"previous_target,omitempty"`
	CandidateTarget string             `json:"candidate_target"`
	Plan            providerapply.Plan `json:"plan"`
	StartedAt       time.Time          `json:"started_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	Steps           []ReceiptStep      `json:"steps"`
}

type ReceiptStep struct {
	Index  int       `json:"index"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	State  StepState `json:"state"`
	Code   string    `json:"code,omitempty"`
}

type Journal struct {
	directory string
	current   string
	receipt   Receipt
	now       func() time.Time
}

func OpenJournal(directory, previousTarget, candidateTarget, leaseID string, plan providerapply.Plan) (*Journal, error) {
	directory = filepath.Clean(strings.TrimSpace(directory))
	if !filepath.IsAbs(directory) || directory == string(filepath.Separator) {
		return nil, errors.New("receipt directory must be absolute and scoped")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		return nil, errors.New("receipt directory must be a real 0700 directory")
	}
	if !filepath.IsAbs(candidateTarget) || (previousTarget != "" && !filepath.IsAbs(previousTarget)) ||
		plan.SchemaVersion != 1 || !plan.Safe || plan.CatalogRevision == 0 || leaseID == "" {
		return nil, errors.New("invalid provider apply receipt input")
	}
	journal := &Journal{directory: directory, current: filepath.Join(directory, "current.json"), now: time.Now}
	if err := journal.archiveTerminal(); err != nil {
		return nil, err
	}
	applyID, err := newApplyID()
	if err != nil {
		return nil, err
	}
	now := journal.now().UTC()
	journal.receipt = Receipt{
		SchemaVersion: 1, ApplyID: applyID, State: StatePrepared,
		CatalogRevision: plan.CatalogRevision, LeaseID: leaseID,
		PreviousTarget: previousTarget, CandidateTarget: candidateTarget, Plan: plan,
		StartedAt: now, UpdatedAt: now, Steps: []ReceiptStep{},
	}
	if err := journal.persist(); err != nil {
		return nil, err
	}
	return journal, nil
}

func (journal *Journal) Before(action, target string) (int, error) {
	action, target = strings.TrimSpace(action), strings.TrimSpace(target)
	if journal == nil || action == "" || target == "" || closed(journal.receipt.State) {
		return -1, errors.New("invalid provider apply receipt step")
	}
	index := len(journal.receipt.Steps)
	journal.receipt.State = StateApplying
	journal.receipt.Steps = append(journal.receipt.Steps, ReceiptStep{
		Index: index, Action: action, Target: target, State: StepPending,
	})
	journal.receipt.UpdatedAt = journal.now().UTC()
	return index, journal.persist()
}

func (journal *Journal) Complete(index int) error {
	if journal == nil || index < 0 || index >= len(journal.receipt.Steps) || journal.receipt.Steps[index].State != StepPending {
		return errors.New("invalid provider apply completed step")
	}
	journal.receipt.Steps[index].State = StepSucceeded
	journal.receipt.UpdatedAt = journal.now().UTC()
	return journal.persist()
}

func (journal *Journal) Fail(index int, code string) error {
	if journal == nil || index < 0 || index >= len(journal.receipt.Steps) || journal.receipt.Steps[index].State != StepPending ||
		strings.TrimSpace(code) == "" {
		return errors.New("invalid provider apply failed step")
	}
	journal.receipt.Steps[index].State = StepFailed
	journal.receipt.Steps[index].Code = strings.TrimSpace(code)
	journal.receipt.UpdatedAt = journal.now().UTC()
	return journal.persist()
}

func (journal *Journal) Finish(state ReceiptState, code string) error {
	if journal == nil || !closed(state) {
		return errors.New("invalid terminal provider apply state")
	}
	journal.receipt.State = state
	journal.receipt.Code = strings.TrimSpace(code)
	journal.receipt.UpdatedAt = journal.now().UTC()
	return journal.persist()
}

func (journal *Journal) Receipt() Receipt { return journal.receipt }

func (journal *Journal) archiveTerminal() error {
	info, err := os.Lstat(journal.current)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumReceiptBytes {
		return ErrIncompleteReceipt
	}
	payload, err := os.ReadFile(journal.current)
	if err != nil {
		return err
	}
	var prior Receipt
	if err := decodeReceipt(payload, &prior); err != nil || !receiptSettled(prior) || prior.ApplyID == "" {
		return ErrIncompleteReceipt
	}
	archive := filepath.Join(journal.directory, prior.ApplyID+".json")
	if _, err := os.Lstat(archive); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("provider apply receipt archive already exists or is unreadable")
	}
	if err := os.Rename(journal.current, archive); err != nil {
		return err
	}
	return syncDirectory(journal.directory)
}

func (journal *Journal) persist() error {
	payload, err := json.MarshalIndent(journal.receipt, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temporary, err := os.CreateTemp(journal.directory, ".current-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	complete := false
	defer func() {
		_ = temporary.Close()
		if !complete {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(journal.current); err == nil && !info.Mode().IsRegular() {
		return errors.New("current provider apply receipt is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryPath, journal.current); err != nil {
		return err
	}
	complete = true
	return syncDirectory(journal.directory)
}

func decodeReceipt(payload []byte, receipt *Receipt) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(receipt); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("provider apply receipt has trailing JSON")
	}
	return nil
}

func closed(state ReceiptState) bool {
	switch state {
	case StateApplied, StateRolledBack, StateAppliedResumeIncomplete, StateManualRecovery:
		return true
	default:
		return false
	}
}

func settled(state ReceiptState) bool {
	return state == StateApplied || state == StateRolledBack
}

func receiptSettled(receipt Receipt) bool {
	if !settled(receipt.State) {
		return false
	}
	for _, step := range receipt.Steps {
		if step.State == StepPending {
			return false
		}
	}
	return true
}

func newApplyID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "apply-" + hex.EncodeToString(value), nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
