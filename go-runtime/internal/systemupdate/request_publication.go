package systemupdate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

type RequestPublication struct {
	Request     Request `json:"request"`
	Status      Status  `json:"status"`
	Committed   bool    `json:"committed"`
	Unconfirmed string  `json:"unconfirmed,omitempty"`
}

func requestStatusMatches(request Request, status Status) bool {
	return status.OperationID == request.OperationID && status.Target == request.Target
}

func terminalStatus(status Status) bool {
	return status.State == StateSucceeded || status.State == StateFailed
}

func publicationUnknown(request Request) Status {
	return Status{SchemaVersion: 1, OperationID: request.OperationID, Target: request.Target, State: StateUnknown,
		Phase: "publication", ErrorCode: "update_request_publication_unconfirmed", UpdatedAt: time.Now().UTC()}
}

// The worker must read through the same Core Store that publishes Request.
// A separately opened root Store cannot synchronize the two-file publication.
func (store *Store) requestPublication(operation, target string, expected *Maintenance) (*RequestPublication, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	request, found, err := store.pendingRequestLocked()
	if err != nil {
		return nil, err
	}
	if !found || request.OperationID != operation || request.Target != target {
		return nil, errors.New("update request identity changed")
	}
	record, err := store.maintenanceLocked()
	if err != nil || !reflect.DeepEqual(cloneMaintenance(record), cloneMaintenance(expected)) {
		return nil, errors.New("original update maintenance readback is unconfirmed")
	}
	publication := &RequestPublication{Request: request, Status: publicationUnknown(request)}
	status, statusFound, err := store.statusLocked()
	if err != nil {
		publication.Unconfirmed = "update_worker_status_unreadable"
		return publication, nil
	}
	if !statusFound || !requestStatusMatches(request, status) {
		return publication, nil
	}
	// A restarted Core has no in-memory publication outcome. Confirm both exact
	// files and their containing directory before granting preparation permission.
	for _, path := range []string{store.requestPath, store.statusPath} {
		file, err := os.Open(path)
		if err != nil {
			publication.Unconfirmed = "update_request_sync_unconfirmed"
			return publication, nil
		}
		err = errors.Join(file.Sync(), file.Close())
		if err != nil {
			publication.Unconfirmed = "update_request_sync_unconfirmed"
			return publication, nil
		}
	}
	if err := store.syncDirectory(filepath.Dir(store.requestPath)); err != nil {
		publication.Unconfirmed = "update_request_sync_unconfirmed"
		return publication, nil
	}
	store.publicationPending = ""
	publication.Committed, publication.Status = true, status
	return publication, nil
}

func (owner *MaintenanceOwner) Publication(operation, target string) (MaintenanceView, error) {
	owner.op.Lock()
	defer owner.op.Unlock()
	view := owner.View()
	publication, err := owner.store.requestPublication(operation, target, view.Record)
	view.Publication = publication
	return view, err
}
