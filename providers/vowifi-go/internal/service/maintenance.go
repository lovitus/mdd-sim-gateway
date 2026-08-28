// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func (backend *Backend) BeginDrain(_ context.Context, request vowifiipc.MaintenanceRequest) (vowifiipc.MaintenanceResult, error) {
	if err := request.Validate(); err != nil {
		return vowifiipc.MaintenanceResult{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.drainLease != "" && backend.drainLease != request.LeaseID {
		return vowifiipc.MaintenanceResult{}, conflictLayer("maintenance_busy", "maintenance")
	}
	if backend.activeCall != nil {
		return vowifiipc.MaintenanceResult{}, conflictLayer("active_call", "maintenance")
	}
	if backend.messageSends != 0 || backend.condition == vowifiipc.RuntimeStarting || backend.condition == vowifiipc.RuntimeStopping {
		return vowifiipc.MaintenanceResult{}, conflictLayer("operation_in_progress", "maintenance")
	}
	if backend.drainLease == "" {
		if err := backend.operations.BeginMaintenance(request.LeaseID); err != nil {
			return vowifiipc.MaintenanceResult{}, err
		}
		backend.drainLease = request.LeaseID
		backend.sequence++
	}
	return vowifiipc.MaintenanceResult{
		LeaseID: request.LeaseID, Draining: true, Status: backend.snapshotLocked(),
	}, nil
}

func (backend *Backend) EndDrain(_ context.Context, request vowifiipc.MaintenanceRequest) (vowifiipc.MaintenanceResult, error) {
	if err := request.Validate(); err != nil {
		return vowifiipc.MaintenanceResult{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.drainLease != request.LeaseID {
		return vowifiipc.MaintenanceResult{}, conflictLayer("maintenance_lease_mismatch", "maintenance")
	}
	if err := backend.operations.EndMaintenance(request.LeaseID); err != nil {
		return vowifiipc.MaintenanceResult{}, err
	}
	backend.drainLease = ""
	backend.sequence++
	return vowifiipc.MaintenanceResult{
		LeaseID: request.LeaseID, Draining: false, Status: backend.snapshotLocked(),
	}, nil
}

var _ vowifiipc.MaintenanceBackend = (*Backend)(nil)
