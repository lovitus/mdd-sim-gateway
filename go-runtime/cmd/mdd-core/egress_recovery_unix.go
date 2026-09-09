//go:build !windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"slices"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func (service *providerApplyService) recoveryDocument(config egressconfig.Snapshot, catalog linecatalog.Snapshot, document egressdesired.Document, request egressconfig.RecoveryRequest) (egressdesired.Document, *egressconfig.ApplyResult, error) {
	reject := func(code string) (egressdesired.Document, *egressconfig.ApplyResult, error) {
		return document, nil, egressFailure(http.StatusConflict, code, nil)
	}
	exit, exists := config.Config.Exits[request.Country]
	if !exists || !config.Config.Enabled || !exit.Enabled || exit.Mode == "direct" ||
		config.Config.Profiles[exit.ProfileID].Type != "subscription" || (exit.PinnedNode != "" && exit.PinMode != "prefer") {
		return reject("egress_recovery_policy_changed")
	}
	lineIndex := slices.IndexFunc(catalog.Lines, func(line linecatalog.Line) bool { return line.ID == request.LineID })
	if lineIndex < 0 || !catalog.Lines[lineIndex].Enabled || catalog.Lines[lineIndex].Network.EgressCountry != request.Country {
		return reject("egress_recovery_line_changed")
	}
	current, err := egressdesired.Read(service.settings.ProviderApply.EgressDesiredPath)
	if err != nil || current.EgressConfigRevision != config.Revision || current.CatalogRevision != catalog.Revision {
		return reject("egress_recovery_application_changed")
	}
	actual, err := egressstatus.Load(service.settings.ProviderApply.EgressStatusPath)
	if err != nil || actual.DesiredGeneration != current.Generation {
		return reject("egress_recovery_runtime_unconfirmed")
	}
	observed := actual.Exits[request.Country]
	if previous, found := current.RecoverySelections[request.Country]; found && previous.FailureID == request.FailureID {
		if previous != request {
			return reject("egress_recovery_identity_reused")
		}
		if !observed.Ready || observed.Node != request.ToNode {
			return reject("egress_recovery_selection_unconfirmed")
		}
		return current, &egressconfig.ApplyResult{SchemaVersion: egressconfig.SchemaVersion, ConfigRevision: config.Revision,
			CatalogRevision: catalog.Revision, Generation: current.Generation, State: "unchanged", Code: "runtime_confirmed"}, nil
	}
	if current.Generation != request.ExpectedGeneration || !observed.Ready || observed.Node != request.FromNode ||
		!slices.Contains(observed.Candidates, request.ToNode) {
		return reject("egress_recovery_candidate_changed")
	}
	document.RecoverySelections = maps.Clone(current.RecoverySelections)
	if document.RecoverySelections == nil {
		document.RecoverySelections = map[string]egressconfig.RecoveryRequest{}
	}
	document.RecoverySelections[request.Country] = request
	payload, err := json.Marshal([]any{document.Generation, document.RecoverySelections})
	if err != nil {
		return reject("egress_recovery_generation_failed")
	}
	digest := sha256.Sum256(payload)
	document.Generation = hex.EncodeToString(digest[:])
	return document, nil, nil
}

func (service *providerApplyService) validateRecoveryPeers(ctx context.Context, address string, catalog linecatalog.Snapshot, lease string, request egressconfig.RecoveryRequest) error {
	snapshot, err := providerapply.Fetch(ctx, address+providerapply.Path, service.settings.Local.Token, nil)
	if err != nil || snapshot.CatalogRevision != catalog.Revision {
		return egressFailure(http.StatusConflict, "egress_recovery_preflight_unavailable", err)
	}
	for _, line := range catalog.Lines {
		if !line.Enabled || line.Network.EgressCountry != request.Country {
			continue
		}
		index := slices.IndexFunc(snapshot.Lines, func(status providerapply.LineStatus) bool { return status.LineID == line.ID })
		if index < 0 {
			return egressFailure(http.StatusConflict, "egress_recovery_peer_unknown", nil)
		}
		status := snapshot.Lines[index]
		if !status.ProviderPresent || status.Code != "provider_reachable" || !status.Maintenance.Draining || status.Maintenance.LeaseID != lease ||
			status.ActiveCall != nil || status.PendingIncomingCall != nil {
			return egressFailure(http.StatusConflict, "egress_recovery_peer_not_drained", nil)
		}
		if line.ID == request.LineID {
			if !status.RuntimeIntentKnown || !status.RuntimeIntentEnabled || status.ProcessGeneration != request.ProviderGeneration || status.Runtime.Condition != vowifiipc.RuntimeFailed || status.Runtime.FailureID != request.FailureID {
				return egressFailure(http.StatusConflict, "egress_recovery_failure_changed", nil)
			}
			continue
		}
		if status.Runtime.Condition == vowifiipc.RuntimeStopped || status.Runtime.Condition == vowifiipc.RuntimeFailed {
			continue
		}
		if status.Runtime.Condition != vowifiipc.RuntimeRunning || status.Tunnel == nil || status.IMS == nil ||
			status.Tunnel.Condition == vowifiipc.LayerUnknown || status.IMS.Condition == vowifiipc.LayerUnknown {
			return egressFailure(http.StatusConflict, "egress_recovery_peer_unknown", nil)
		}
		if status.Tunnel.Condition == vowifiipc.LayerReady && status.Tunnel.Available && status.IMS.Condition == vowifiipc.LayerReady && status.IMS.Available {
			return egressFailure(http.StatusConflict, "egress_recovery_healthy_peer", nil)
		}
	}
	return nil
}

func (service *providerApplyService) resumeRecoveryLease(ctx context.Context, address string, request egressconfig.RecoveryRequest) error {
	snapshot, err := providerapply.Fetch(ctx, address+providerapply.Path, service.settings.Local.Token, nil)
	if err != nil || snapshot.CatalogRevision != request.CatalogRevision {
		return egressFailure(http.StatusConflict, "egress_recovery_resume_unconfirmed", err)
	}
	drain := providerapply.DrainRequest{SchemaVersion: 1, CatalogRevision: request.CatalogRevision, LeaseID: "recovery-" + request.FailureID}
	for _, line := range snapshot.Lines {
		if line.Maintenance.Draining && line.Maintenance.LeaseID == drain.LeaseID {
			drain.LineIDs = append(drain.LineIDs, line.LineID)
		}
	}
	if len(drain.LineIDs) == 0 {
		return nil
	}
	if _, err := providerapply.RequestMaintenance(ctx, address, service.settings.Local.Token, drain, false, nil); err != nil {
		return egressFailure(http.StatusConflict, "egress_recovery_resume_unconfirmed", err)
	}
	return nil
}
