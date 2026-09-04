package agentpolicy

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type Target struct {
	AttachmentID         string
	EquipmentID          string
	CardID               string
	SIMSessionGeneration string
}

type ProfileView struct {
	Name               string
	APN                string
	Auth               string
	Username           string
	PasswordConfigured bool
	System             bool
	Source             string
	PDPType            string
}

type Runtime interface {
	Probe(context.Context) ([]agentmodem.Fact, error)
	SetPolicyRadio(context.Context, Target, bool) error
	ListPolicyProfiles(context.Context, Target) ([]ProfileView, error)
	SavePolicyProfile(context.Context, Target, Profile) error
}

type ConnectionRuntime interface {
	SetPersistent(context.Context, agentdata.Target, agentdata.Profile, bool) error
	OwnedTargets() []agentdata.Target
	ReleaseStale(context.Context, agentdata.Target) error
	Persistent(agentdata.Target) bool
}

type Config struct {
	Store       *Store
	Runtime     Runtime
	Connection  ConnectionRuntime
	Coordinator agentmodem.AuxiliaryCoordinator
	Recovery    recovery.Policy
	Now         func() time.Time
}

type reconcileStatus struct {
	State   string
	Code    string
	RetryAt time.Time
	Attempt int
}

type leaseStatus struct {
	SessionID string
	Purpose   string
	State     string
}

// Manager is the single durable policy and data-admission authority. The
// currently bound coordinator is swapped when one Agent WSS owns a data
// manager; that coordinator serializes policy mutation/reconcile with the
// same paid-call and data lifecycle locks.
type Manager struct {
	config          Config
	mu              sync.RWMutex
	coordinator     agentmodem.AuxiliaryCoordinator
	status          map[string]reconcileStatus
	leases          map[string]leaseStatus
	appliedProfiles map[string]Profile
}

func New(config Config) (*Manager, error) {
	if config.Store == nil || config.Runtime == nil || config.Coordinator == nil {
		return nil, errors.New("invalid modem policy manager configuration")
	}
	if _, err := config.Recovery.Decide(recovery.Failure{Attempt: 1, Recoverable: true}); err != nil {
		return nil, err
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Manager{config: config, coordinator: config.Coordinator,
		status: map[string]reconcileStatus{}, leases: map[string]leaseStatus{},
		appliedProfiles: map[string]Profile{}}, nil
}

func (manager *Manager) Close() error { return manager.config.Store.Close() }

func (manager *Manager) BindCoordinator(coordinator agentmodem.AuxiliaryCoordinator) error {
	if coordinator == nil {
		return errors.New("modem policy coordinator is required")
	}
	manager.mu.Lock()
	manager.coordinator = coordinator
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) BindConnection(connection ConnectionRuntime) error {
	if connection == nil {
		return errors.New("modem connection runtime is required")
	}
	manager.mu.Lock()
	manager.config.Connection = connection
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) coordinatorNow() agentmodem.AuxiliaryCoordinator {
	manager.mu.RLock()
	current := manager.coordinator
	manager.mu.RUnlock()
	return current
}

func (manager *Manager) View(equipmentID, cardID string) agentlink.ModemPolicyFact {
	policy, found, err := manager.config.Store.Get(equipmentID, cardID)
	view := agentlink.ModemPolicyFact{SchemaVersion: 1, EquipmentID: equipmentID, CardID: cardID,
		Desired: agentlink.ModemPolicyDesired{CellularEnabled: false, ConnectionEnabled: false, FlightMode: false, RoamingEnabled: false}}
	view.ProfileMode = "agent"
	manager.mu.RLock()
	connection := manager.config.Connection
	view.ConnectionAvailable = connection != nil
	manager.mu.RUnlock()
	if connection != nil {
		for _, target := range connection.OwnedTargets() {
			if target.EquipmentID == equipmentID && target.CardID == cardID && connection.Persistent(target) {
				view.ConnectionActive = true
				break
			}
		}
	}
	if provider, ok := manager.config.Runtime.(interface{ PolicyProfileMode() string }); ok {
		view.ProfileMode = provider.PolicyProfileMode()
	}
	if err == nil {
		view.Revision = policy.Revision
		view.Persisted = found
		view.Desired = desiredFact(policy.Desired)
		view.UpdatedAt = policy.UpdatedAt
	} else {
		view.State, view.Code = "error", "policy_store_unavailable"
	}
	manager.mu.RLock()
	status := manager.status[pair(equipmentID, cardID)]
	lease := manager.leases[equipmentID]
	manager.mu.RUnlock()
	if view.State == "" {
		view.State, view.Code, view.RetryAt = status.State, status.Code, status.RetryAt
		if view.State == "" {
			view.State, view.Code = "ready", "policy_ready"
		}
	}
	if lease.SessionID != "" {
		view.DataLease = &agentlink.ModemPolicyDataLease{SessionID: lease.SessionID, Purpose: lease.Purpose, State: lease.State}
	}
	return view
}

func desiredFact(input Desired) agentlink.ModemPolicyDesired {
	return agentlink.ModemPolicyDesired{CellularEnabled: input.CellularEnabled, ConnectionEnabled: input.ConnectionEnabled, FlightMode: input.FlightMode,
		RoamingEnabled: input.RoamingEnabled, SelectedProfile: input.SelectedProfile}
}

func desiredStore(input agentlink.ModemPolicyDesired) Desired {
	return Desired{CellularEnabled: input.CellularEnabled, ConnectionEnabled: input.ConnectionEnabled, FlightMode: input.FlightMode,
		RoamingEnabled: input.RoamingEnabled, SelectedProfile: input.SelectedProfile}
}

func (manager *Manager) Execute(ctx context.Context, request agentlink.ModemPolicyRequest) agentlink.ModemPolicyResponse {
	response := agentlink.ModemPolicyResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
		EquipmentID: request.EquipmentID, CardID: request.CardID, SIMSessionGeneration: request.SIMSessionGeneration}
	if err := request.Validate(); err != nil {
		response.Failure = &agentlink.RemoteError{Kind: "rejected", Code: "invalid_modem_policy_request"}
		return response
	}
	coordinator := manager.coordinatorNow()
	err := coordinator.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
		return manager.executeLocked(operationContext, request, &response)
	})
	if err != nil {
		response.Failure = policyFailure(err)
	}
	return response
}

