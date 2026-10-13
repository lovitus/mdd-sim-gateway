package systemupdate

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
)

type MaintenanceRuntime interface {
	Snapshot() (uint64, []mediaauth.ProviderFence, error)
	Busy() error
	Provider(context.Context, mediaauth.ProviderFence, string, string) error
	FreezeAgents(string) error
	ReleaseAgents(string) error
}

type MaintenanceView struct {
	Record             *Maintenance        `json:"record"`
	Instance           string              `json:"core_instance"`
	CoreSHA256         string              `json:"core_sha256"`
	GuardInstalled     bool                `json:"guard_installed"`
	PersistencePending bool                `json:"persistence_pending,omitempty"`
	Publication        *RequestPublication `json:"publication,omitempty"`
}

// MaintenanceOwner serializes only update operations. Business admission uses
// its own short mutex and never waits for Provider I/O while holding that mutex.
type MaintenanceOwner struct {
	op                   sync.Mutex
	mu                   sync.Mutex
	store                *Store
	gate                 *Admission
	runtime              MaintenanceRuntime
	record               *Maintenance
	persistencePending   bool
	instance, coreSHA256 string
}

func NewMaintenanceOwner(store *Store, gate *Admission, runtime MaintenanceRuntime, instance, coreSHA256 string) (*MaintenanceOwner, error) {
	if store == nil || gate == nil || runtime == nil || !updateID.MatchString(instance) || !updateDigest.MatchString(coreSHA256) {
		return nil, errors.New("invalid update maintenance owner")
	}
	record, err := store.Maintenance()
	if err != nil {
		return nil, err
	}
	if record != nil {
		if err := store.confirmMaintenance(record); err != nil {
			return nil, err
		}
	}
	owner := &MaintenanceOwner{store: store, gate: gate, runtime: runtime, record: record, instance: instance, coreSHA256: coreSHA256}
	if record != nil && record.Phase != MaintenanceReleased {
		if err := gate.freeze(record.LeaseID); err != nil {
			return nil, err
		}
		// This runs before listeners and background producers. Remote registration
		// remains available; verification is deferred until those routes return.
		if err := runtime.FreezeAgents(record.LeaseID); err != nil {
			return nil, err
		}
	}
	return owner, nil
}

func cloneMaintenance(record *Maintenance) *Maintenance {
	if record == nil {
		return nil
	}
	copy := *record
	copy.Providers = append([]MaintenanceProvider(nil), record.Providers...)
	copy.Restarted = append([]string(nil), record.Restarted...)
	return &copy
}

func (owner *MaintenanceOwner) View() MaintenanceView {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return MaintenanceView{Record: cloneMaintenance(owner.record), Instance: owner.instance, CoreSHA256: owner.coreSHA256, GuardInstalled: owner.gate.Blocked(), PersistencePending: owner.persistencePending}
}

func (owner *MaintenanceOwner) save(next *Maintenance) error {
	previous := next.Revision
	next.Revision++
	next.UpdatedAt = time.Now().UTC()
	if err := owner.store.SaveMaintenance(*next, previous); err != nil {
		if wasPublished(err) {
			owner.mu.Lock()
			owner.record = cloneMaintenance(next)
			owner.persistencePending = true
			owner.mu.Unlock()
		} else {
			next.Revision = previous
		}
		return err
	}
	owner.mu.Lock()
	owner.record = cloneMaintenance(next)
	owner.mu.Unlock()
	return nil
}

func (owner *MaintenanceOwner) confirmPersistence(record *Maintenance) error {
	if !owner.View().PersistencePending {
		return nil
	}
	if err := owner.store.confirmMaintenance(record); err != nil {
		return err
	}
	owner.mu.Lock()
	owner.persistencePending = false
	owner.mu.Unlock()
	return nil
}

