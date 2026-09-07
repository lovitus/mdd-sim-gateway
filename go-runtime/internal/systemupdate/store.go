package systemupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Request struct {
	SchemaVersion int       `json:"schema_version"`
	OperationID   string    `json:"operation_id"`
	Repository    string    `json:"repository"`
	Target        string    `json:"target"`
	RequestedAt   time.Time `json:"requested_at"`
}

type Status struct {
	SchemaVersion int       `json:"schema_version"`
	OperationID   string    `json:"operation_id,omitempty"`
	State         string    `json:"state"`
	Phase         string    `json:"phase,omitempty"`
	Target        string    `json:"target,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	ErrorDetail   string    `json:"error_detail,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

const (
	StateIdle      = "idle"
	StateRequested = "requested"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateUnknown   = "unknown"
)

type Store struct {
	requestPath, statusPath string
	mu                      sync.Mutex
}

func Open(path string) (*Store, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return nil, errors.New("invalid update state path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &Store{requestPath: path + ".request.json", statusPath: path + ".status.json"}, nil
}

func (store *Store) Request(input Request) error {
	if input.SchemaVersion == 0 {
		input.SchemaVersion = 1
	}
	if input.OperationID == "" || input.Repository == "" || input.Target == "" || input.RequestedAt.IsZero() {
		return errors.New("invalid update request")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	status, found, err := store.statusLocked()
	if err != nil {
		return err
	}
	if found && (status.State == StateRequested || status.State == StateRunning || status.State == StateUnknown) {
		return errors.New("update already in progress")
	}
	if err := atomicJSON(store.requestPath, input); err != nil {
		return err
	}
	return atomicJSON(store.statusPath, Status{SchemaVersion: 1, OperationID: input.OperationID, State: StateRequested, Phase: "requested", Target: input.Target, UpdatedAt: time.Now().UTC()})
}

func (store *Store) Status() (Status, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	status, found, err := store.statusLocked()
	if err != nil {
		return Status{}, err
	}
	if !found {
		return Status{SchemaVersion: 1, State: StateIdle, UpdatedAt: time.Now().UTC()}, nil
	}
	return status, nil
}

func (store *Store) PendingRequest() (Request, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	payload, err := os.ReadFile(store.requestPath)
	if errors.Is(err, os.ErrNotExist) {
		return Request{}, false, nil
	}
	if err != nil {
		return Request{}, false, err
	}
	var request Request
	if json.Unmarshal(payload, &request) != nil || request.SchemaVersion != 1 || request.OperationID == "" || request.Target == "" {
		return Request{}, false, errors.New("stored update request is corrupt")
	}
	return request, true, nil
}

func (store *Store) SetStatus(next Status) error {
	if next.SchemaVersion == 0 {
		next.SchemaVersion = 1
	}
	if next.State == "" || next.UpdatedAt.IsZero() {
		return errors.New("invalid update status")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return atomicJSON(store.statusPath, next)
}

func (store *Store) statusLocked() (Status, bool, error) {
	payload, err := os.ReadFile(store.statusPath)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	var status Status
	if json.Unmarshal(payload, &status) != nil || status.SchemaVersion != 1 || status.State == "" {
		return Status{}, false, errors.New("stored update status is corrupt")
	}
	return status, true, nil
}

func atomicJSON(path string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".update-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