func (manager *Manager) executeLocked(ctx context.Context, request agentlink.ModemPolicyRequest,
	response *agentlink.ModemPolicyResponse) error {
	target := targetFor(request)
	if err := manager.requireFresh(ctx, target); err != nil {
		return err
	}
	policy, _, err := manager.config.Store.Get(request.EquipmentID, request.CardID)
	if err != nil {
		return err
	}
	switch request.Action {
	case agentlink.ModemPolicyRead:
		response.Policy = pointerPolicy(manager.View(request.EquipmentID, request.CardID))
		return nil
	case agentlink.ModemPolicySet:
		if policy.Revision != request.ExpectedRevision {
			return ErrRevision
		}
		next := policy
		if request.Patch.CellularEnabled != nil {
			next.Desired.CellularEnabled = *request.Patch.CellularEnabled
		}
		if request.Patch.ConnectionEnabled != nil {
			next.Desired.ConnectionEnabled = *request.Patch.ConnectionEnabled
		}
		if request.Patch.FlightMode != nil {
			next.Desired.FlightMode = *request.Patch.FlightMode
		}
		if request.Patch.RoamingEnabled != nil {
			next.Desired.RoamingEnabled = *request.Patch.RoamingEnabled
		}
		next, err = manager.config.Store.PutExpected(next, request.ExpectedRevision)
		if err != nil {
			return err
		}
		if next.Desired.FlightMode && (!policy.Desired.FlightMode || policy.Desired.ConnectionEnabled) {
			if err := manager.setPersistentConnection(ctx, target, next, false); err != nil {
				manager.setFailure(next.EquipmentID, next.CardID, "cellular_connection_reconcile_failed")
				response.Policy = pointerPolicy(manager.View(next.EquipmentID, next.CardID))
				return nil
			}
		}
		if next.Desired.FlightMode != policy.Desired.FlightMode {
			if err := manager.config.Runtime.SetPolicyRadio(ctx, target, !next.Desired.FlightMode); err != nil {
				manager.setFailure(next.EquipmentID, next.CardID, policyErrorCode(err))
				response.Policy = pointerPolicy(manager.View(next.EquipmentID, next.CardID))
				return nil
			}
		}
		if !next.Desired.FlightMode && (next.Desired.ConnectionEnabled != policy.Desired.ConnectionEnabled ||
			next.Desired.RoamingEnabled != policy.Desired.RoamingEnabled || policy.Desired.FlightMode) {
			if err := manager.setPersistentConnection(ctx, target, next, next.Desired.ConnectionEnabled); err != nil {
				manager.setFailure(next.EquipmentID, next.CardID, "cellular_connection_reconcile_failed")
				response.Policy = pointerPolicy(manager.View(next.EquipmentID, next.CardID))
				return nil
			}
		}
		manager.setReady(next.EquipmentID, next.CardID)
		response.Policy = pointerPolicy(manager.View(next.EquipmentID, next.CardID))
		return nil
	case agentlink.ModemPolicyProfiles:
		profiles, err := manager.profileViews(ctx, target)
		if err != nil {
			return err
		}
		response.Policy = pointerPolicy(manager.View(request.EquipmentID, request.CardID))
		response.Profiles = profiles
		return nil
	case agentlink.ModemPolicyProfileSave:
		if policy.Revision != request.ExpectedRevision {
			return ErrRevision
		}
		profile := Profile{Name: request.Profile.Name, APN: request.Profile.APN, Auth: request.Profile.Auth,
			Username: request.Profile.Username, Password: request.Profile.Password}
		if !request.Profile.PasswordSet {
			if current, found, loadErr := manager.config.Store.Profile(request.EquipmentID, request.CardID, profile.Name); loadErr != nil {
				return loadErr
			} else if found {
				profile.Password = current.Password
			}
		}
		next, err := manager.config.Store.SaveProfileExpected(request.EquipmentID, request.CardID, profile,
			!request.Profile.PasswordSet, request.ExpectedRevision)
		if err != nil {
			return err
		}
		persisted, found, err := manager.config.Store.Profile(request.EquipmentID, request.CardID, profile.Name)
		if err != nil || !found {
			return errors.Join(err, ErrProfileNotFound)
		}
		if next.Desired.ConnectionEnabled && !next.Desired.FlightMode {
			if err := manager.setPersistentConnection(ctx, target, next, false); err != nil {
				manager.setFailure(next.EquipmentID, next.CardID, "cellular_connection_reconcile_failed")
				response.Policy = pointerPolicy(manager.View(next.EquipmentID, next.CardID))
				response.Profiles, _ = manager.profileViews(ctx, target)
				return nil
			}
		}
		if err := manager.config.Runtime.SavePolicyProfile(ctx, target, persisted); err != nil {
			manager.setFailure(next.EquipmentID, next.CardID, "profile_apply_failed")
			response.Policy = pointerPolicy(manager.View(next.EquipmentID, next.CardID))
			response.Profiles, _ = manager.profileViews(ctx, target)
			return nil
		}
		manager.markProfileApplied(next.EquipmentID, next.CardID, persisted)
		if next.Desired.ConnectionEnabled && !next.Desired.FlightMode {
			if err := manager.setPersistentConnection(ctx, target, next, true); err != nil {
				manager.setFailure(next.EquipmentID, next.CardID, "cellular_connection_reconcile_failed")
				response.Policy = pointerPolicy(manager.View(next.EquipmentID, next.CardID))
				response.Profiles, _ = manager.profileViews(ctx, target)
				return nil
			}
		}
		manager.setReady(next.EquipmentID, next.CardID)
		response.Policy = pointerPolicy(manager.View(next.EquipmentID, next.CardID))
		response.Profiles, err = manager.profileViews(ctx, target)
		return err
	default:
		return errors.New("unsupported modem policy action")
	}
}