// Registration admits only original fenced routes while an update is pending.
// An absent or replaced Provider cannot silently join the release set.
func (owner *MaintenanceOwner) Registration(provider mediaauth.Provider) (func(), error) {
	if done, err := owner.gate.Enter(); err == nil {
		return done, nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.record != nil && owner.record.Phase != MaintenanceReleased && owner.gate.owns(owner.record.LeaseID) {
		for _, item := range owner.record.Providers {
			if item.Fence == provider.Fence() {
				return func() {}, nil
			}
		}
	}
	return nil, ErrMaintenance
}

func (owner *MaintenanceOwner) Begin(ctx context.Context, operation string, target UpdateTarget) (MaintenanceView, error) {
	owner.op.Lock()
	defer owner.op.Unlock()
	if !updateID.MatchString(operation) || len("update-drain-"+operation) > 128 || target.Validate() != nil {
		return owner.View(), errors.New("invalid update maintenance request")
	}
	prior := owner.View().Record
	if owner.View().PersistencePending || prior != nil && (prior.Phase != MaintenanceReleased || prior.OperationID == operation) {
		return owner.View(), errors.New("original update requires reconciliation")
	}
	request, found, err := owner.store.PendingRequest()
	if err != nil || !found || request.OperationID != operation || request.Target != target.RequestedTarget {
		return owner.View(), errors.New("update request identity changed")
	}
	lease := "update-drain-" + operation
	if err := owner.gate.freeze(lease); err != nil {
		return owner.View(), err
	}
	revision, providers, err := owner.runtime.Snapshot()
	if err != nil {
		owner.gate.release(lease)
		return owner.View(), err
	}
	record := &Maintenance{SchemaVersion: MaintenanceVersion, OperationID: operation, LeaseID: lease,
		Target: target, Phase: MaintenancePreparing, CatalogRevision: revision, OriginalCore: owner.instance,
		Providers: []MaintenanceProvider{}}
	if prior != nil {
		record.Revision = prior.Revision
	}
	for _, provider := range providers {
		record.Providers = append(record.Providers, MaintenanceProvider{Fence: provider, State: "planned"})
	}
	if err := owner.save(record); err != nil {
		if !wasPublished(err) {
			owner.gate.release(lease)
		}
		return owner.View(), err
	}
	fail := func(cause error) (MaintenanceView, error) {
		record.Phase = MaintenanceCleanup
		if err := owner.save(record); err != nil {
			return owner.View(), errors.Join(cause, err)
		}
		err := owner.release(ctx, record)
		return owner.View(), errors.Join(cause, err)
	}
	// Check before installing the broad Agent RPC fence, so a rejected update
	// never interrupts a live call's renew or cleanup commands.
	if err := owner.runtime.Busy(); err != nil {
		return fail(err)
	}
	for index := range record.Providers {
		record.Providers[index].State = "acquiring"
		if err := owner.save(record); err != nil {
			return owner.View(), err
		}
		if err := owner.runtime.Provider(ctx, record.Providers[index].Fence, lease, "begin"); err != nil {
			return fail(err)
		}
		record.Providers[index].State = "acquired"
		if err := owner.save(record); err != nil {
			return owner.View(), err
		}
	}
	if err := owner.runtime.Busy(); err != nil {
		return fail(err)
	}
	if err := owner.runtime.FreezeAgents(lease); err != nil {
		return fail(err)
	}
	record.Phase = MaintenanceReady
	if err := owner.save(record); err != nil {
		return owner.View(), err
	}
	return owner.View(), nil
}

func (owner *MaintenanceOwner) Activate(operation string, revision uint64) (MaintenanceView, error) {
	owner.op.Lock()
	defer owner.op.Unlock()
	record := owner.View().Record
	if owner.View().PersistencePending || record == nil || record.OperationID != operation || record.Revision != revision || record.Phase != MaintenanceReady || record.OriginalCore != owner.instance {
		return owner.View(), errors.New("update activation permit is not fresh")
	}
	record.Phase = MaintenanceActivating
	if err := owner.save(record); err != nil {
		return owner.View(), err
	}
	return owner.View(), nil
}

// Finish is called only after the worker confirms all fixed services. Recovery
// of releasing/cleanup phases only releases the original set, never reinstalls.
func (owner *MaintenanceOwner) Finish(ctx context.Context, operation string, revision uint64) (MaintenanceView, error) {
	owner.op.Lock()
	defer owner.op.Unlock()
	record := owner.View().Record
	if record == nil || record.OperationID != operation || record.Revision != revision {
		return owner.View(), errors.New("update maintenance identity changed")
	}
	if err := owner.confirmPersistence(record); err != nil {
		return owner.View(), err
	}
	if record.Phase == MaintenanceReleased {
		if err := owner.runtime.ReleaseAgents(record.LeaseID); err != nil {
			return owner.View(), err
		}
		owner.gate.release(record.LeaseID)
		return owner.View(), nil
	}
	if record.Phase == MaintenanceActivating {
		if owner.instance == record.OriginalCore || owner.coreSHA256 != record.Target.CoreSHA256 || !owner.gate.Blocked() {
			return owner.View(), errors.New("replacement Core maintenance is unconfirmed")
		}
		if len(record.Restarted) != len(FixedServices) {
			return owner.View(), errors.New("fixed service restart receipts are incomplete")
		}
		for _, item := range record.Providers {
			if item.State != "acquired" {
				return owner.View(), errors.New("update drain is unconfirmed")
			}
			if err := owner.runtime.Provider(ctx, item.Fence, record.LeaseID, "verify"); err != nil {
				return owner.View(), err
			}
		}
		record.VerifiedCore, record.Phase = owner.instance, MaintenanceReleasing
		if err := owner.save(record); err != nil {
			return owner.View(), err
		}
	}
	if record.Phase != MaintenanceReleasing && record.Phase != MaintenanceCleanup {
		return owner.View(), errors.New("update is not authorized for release")
	}
	err := owner.release(ctx, record)
	return owner.View(), err
}

var FixedServices = [...]string{"mdd-core.service", "mdd-provider-apply.service", "mdd-egress.service"}

func (owner *MaintenanceOwner) Restarted(operation string, revision uint64, unit string) (MaintenanceView, error) {
	owner.op.Lock()
	defer owner.op.Unlock()
	record := owner.View().Record
	if owner.View().PersistencePending || record == nil || record.OperationID != operation || record.Revision != revision || record.Phase != MaintenanceActivating ||
		owner.instance == record.OriginalCore || owner.coreSHA256 != record.Target.CoreSHA256 {
		return owner.View(), errors.New("fixed service receipt does not match replacement Core")
	}
	index := len(record.Restarted)
	if index >= len(FixedServices) || FixedServices[index] != unit {
		return owner.View(), errors.New("unexpected fixed service receipt")
	}
	record.Restarted = append(record.Restarted, unit)
	if err := owner.save(record); err != nil {
		return owner.View(), err
	}
	return owner.View(), nil
}

func (owner *MaintenanceOwner) Cleanup(ctx context.Context, operation string, revision uint64) (MaintenanceView, error) {
	owner.op.Lock()
	defer owner.op.Unlock()
	record := owner.View().Record
	if record == nil || record.OperationID != operation || record.Revision != revision {
		return owner.View(), errors.New("update maintenance identity changed")
	}
	if err := owner.confirmPersistence(record); err != nil {
		return owner.View(), err
	}
	if record.Phase != MaintenancePreparing && record.Phase != MaintenanceReady && record.Phase != MaintenanceCleanup {
		return owner.View(), errors.New("activated update cannot be cancelled")
	}
	record.Phase = MaintenanceCleanup
	if err := owner.save(record); err != nil {
		return owner.View(), err
	}
	err := owner.release(ctx, record)
	return owner.View(), err
}

func (owner *MaintenanceOwner) release(ctx context.Context, record *Maintenance) error {
	for index := range record.Providers {
		item := &record.Providers[index]
		if item.State == "released" {
			continue
		}
		if item.State != "planned" {
			item.State = "releasing"
			if err := owner.save(record); err != nil {
				return err
			}
			if err := owner.runtime.Provider(ctx, item.Fence, record.LeaseID, "release"); err != nil {
				return err
			}
		}
		item.State = "released"
		if err := owner.save(record); err != nil {
			return err
		}
	}
	record.Phase = MaintenanceReleased
	if err := owner.save(record); err != nil {
		return err
	}
	if err := owner.runtime.ReleaseAgents(record.LeaseID); err != nil {
		return err
	}
	owner.gate.release(record.LeaseID)
	return nil
}
