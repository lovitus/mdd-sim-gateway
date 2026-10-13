package euiccprofiles

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type exactProfileRuntime interface {
	ExecuteEUICCProfile(context.Context, string, string, agentlink.EUICCProfileRequest) (agentlink.EUICCProfileResponse, error)
}

type mutationTarget struct {
	status                agentlink.ConnectionStatus
	reader, session, slot string
	inventory             agentlink.EUICCFact
}

type pendingMutation struct {
	target      mutationTarget
	operation   string
	release     func(context.Context) error
	cleanupOnly bool
}

// Installed once by the Core composition root before the HTTP listener opens.
func (s *Service) SetMutationGuard(guard func(context.Context, agentlink.ConnectionStatus, []string, string) (func(context.Context) error, error)) {
	s.mutationGuard = guard
}

func profileUnavailable(code string) error {
	return &agentlink.RemoteError{Kind: "not_ready", Code: code}
}

func (s *Service) mutationTarget(eid string) (mutationTarget, error) {
	return s.profileTarget(eid, false)
}

func (s *Service) profileTarget(eid string, readOnly bool) (mutationTarget, error) {
	var matches []mutationTarget
	for _, status := range s.agents.Statuses() {
		if status.Topology == nil {
			continue
		}
		for _, reader := range status.Topology.Readers {
			for _, slot := range agentlink.ReaderEUICCs(reader) {
				if slot.EUICC.EID != eid {
					continue
				}
				if status.AgentID == "" || status.ProcessGeneration == "" || status.LastReport.IsZero() ||
					s.now().Sub(status.LastReport) > 45*time.Second || status.LastReport.Sub(s.now()) > 5*time.Second ||
					status.Topology.ReaderCondition != agentlink.ReaderReady || !reader.CardPresent ||
					reader.IdentityState != agentlink.CardIdentified || reader.SessionGeneration == "" ||
					(readOnly && !slot.EUICC.InventoryRefresh) || (!readOnly && (!slot.EUICC.ProfilesAvailable || !slot.EUICC.ProfileManagement)) {
					return mutationTarget{}, profileUnavailable("euicc_profile_inventory_unavailable")
				}
				matches = append(matches, mutationTarget{status, reader.ReaderName, reader.SessionGeneration, slot.SlotID, *cloneEUICC(&slot.EUICC)})
			}
		}
	}
	if len(matches) == 0 {
		return mutationTarget{}, agentlink.ErrCardOffline
	}
	if len(matches) != 1 {
		return mutationTarget{}, agentlink.ErrCardAmbiguous
	}
	return matches[0], nil
}

func sameMutationTarget(a, b mutationTarget) bool {
	return a.status.AgentID == b.status.AgentID && a.status.ProcessGeneration == b.status.ProcessGeneration &&
		a.reader == b.reader && a.session == b.session && a.slot == b.slot &&
		reflect.DeepEqual(a.inventory.Profiles, b.inventory.Profiles)
}

func (s *Service) mutationLines(ctx context.Context, target mutationTarget) ([]string, uint64, error) {
	if s.catalog == nil || s.providers == nil {
		return nil, 0, profileUnavailable("euicc_profile_safety_unavailable")
	}
	catalog, err := s.catalog.Snapshot()
	if err != nil {
		return nil, 0, profileUnavailable("euicc_profile_line_state_unavailable")
	}
	cards := map[string]bool{}
	for _, profile := range target.inventory.Profiles {
		cards[profile.ICCID] = true
	}
	var lines []string
	for _, line := range catalog.Lines {
		if !cards[line.CardID] {
			continue
		}
		// Catalog intent does not prove that an old runtime has stopped.
		status, err := s.providers.Status(ctx, line.ID)
		if err != nil {
			return nil, 0, profileUnavailable("euicc_profile_line_state_unavailable")
		}
		if status.ActiveCall != nil {
			return nil, 0, &agentlink.RemoteError{Kind: "conflict", Code: "euicc_profile_call_active"}
		}
		if status.Runtime.Condition != vowifiipc.RuntimeStopped {
			return nil, 0, &agentlink.RemoteError{Kind: "conflict", Code: "euicc_profile_line_active"}
		}
		lines = append(lines, line.ID)
	}
	sort.Strings(lines)
	return lines, catalog.Revision, nil
}

func (s *Service) releaseMutation(eid string, pending *pendingMutation) error {
	pending.cleanupOnly = true
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pending.release(ctx); err != nil {
		return profileUnavailable("euicc_profile_maintenance_release_unknown")
	}
	delete(s.pendingMutations, eid)
	return nil
}

