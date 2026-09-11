package core

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

type agentRestartGuardFunc func(context.Context, agentlink.ConnectionStatus, string) (func(context.Context) error, error)

func (f agentRestartGuardFunc) Prepare(ctx context.Context, status agentlink.ConnectionStatus, id string) (func(context.Context) error, error) {
	return f(ctx, status, id)
}

func (s *Server) prepareAgentRestart(ctx context.Context, status agentlink.ConnectionStatus, leaseID string) (func(context.Context) error, error) {
	agents, ok := s.agents.(hostAgentMaintenance)
	maintenance, available := s.systemMaintenance.(*SystemMaintenanceHandler)
	if !ok || !available || maintenance.runtime == nil || s.catalog == nil || s.agentRestartBusy == nil || status.Topology == nil ||
		status.LastReport.IsZero() || s.now().Sub(status.LastReport) > 45*time.Second {
		return nil, errors.New("Agent restart safety evidence unavailable")
	}
	if len(status.Topology.RawUSBSessions) != 0 {
		return nil, errors.New("raw USB transport is active")
	}
	if status.Topology.ReaderCondition != agentlink.ReaderReady {
		return nil, errors.New("reader inventory is not current")
	}
	for _, reader := range status.Topology.Readers {
		if reader.CardPresent && reader.IdentityState != agentlink.CardIdentified {
			return nil, errors.New("reader identity is not established")
		}
	}
	snapshot, err := s.currentDevices()
	if err != nil {
		return nil, err
	}
	lineSet := map[string]bool{}
	for _, device := range snapshot.Devices {
		if device.AgentID != status.AgentID || device.ObservedOnly {
			continue
		}
		if device.ProcessGeneration != status.ProcessGeneration {
			return nil, agentlink.ErrGenerationMismatch
		}
		for _, endpoint := range device.Endpoints {
			if endpoint.Line != nil {
				lineSet[endpoint.Line.ID] = true
			}
		}
	}
	lineIDs := make([]string, 0, len(lineSet))
	for id := range lineSet {
		lineIDs = append(lineIDs, id)
	}
	sort.Strings(lineIDs)
	check := func() error {
		for _, id := range lineIDs {
			busy, err := s.agentRestartBusy(id)
			if err != nil {
				return err
			}
			if busy {
				return errors.New("Agent has active line business")
			}
		}
		return nil
	}
	if err := check(); err != nil {
		return nil, err
	}
	catalog, err := s.catalog.Snapshot()
	if err != nil {
		return nil, err
	}
	drain := providerapply.DrainRequest{SchemaVersion: 1, CatalogRevision: catalog.Revision, LeaseID: leaseID, LineIDs: lineIDs}
	releaseProviders := func(releaseContext context.Context) error {
		if len(lineIDs) == 0 {
			return nil
		}
		result, err := maintenance.runtime.Request(releaseContext, drain, false)
		if err != nil {
			return err
		}
		if !result.Ready {
			return errors.New("provider maintenance release unconfirmed")
		}
		return nil
	}
	if len(lineIDs) != 0 {
		result, err := maintenance.runtime.Request(ctx, drain, true)
		if err != nil || !result.Ready {
			return nil, errors.New("provider maintenance not ready")
		}
	}
	if err := agents.BeginHostMaintenance(status.AgentID, status.ProcessGeneration, leaseID); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return nil, errors.Join(err, releaseProviders(cleanup))
	}
	release := func(releaseContext context.Context) error {
		return errors.Join(agents.EndHostMaintenance(status.AgentID, leaseID), releaseProviders(releaseContext))
	}
	if err := check(); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return nil, errors.Join(err, release(cleanup))
	}
	return release, nil
}
