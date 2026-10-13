package systemupdate

import (
	"errors"
	"time"
)

type RequestPublication struct {
	Request   Request `json:"request"`
	Status    Status  `json:"status"`
	Committed bool    `json:"committed"`
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
func (store *Store) requestPublication(operation, target string) (*RequestPublication, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	request, found, err := store.pendingRequestLocked()
	if err != nil {
		return nil, err
	}
	if !found || request.OperationID != operation || request.Target != target {
		return nil, errors.New("update request identity changed")
	}
	status, statusFound, err := store.statusLocked()
	if err != nil {
		return nil, err
	}
	committed := statusFound && requestStatusMatches(request, status) && (store.publicationPending != operation || terminalStatus(status))
	if !committed {
		status = publicationUnknown(request)
	}
	return &RequestPublication{Request: request, Status: status, Committed: committed}, nil
}

func (owner *MaintenanceOwner) Publication(operation, target string) (MaintenanceView, error) {
	owner.op.Lock()
	defer owner.op.Unlock()
	publication, err := owner.store.requestPublication(operation, target)
	view := owner.View()
	view.Publication = publication
	return view, err
}
