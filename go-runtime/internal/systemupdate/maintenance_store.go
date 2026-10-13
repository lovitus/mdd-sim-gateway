package systemupdate

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
)

const (
	MaintenanceVersion    = 1
	MaintenancePreparing  = "preparing"
	MaintenanceReady      = "ready"
	MaintenanceActivating = "activating"
	MaintenanceReleasing  = "releasing"
	MaintenanceCleanup    = "cleanup_only"
	MaintenanceReleased   = "released"
)

// Maintenance is Core-owned. Worker progress is deliberately not authoritative
// for permission to activate, release, or begin another update.
type Maintenance struct {
	SchemaVersion   int                   `json:"schema_version"`
	Revision        uint64                `json:"revision"`
	OperationID     string                `json:"operation_id"`
	LeaseID         string                `json:"lease_id"`
	Target          UpdateTarget          `json:"target"`
	Phase           string                `json:"phase"`
	CatalogRevision uint64                `json:"catalog_revision"`
	Providers       []MaintenanceProvider `json:"providers"`
	OriginalCore    string                `json:"original_core"`
	VerifiedCore    string                `json:"verified_core,omitempty"`
	Restarted       []string              `json:"restarted_fixed_services,omitempty"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

type UpdateTarget struct {
	RequestedTarget string `json:"requested_target"`
	ReleaseID       string `json:"release_id"`
	SourceRevision  string `json:"source_revision"`
	CoreSHA256      string `json:"core_sha256"`
	Contract        int    `json:"maintenance_contract"`
}

type MaintenanceProvider struct {
	Fence mediaauth.ProviderFence `json:"identity"`
	State string                  `json:"state"`
}

var updateID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var updateDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var updateRevision = regexp.MustCompile(`^[a-f0-9]{40,64}$`)

func (target UpdateTarget) Validate() error {
	if !updateID.MatchString(target.RequestedTarget) || !updateID.MatchString(target.ReleaseID) || !updateRevision.MatchString(target.SourceRevision) ||
		!updateDigest.MatchString(target.CoreSHA256) || target.Contract != MaintenanceVersion {
		return errors.New("unsupported or invalid update maintenance target")
	}
	return nil
}

func (record Maintenance) Validate() error {
	if record.SchemaVersion != MaintenanceVersion || record.Revision == 0 ||
		!updateID.MatchString(record.OperationID) || record.LeaseID != "update-drain-"+record.OperationID ||
		!updateID.MatchString(record.OriginalCore) || record.Target.Validate() != nil || record.UpdatedAt.IsZero() {
		return errors.New("invalid update maintenance record")
	}
	switch record.Phase {
	case MaintenancePreparing, MaintenanceReady, MaintenanceActivating, MaintenanceReleasing, MaintenanceCleanup, MaintenanceReleased:
	default:
		return errors.New("invalid update maintenance phase")
	}
	seen := map[string]bool{}
	if len(record.Restarted) > len(FixedServices) {
		return errors.New("invalid fixed service receipts")
	}
	for index, unit := range record.Restarted {
		if FixedServices[index] != unit {
			return errors.New("invalid fixed service receipt order")
		}
	}
	for _, item := range record.Providers {
		if !updateID.MatchString(item.Fence.LineID) || !updateID.MatchString(item.Fence.ProviderID) ||
			!updateID.MatchString(item.Fence.Generation) || seen[item.Fence.LineID] {
			return errors.New("invalid update provider identity")
		}
		seen[item.Fence.LineID] = true
		switch item.State {
		case "planned", "acquiring", "acquired", "releasing", "released":
		default:
			return errors.New("invalid update provider phase")
		}
	}
	return nil
}

func (store *Store) Maintenance() (*Maintenance, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.maintenanceLocked()
}

func (store *Store) maintenanceLocked() (*Maintenance, error) {
	payload, err := os.ReadFile(store.maintenancePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record Maintenance
	if json.Unmarshal(payload, &record) != nil || record.Validate() != nil {
		return nil, errors.New("stored update maintenance is corrupt")
	}
	return &record, nil
}

// SaveMaintenance is only called by Core's single update owner, never by the
// root worker. CAS prevents a stale cleanup from replacing a newer operation.
func (store *Store) SaveMaintenance(next Maintenance, expected uint64) error {
	if next.Revision != expected+1 || next.Validate() != nil {
		return errors.New("invalid update maintenance transition")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	prior, err := store.maintenanceLocked()
	if err != nil {
		return err
	}
	if prior == nil && expected != 0 || prior != nil && prior.Revision != expected {
		return errors.New("update maintenance revision changed")
	}
	if prior != nil && prior.Phase != MaintenanceReleased &&
		(prior.OperationID != next.OperationID || prior.LeaseID != next.LeaseID || prior.Target != next.Target) {
		return errors.New("update maintenance identity changed")
	}
	return atomicJSON(store.maintenancePath, next)
}