func (manager *Manager) profileViews(ctx context.Context, target Target) ([]agentlink.ModemProfileView, error) {
	stored, err := manager.config.Store.Profiles(target.EquipmentID, target.CardID)
	if err != nil {
		return nil, err
	}
	system, systemErr := manager.config.Runtime.ListPolicyProfiles(ctx, target)
	result := make([]agentlink.ModemProfileView, 0, len(stored)+len(system))
	seen := map[string]struct{}{}
	for _, profile := range stored {
		result = append(result, agentlink.ModemProfileView{Name: profile.Name, APN: profile.APN, Auth: profile.Auth,
			Username: profile.Username, PasswordConfigured: profile.Password != "", System: false, Source: "saved"})
		seen[profile.Name] = struct{}{}
	}
	for _, profile := range system {
		if _, exists := seen[profile.Name]; exists {
			continue
		}
		result = append(result, agentlink.ModemProfileView{Name: profile.Name, APN: profile.APN, Auth: profile.Auth,
			Username: profile.Username, PasswordConfigured: profile.PasswordConfigured, System: profile.System,
			Source: profile.Source, PDPType: profile.PDPType})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	if systemErr != nil && len(result) == 0 {
		return nil, systemErr
	}
	return result, nil
}

func pointerPolicy(value agentlink.ModemPolicyFact) *agentlink.ModemPolicyFact { return &value }

func (manager *Manager) requireFresh(ctx context.Context, target Target) error {
	facts, err := manager.config.Runtime.Probe(ctx)
	if err != nil {
		return err
	}
	matches := 0
	for _, fact := range facts {
		if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID &&
			fact.SIM.ICCID == target.CardID && fact.SIM.State == agentmodem.SIMReady &&
			fact.SIM.SessionGeneration == target.SIMSessionGeneration {
			matches++
		}
	}
	if matches != 1 {
		return agentmodem.ErrOperationTargetReplaced
	}
	return nil
}

func targetFor(request agentlink.ModemPolicyRequest) Target {
	return Target{AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, SIMSessionGeneration: request.SIMSessionGeneration}
}

func dataTargetFor(target Target) agentdata.Target {
	return agentdata.Target{AttachmentID: target.AttachmentID, EquipmentID: target.EquipmentID,
		CardID: target.CardID, SIMSessionGeneration: target.SIMSessionGeneration}
}

func (manager *Manager) setPersistentConnection(ctx context.Context, target Target, policy Policy, enabled bool) error {
	manager.mu.RLock()
	connection := manager.config.Connection
	manager.mu.RUnlock()
	if connection == nil {
		if enabled {
			return agentmodem.ErrOperationUnavailable
		}
		return nil
	}
	if enabled {
		_, roaming, err := manager.validateDataTarget(ctx, dataTargetFor(target))
		if err != nil {
			return err
		}
		if roaming && !policy.Desired.RoamingEnabled {
			return ErrRoamingDisabled
		}
	}
	profile := agentdata.Profile{AllowRoaming: policy.Desired.RoamingEnabled}
	if name := strings.TrimSpace(policy.Desired.SelectedProfile); name != "" {
		stored, found, err := manager.config.Store.Profile(policy.EquipmentID, policy.CardID, name)
		if err != nil {
			return err
		}
		if !found {
			return ErrProfileNotFound
		}
		profile.Name, profile.APN, profile.Auth = stored.Name, stored.APN, stored.Auth
		profile.Username, profile.Password = stored.Username, stored.Password
	}
	return connection.SetPersistent(ctx, dataTargetFor(target), profile, enabled)
}

// ResolveDataProfile is called while the data manager already owns its
// lifecycle and paid-call locks. It performs no hardware mutation.
func (manager *Manager) ResolveDataProfile(ctx context.Context, target agentdata.Target, requested, sessionID, purpose string) (agentdata.Profile, error) {
	policy, _, err := manager.config.Store.Get(target.EquipmentID, target.CardID)
	if err != nil {
		return agentdata.Profile{}, err
	}
	if !policy.Desired.CellularEnabled {
		return agentdata.Profile{}, ErrCellularDisabled
	}
	if policy.Desired.FlightMode {
		return agentdata.Profile{}, ErrFlightMode
	}
	_, roaming, err := manager.validateDataTarget(ctx, target)
	if err != nil {
		return agentdata.Profile{}, err
	}
	if roaming && !policy.Desired.RoamingEnabled {
		return agentdata.Profile{}, ErrRoamingDisabled
	}
	name := strings.TrimSpace(requested)
	if name == "" {
		name = policy.Desired.SelectedProfile
	}
	profile := agentdata.Profile{Name: name, AllowRoaming: policy.Desired.RoamingEnabled}
	if name != "" {
		stored, found, loadErr := manager.config.Store.Profile(target.EquipmentID, target.CardID, name)
		if loadErr != nil {
			return agentdata.Profile{}, loadErr
		}
		if !found {
			return agentdata.Profile{}, ErrProfileNotFound
		}
		profile.APN, profile.Auth, profile.Username, profile.Password = stored.APN, stored.Auth, stored.Username, stored.Password
	}
	manager.mu.Lock()
	manager.leases[target.EquipmentID] = leaseStatus{SessionID: sessionID, Purpose: purpose, State: "preparing"}
	manager.mu.Unlock()
	return profile, nil
}

func (manager *Manager) ValidateDataTarget(ctx context.Context, target agentdata.Target) error {
	_, _, err := manager.validateDataTarget(ctx, target)
	return err
}

func (manager *Manager) validateDataTarget(ctx context.Context, target agentdata.Target) ([]agentmodem.Fact, bool, error) {
	facts, err := manager.config.Runtime.Probe(ctx)
	if err != nil {
		return nil, false, err
	}
	matches, roaming := 0, false
	for _, fact := range facts {
		if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID &&
			fact.SIM.ICCID == target.CardID && fact.SIM.State == agentmodem.SIMReady &&
			(target.SIMSessionGeneration == "" || fact.SIM.SessionGeneration == target.SIMSessionGeneration) {
			matches++
			roaming = fact.Network.Registration == agentmodem.RegistrationRoaming
		}
	}
	if matches != 1 {
		return facts, false, agentmodem.ErrOperationTargetReplaced
	}
	return facts, roaming, nil
}

func (manager *Manager) DataPrepared(target agentdata.Target, sessionID, purpose string) {
	manager.mu.Lock()
	if current := manager.leases[target.EquipmentID]; current.SessionID == sessionID {
		current.Purpose, current.State = purpose, "active"
		manager.leases[target.EquipmentID] = current
	}
	manager.mu.Unlock()
}

func (manager *Manager) DataCleanup(target agentdata.Target, sessionID, purpose string) {
	manager.mu.Lock()
	if current := manager.leases[target.EquipmentID]; current.SessionID == sessionID {
		current.Purpose, current.State = purpose, "cleanup"
		manager.leases[target.EquipmentID] = current
	}
	manager.mu.Unlock()
}

func (manager *Manager) DataReleased(target agentdata.Target, sessionID string) {
	manager.mu.Lock()
	if manager.leases[target.EquipmentID].SessionID == sessionID {
		delete(manager.leases, target.EquipmentID)
	}
	manager.mu.Unlock()
}

func (manager *Manager) DataLeaseActive(equipmentID string) bool {
	manager.mu.RLock()
	active := manager.leases[equipmentID].SessionID != ""
	manager.mu.RUnlock()
	return active
}

// ReconcilePolicies is invoked after a fresh topology probe. It repairs the
// selected platform profile and software radio. Data remains lease-driven;
// roaming is enforced when the lease is prepared.
func (manager *Manager) ReconcilePolicies(ctx context.Context, facts []agentmodem.Fact) {
	blockedConnections := manager.reconcileConnectionOwners(ctx, facts)
	for _, fact := range facts {
		if fact.EquipmentID == "" || fact.SIM.ICCID == "" || fact.SIM.State != agentmodem.SIMReady {
			continue
		}
		policy, found, err := manager.config.Store.Get(fact.EquipmentID, fact.SIM.ICCID)
		if err != nil {
			manager.setFailure(fact.EquipmentID, fact.SIM.ICCID, "policy_store_unavailable")
			continue
		}
		if err := blockedConnections[fact.EquipmentID]; err != nil {
			manager.setFailure(fact.EquipmentID, fact.SIM.ICCID, "cellular_connection_cleanup_failed")
			continue
		}
		if !found {
			manager.setReady(fact.EquipmentID, fact.SIM.ICCID)
			continue
		}
		key := pair(fact.EquipmentID, fact.SIM.ICCID)
		manager.mu.RLock()
		status := manager.status[key]
		manager.mu.RUnlock()
		if !status.RetryAt.IsZero() && manager.config.Now().Before(status.RetryAt) {
			continue
		}
		target := Target{AttachmentID: fact.AttachmentID, EquipmentID: fact.EquipmentID, CardID: fact.SIM.ICCID,
			SIMSessionGeneration: fact.SIM.SessionGeneration}
		coordinator := manager.coordinatorNow()
		if policy.Desired.SelectedProfile != "" {
			profile, profileFound, profileErr := manager.config.Store.Profile(
				fact.EquipmentID, fact.SIM.ICCID, policy.Desired.SelectedProfile)
			if profileErr != nil || !profileFound {
				manager.setFailure(fact.EquipmentID, fact.SIM.ICCID, "profile_store_unavailable")
				continue
			}
			if !manager.profileApplied(fact.EquipmentID, fact.SIM.ICCID, profile) {
				err = coordinator.DoAuxiliary(ctx, fact.EquipmentID, func(operationContext context.Context) error {
					return manager.config.Runtime.SavePolicyProfile(operationContext, target, profile)
				})
				if err != nil {
					manager.setFailure(fact.EquipmentID, fact.SIM.ICCID, "profile_apply_failed")
					continue
				}
				manager.markProfileApplied(fact.EquipmentID, fact.SIM.ICCID, profile)
			}
		}
		wantedOn := !policy.Desired.FlightMode
		if fact.Network.SoftwareRadio == agentmodem.RadioUnknown &&
			(policy.Desired.ConnectionEnabled || policy.Desired.FlightMode) {
			manager.setFailure(fact.EquipmentID, fact.SIM.ICCID, "radio_state_unavailable")
			continue
		}
		err = coordinator.DoAuxiliary(ctx, fact.EquipmentID, func(operationContext context.Context) error {
			if policy.Desired.FlightMode {
				if err := manager.setPersistentConnection(operationContext, target, policy, false); err != nil {
					return err
				}
			}
			if fact.Network.SoftwareRadio != agentmodem.RadioUnknown &&
				(fact.Network.SoftwareRadio == agentmodem.RadioOn) != wantedOn {
				if err := manager.config.Runtime.SetPolicyRadio(operationContext, target, wantedOn); err != nil {
					return err
				}
			}
			if wantedOn {
				return manager.setPersistentConnection(operationContext, target, policy, policy.Desired.ConnectionEnabled)
			}
			return nil
		})
		if err != nil {
			code := policyErrorCode(err)
			if policy.Desired.ConnectionEnabled {
				code = "cellular_connection_reconcile_failed"
			}
			manager.setFailure(fact.EquipmentID, fact.SIM.ICCID, code)
			continue
		}
		manager.setReady(fact.EquipmentID, fact.SIM.ICCID)
	}
}

func (manager *Manager) reconcileConnectionOwners(ctx context.Context, facts []agentmodem.Fact) map[string]error {
	blocked := map[string]error{}
	manager.mu.RLock()
	connection := manager.config.Connection
	manager.mu.RUnlock()
	if connection == nil {
		return blocked
	}
	current := make(map[string]agentdata.Target)
	for _, fact := range facts {
		if fact.EquipmentID == "" || fact.SIM.State != agentmodem.SIMReady || fact.SIM.ICCID == "" ||
			fact.SIM.SessionGeneration == "" {
			continue
		}
		target := agentdata.Target{AttachmentID: fact.AttachmentID, EquipmentID: fact.EquipmentID,
			CardID: fact.SIM.ICCID, SIMSessionGeneration: fact.SIM.SessionGeneration}
		if prior, duplicate := current[fact.EquipmentID]; duplicate && prior != target {
			delete(current, fact.EquipmentID)
			blocked[fact.EquipmentID] = agentmodem.ErrOperationTargetReplaced
			continue
		}
		current[fact.EquipmentID] = target
	}
	for _, owned := range connection.OwnedTargets() {
		if target, present := current[owned.EquipmentID]; present && target == owned {
			continue
		}
		key := pair(owned.EquipmentID, owned.CardID)
		manager.mu.RLock()
		status := manager.status[key]
		manager.mu.RUnlock()
		if !status.RetryAt.IsZero() && manager.config.Now().Before(status.RetryAt) {
			blocked[owned.EquipmentID] = errors.New("cellular connection cleanup is in backoff")
			continue
		}
		err := manager.coordinatorNow().DoAuxiliary(ctx, owned.EquipmentID, func(operationContext context.Context) error {
			return connection.ReleaseStale(operationContext, owned)
		})
		if err != nil {
			manager.setFailure(owned.EquipmentID, owned.CardID, "cellular_connection_cleanup_failed")
			blocked[owned.EquipmentID] = err
		} else {
			manager.setReady(owned.EquipmentID, owned.CardID)
		}
	}
	return blocked
}

func (manager *Manager) profileApplied(equipmentID, cardID string, profile Profile) bool {
	manager.mu.RLock()
	applied, found := manager.appliedProfiles[pair(equipmentID, cardID)]
	manager.mu.RUnlock()
	return found && applied == profile
}

func (manager *Manager) markProfileApplied(equipmentID, cardID string, profile Profile) {
	manager.mu.Lock()
	manager.appliedProfiles[pair(equipmentID, cardID)] = profile
	manager.mu.Unlock()
}

func (manager *Manager) setReady(equipmentID, cardID string) {
	manager.mu.Lock()
	manager.status[pair(equipmentID, cardID)] = reconcileStatus{State: "ready", Code: "policy_ready"}
	manager.mu.Unlock()
}
func (manager *Manager) setFailure(equipmentID, cardID, code string) {
	key := pair(equipmentID, cardID)
	manager.mu.Lock()
	current := manager.status[key]
	current.Attempt++
	decision, _ := manager.config.Recovery.Decide(recovery.Failure{Attempt: current.Attempt, Recoverable: true})
	current.State, current.Code, current.RetryAt = "recovering", code, manager.config.Now().Add(decision.After)
	manager.status[key] = current
	manager.mu.Unlock()
}
func policyErrorCode(err error) string {
	switch {
	case errors.Is(err, agentdata.ErrSessionActive):
		return "data_lease_active"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "policy_timeout"
	default:
		return "radio_reconcile_failed"
	}
}

func policyFailure(err error) *agentlink.RemoteError {
	switch {
	case errors.Is(err, agentdata.ErrSessionActive):
		return &agentlink.RemoteError{Kind: "conflict", Code: "data_lease_active"}
	case errors.Is(err, ErrRevision):
		return &agentlink.RemoteError{Kind: "conflict", Code: "policy_revision_changed"}
	case errors.Is(err, agentmodem.ErrOperationTargetReplaced):
		return &agentlink.RemoteError{Kind: "not_ready", Code: "modem_target_replaced", Retryable: true}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &agentlink.RemoteError{Kind: "transport", Code: "modem_policy_timeout", Retryable: true}
	default:
		return &agentlink.RemoteError{Kind: "failed", Code: "modem_policy_operation_failed"}
	}
}
func pair(equipmentID, cardID string) string { return equipmentID + "\x00" + cardID }

type dataAdmissionError struct{ code, detail string }

func (err dataAdmissionError) Error() string         { return err.detail }
func (err dataAdmissionError) ModemDataCode() string { return err.code }

var (
	ErrCellularDisabled error = dataAdmissionError{"cellular_data_disabled", "cellular data borrowing is disabled by device policy"}
	ErrFlightMode       error = dataAdmissionError{"flight_mode_enabled", "flight mode blocks cellular data borrowing"}
	ErrRoamingDisabled  error = dataAdmissionError{"cellular_roaming_disabled", "data roaming is disabled by modem policy"}
	ErrProfileNotFound  error = dataAdmissionError{"cellular_profile_not_found", "selected cellular profile was not found"}
)