func (s *Service) mutateProfile(ctx context.Context, command agentlink.EUICCProfileCommand) (agentlink.EUICCProfileResponse, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	var empty agentlink.EUICCProfileResponse
	if s.pendingMutations[command.EID] != nil {
		return empty, profileUnavailable("euicc_profile_previous_operation_unknown_read_inventory")
	}
	target, err := s.mutationTarget(command.EID)
	if err != nil {
		return empty, err
	}
	lines, revision, err := s.mutationLines(ctx, target)
	if err != nil {
		return empty, err
	}
	runtime, ok := s.agents.(exactProfileRuntime)
	if !ok || s.mutationGuard == nil {
		return empty, profileUnavailable("euicc_profile_safety_unavailable")
	}
	found := false
	for _, profile := range target.inventory.Profiles {
		if profile.ICCID == command.ICCID && profile.State == command.ExpectedState {
			found = true
		}
	}
	if !found {
		return empty, &agentlink.RemoteError{Kind: "conflict", Code: "euicc_profile_state_changed"}
	}
	release, err := s.mutationGuard(ctx, target.status, lines, command.OperationID)
	if err != nil {
		return empty, err
	}
	if release == nil {
		return empty, profileUnavailable("euicc_profile_safety_unavailable")
	}
	pending := &pendingMutation{target: target, operation: command.OperationID, release: release}
	if s.pendingMutations == nil {
		s.pendingMutations = map[string]*pendingMutation{}
	}
	s.pendingMutations[command.EID] = pending
	// Recheck under the acquired permit. Never resolve again at the send call.
	current, err := s.mutationTarget(command.EID)
	if err == nil && !sameMutationTarget(target, current) {
		err = agentlink.ErrGenerationMismatch
	}
	if err == nil {
		var next []string
		var nextRevision uint64
		next, nextRevision, err = s.mutationLines(ctx, current)
		if err == nil && (revision != nextRevision || !reflect.DeepEqual(lines, next)) {
			err = profileUnavailable("euicc_profile_catalog_changed")
		}
	}
	if err != nil {
		return empty, errors.Join(err, s.releaseMutation(command.EID, pending))
	}
	request := agentlink.EUICCProfileRequest{OperationID: command.OperationID, SessionGeneration: target.session, EID: command.EID,
		ICCID: command.ICCID, Action: command.Action, ExpectedState: command.ExpectedState}
	ctx = agentlink.WithEUICCProfileMaintenance(ctx, target.status.AgentID, target.status.ProcessGeneration, command.OperationID, request)
	result, err := runtime.ExecuteEUICCProfile(ctx, target.status.AgentID, target.status.ProcessGeneration, request)
	// Missing/uncertain replies retain the original lease. An explicit inventory
	// read can establish safe handback; it never repeats the mutation.
	if result.ValidateFor(request) != nil || result.Outcome == agentlink.EUICCProfileUncertain || (err != nil && result.Failure == nil) {
		if err == nil && result.Outcome != agentlink.EUICCProfileUncertain {
			err = profileUnavailable("euicc_profile_response_unconfirmed")
		}
		return result, err
	}
	return result, errors.Join(err, s.releaseMutation(command.EID, pending))
}

func (s *Service) refreshProfile(ctx context.Context, command agentlink.EUICCProfileCommand) (agentlink.EUICCProfileResponse, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	pending := s.pendingMutations[command.EID]
	if pending == nil {
		return s.agents.ExecuteEUICCProfileCommand(ctx, command)
	}
	if pending.cleanupOnly {
		if err := s.releaseMutation(command.EID, pending); err != nil {
			return agentlink.EUICCProfileResponse{}, err
		}
		return s.agents.ExecuteEUICCProfileCommand(ctx, command)
	}
	target, err := s.profileTarget(command.EID, true)
	if err != nil {
		return agentlink.EUICCProfileResponse{}, err
	}
	// A successful ES10c switch normally republishes a new insertion. Read that
	// EID only on the original Agent/process; this is not authority for a write.
	if target.status.AgentID != pending.target.status.AgentID || target.status.ProcessGeneration != pending.target.status.ProcessGeneration {
		return agentlink.EUICCProfileResponse{}, agentlink.ErrGenerationMismatch
	}
	runtime, ok := s.agents.(exactProfileRuntime)
	if !ok {
		return agentlink.EUICCProfileResponse{}, profileUnavailable("euicc_profile_safety_unavailable")
	}
	request := agentlink.EUICCProfileRequest{OperationID: command.OperationID, EID: command.EID, SessionGeneration: target.session, Action: agentlink.EUICCProfileRefresh}
	ctx = agentlink.WithEUICCProfileMaintenance(ctx, target.status.AgentID, target.status.ProcessGeneration, pending.operation, request)
	result, err := runtime.ExecuteEUICCProfile(ctx, target.status.AgentID, target.status.ProcessGeneration, request)
	if err == nil && result.ValidateFor(request) == nil && result.Outcome == agentlink.EUICCProfileRefreshed && result.Inventory != nil && result.Inventory.ProfilesAvailable {
		err = s.releaseMutation(command.EID, pending)
	}
	return result, err
}
