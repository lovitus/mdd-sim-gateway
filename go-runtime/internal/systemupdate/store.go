package systemupdate

import (
	"encoding/json"
	"errors"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/updatenetwork"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Request struct {
	Network       *updatenetwork.Route `json:"network,omitempty"`
	SchemaVersion int                  `json:"schema_version"`
	OperationID   string               `json:"operation_id"`
	Repository    string               `json:"repository"`
	Target        string               `json:"target"`
	RequestedAt   time.Time            `json:"requested_at"`
}

type Status struct {
	Maintenance   *Maintenance `json:"maintenance,omitempty"`
	SchemaVersion int          `json:"schema_version"`
	OperationID   string       `json:"operation_id,omitempty"`
	State         string       `json:"state"`
	Phase         string       `json:"phase,omitempty"`
	Target        string       `json:"target,omitempty"`
	ErrorCode     string       `json:"error_code,omitempty"`
	ErrorDetail   string       `json:"error_detail,omitempty"`
	UpdatedAt     time.Time    `json:"updated_at"`
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
	maintenancePath         string
	statusReaderGID         int
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
	return &Store{requestPath: path + ".request.json", statusPath: path + ".status.json", maintenancePath: path + ".maintenance.json", statusReaderGID: -1}, nil
}

func (store *Store) Request(input Request) error {
	if input.SchemaVersion == 0 {
		input.SchemaVersion = 1
	}
	if input.OperationID == "" || input.Repository == "" || input.Target == "" || input.RequestedAt.IsZero() {
		return errors.New("invalid update request")
	}
	if input.Network != nil {
		if err := input.Network.ValidateIdentity(); err != nil {
			return err
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	maintenance, err := store.maintenanceLocked()
	if err != nil {
		return err
	}
	if maintenance != nil && maintenance.Phase != MaintenanceReleased {
		return errors.New("update maintenance requires reconciliation")
	}
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
	record, maintenanceErr := store.maintenanceLocked()
	if maintenanceErr != nil {
		return Status{}, maintenanceErr
	}
	if record != nil && (record.Phase != MaintenanceReleased || !found || status.OperationID == record.OperationID) {
		if err != nil || !found || status.OperationID != record.OperationID {
			return Status{SchemaVersion: 1, OperationID: record.OperationID, State: StateUnknown, Phase: record.Phase,
				ErrorCode: "update_worker_status_unconfirmed", Maintenance: record, UpdatedAt: record.UpdatedAt}, nil
		}
		status.Maintenance = record
		if record.Phase != MaintenanceReleased && status.State != StateRunning && status.State != StateRequested {
			status.State, status.Phase, status.ErrorCode = StateUnknown, record.Phase, "update_maintenance_unconfirmed"
			status.ErrorDetail = "Original update maintenance is retained; reconcile this operation before another update."
		} else if record.Phase == MaintenanceReleased && status.State != StateSucceeded && status.State != StateFailed {
			status.State, status.Phase, status.ErrorCode = StateUnknown, record.Phase, "update_release_receipt_pending"
			status.ErrorDetail = "Maintenance was released; the worker completion receipt still requires reconciliation."
		}
	}
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
	if request.Network != nil && request.Network.ValidateIdentity() != nil {
		return Request{}, false, errors.New("stored update route is invalid")
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
	next.Maintenance = nil
	if store.statusReaderGID >= 0 {
		return atomicJSONOwner(store.statusPath, next, 0o640, 0, store.statusReaderGID)
	}
	return atomicJSON(store.statusPath, next)
}

func (store *Store) SetWorkerStatusReaderGroup(gid int) error {
	if os.Geteuid() != 0 || gid < 0 {
		return errors.New("worker status ownership requires root and a service group")
	}
	store.statusReaderGID = gid
	return nil
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
	return atomicJSONOwner(path, value, 0o600, -1, -1)
}

func atomicJSONOwner(path string, value any, mode os.FileMode, uid, gid int) error {
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
	if uid >= 0 {
		if err := temporary.Chown(uid, gid); err != nil {
			_ = temporary.Close()
			return err
		}
	}
	if err := temporary.Chmod(mode); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
