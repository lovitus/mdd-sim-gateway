package runtimereconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type ExitRecoveryConfig struct {
	Apply egressconfig.RecoveryService
	Store interface {
		ExitRecovery(string) (events.ExitRecoverySnapshot, error)
		PutExitRecoveryExpected(string, recovery.ExitLedger, uint64) (events.ExitRecoverySnapshot, error)
	}
	Config interface {
		Snapshot() (egressconfig.Snapshot, error)
	}
	DesiredPath string
	StatusPath  string
}

func (config *ExitRecoveryConfig) valid() bool {
	return config.Store != nil && config.Config != nil && filepath.IsAbs(config.DesiredPath) && filepath.IsAbs(config.StatusPath)
}

// This commits observed policy evidence, not a completed switch. Execution of
// LastDecision must still obtain the existing apply/maintenance authority.
func (reconciler *Reconciler) observeExitRecovery(ctx context.Context, catalog linecatalog.Snapshot, line linecatalog.Line, observation lineObservation) error {
	config := reconciler.exitRecovery
	status := observation.status
	if config == nil || !line.Enabled || !observation.intentFound || !observation.intentEnabled ||
		!observation.providerReady || observation.cardMatches != 1 || status.ActiveCall != nil || status.PendingIncomingCall != nil || status.Maintenance.Draining ||
		status.Validate() != nil || status.LineID != line.ID || observation.fence.CardID != line.CardID ||
		status.ProviderID != observation.fence.ProviderID || status.ProcessGeneration != observation.fence.Generation {
		return nil
	}
	now := reconciler.now().UTC()
	if now.Sub(status.ObservedAt) > agentTopologyTTL || status.ObservedAt.After(now.Add(5*time.Second)) {
		return nil
	}
	reconciler.mu.Lock()
	currentLine := reconciler.lineLocked(line.ID)
	stableFor := time.Duration(0)
	if !currentLine.healthySince.IsZero() && currentLine.healthyFence == observation.fence {
		stableFor = reconciler.now().Sub(currentLine.healthySince)
	}
	reconciler.mu.Unlock()
	if status.Runtime.Condition == vowifiipc.RuntimeRunning && status.Tunnel.Condition == vowifiipc.LayerReady && status.Tunnel.Available &&
		status.IMS.Condition == vowifiipc.LayerReady && status.IMS.Available && stableFor >= recoveryStableWindow {
		current, err := config.Store.ExitRecovery(line.ID)
		if err != nil || current.Revision == 0 || (current.Ledger.Failures == 0 && current.Ledger.LastDecision == "") {
			return err
		}
		if current.Ledger.Selection.Pending() {
			return nil
		}
		if current.Ledger.SampleGeneration == status.ProcessGeneration && status.Sequence <= current.Ledger.LastSequence {
			return nil
		}
		// Close the outage while keeping the last failure and sample watermark.
		// A late snapshot cannot revive a failure from the healed campaign.
		_, err = config.Store.PutExitRecoveryExpected(line.ID, recovery.ExitLedger{
			LastFailureID: current.Ledger.LastFailureID, LastSequence: status.Sequence, SampleGeneration: status.ProcessGeneration,
			LastManualRetryID: current.Ledger.LastManualRetryID,
		}, current.Revision)
		return err
	}
	verdict := recovery.ClassifyProviderFailure(status, stableFor, recoveryStableWindow)
	if verdict == recovery.ExitUnclear {
		return nil
	}
	current, err := config.Store.ExitRecovery(line.ID)
	if err != nil {
		return err
	}
	if current.Ledger.Selection.Pending() {
		return nil
	}
	if current.Ledger.LastFailureID == status.Runtime.FailureID ||
		(current.Ledger.SampleGeneration == status.ProcessGeneration && status.Sequence <= current.Ledger.LastSequence) {
		return nil
	}
	exits, err := config.Config.Snapshot()
	if err != nil {
		return errors.New("exit configuration unavailable")
	}
	desired, err := egressdesired.Read(config.DesiredPath)
	if err != nil || desired.EgressConfigRevision != exits.Revision || desired.CatalogRevision != catalog.Revision {
		return errors.New("exit desired revision is not current")
	}
	actual, err := egressstatus.Load(config.StatusPath)
	if err != nil || actual.DesiredGeneration != desired.Generation {
		return errors.New("exit runtime generation is not confirmed")
	}
	country := line.Network.EgressCountry
	exit, exists := exits.Config.Exits[country]
	observedExit, observed := actual.Exits[country]
	if !exists || !observed || !exits.Config.Enabled || !exit.Enabled || exit.Mode == "direct" ||
		!observedExit.Ready || observedExit.Node == "" {
		return nil
	}
	peerRegistered := false
	for _, peer := range catalog.Lines {
		if peer.ID == line.ID || !peer.Enabled || peer.Network.EgressCountry != country {
			continue
		}
		intent, found, _, err := reconciler.catalog.RuntimeIntent(peer.ID)
		if err != nil || !found {
			return errors.New("peer runtime intent is unknown")
		}
		if !intent {
			continue
		}
		peerContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		peerStatus, fence, err := reconciler.runtime.Observe(peerContext, peer.ID)
		cancel()
		if err != nil || fence.CardID != peer.CardID || peerStatus.Validate() != nil || peerStatus.LineID != peer.ID ||
			peerStatus.ProviderID != fence.ProviderID || peerStatus.ProcessGeneration != fence.Generation ||
			now.Sub(peerStatus.ObservedAt) > agentTopologyTTL || peerStatus.ObservedAt.After(now.Add(5*time.Second)) {
			return errors.New("peer runtime evidence is unknown")
		}
		if peerStatus.ActiveCall != nil || peerStatus.PendingIncomingCall != nil {
			return nil
		}
		peerRegistered = peerRegistered || (peerStatus.Tunnel.Condition == vowifiipc.LayerReady && peerStatus.Tunnel.Available &&
			peerStatus.IMS.Condition == vowifiipc.LayerReady && peerStatus.IMS.Available)
	}
	lineEpoch := recoveryDigest(line)
	campaign := recovery.ExitCampaign{Epoch: recoveryDigest([]any{lineEpoch, exits.Revision, status.ProviderID, status.ProcessGeneration}),
		SampleGeneration: status.ProcessGeneration, StableCardKey: recoveryDigest(line.CardID), LineConfigEpoch: lineEpoch}
	if current.Ledger.CampaignEpoch == campaign.Epoch && status.Sequence <= current.Ledger.LastSequence {
		return nil
	}
	ledger, err := recovery.BeginExitCampaign(current.Ledger, campaign)
	if err != nil {
		return err
	}
	action, ledger := recovery.RecordExitFailureOnce(ledger, recovery.ExitFailure{
		Verdict: verdict, Node: observedExit.Node, Candidates: observedExit.Candidates, PeerRegistered: peerRegistered,
		Pinned: exit.PinnedNode != "" && exit.PinMode != "prefer", CampaignEpoch: campaign.Epoch,
		SampleGeneration: campaign.SampleGeneration, ExpectedSampleGeneration: campaign.SampleGeneration,
	}, status.Runtime.FailureID)
	ledger.LastSequence, ledger.LastDecision = status.Sequence, action
	if action == recovery.ExitBackOff {
		ledger.RetryAfter = now.Add(recovery.ExhaustedRetry)
	}
	if action == recovery.ExitGiveUp || action == recovery.ExitReport || (action == recovery.ExitBackOff && !current.Ledger.Exhausted) {
		text := fmt.Sprintf("当前账本记录 %d 次异常样本；未切换共享出口。请检查线路、ePDG/IMS 和出口状态。", ledger.Failures)
		if ledger.HeldForPeer {
			text += " 同一出口有其他已注册线路，已保留该出口。"
		}
		if action == recovery.ExitGiveUp {
			text = "锁定节点连续建隧道失败，已暂停自动恢复。请人工更换节点、解除锁定或明确重试。"
		}
		if action == recovery.ExitBackOff {
			text = "当前候选出口已全部尝试，恢复进入约每小时一次的慢速重试。请检查出口和本机网络。"
		}
		ledger.Notice = exitNotice(line, recoveryDigest([]any{ledger.CampaignEpoch, action, status.Runtime.FailureID}), text, now)
	}
	if action == recovery.ExitSwitch && config.Apply != nil {
		for _, candidate := range observedExit.Candidates {
			if candidate == "" || candidate == observedExit.Node || slices.Contains(ledger.Tried, candidate) {
				continue
			}
			request := egressconfig.RecoveryRequest{SchemaVersion: egressconfig.SchemaVersion, ConfigRevision: exits.Revision, CatalogRevision: catalog.Revision,
				LineID: line.ID, ProviderGeneration: status.ProcessGeneration, FailureID: status.Runtime.FailureID,
				ExpectedGeneration: desired.Generation, Country: country, FromNode: observedExit.Node, ToNode: candidate}
			if err := request.Validate(); err != nil {
				return err
			}
			ledger.Selection = &recovery.ExitSelection{Request: request, State: "pending", NextAttempt: now}
			break
		}
	}
	latestCatalog, err := reconciler.catalog.Snapshot()
	if err != nil || latestCatalog.Revision != catalog.Revision {
		return errors.New("catalog changed during recovery observation")
	}
	latestExits, err := config.Config.Snapshot()
	if err != nil || latestExits.Revision != exits.Revision {
		return errors.New("exit configuration changed during recovery observation")
	}
	latestDesired, err := egressdesired.Read(config.DesiredPath)
	if err != nil || latestDesired.Generation != desired.Generation {
		return errors.New("exit application changed during recovery observation")
	}
	latestActual, err := egressstatus.Load(config.StatusPath)
	if err != nil || latestActual.DesiredGeneration != desired.Generation || !latestActual.Exits[country].Ready ||
		latestActual.Exits[country].Node != observedExit.Node {
		return errors.New("selected exit changed during recovery observation")
	}
	intent, found, _, err := reconciler.catalog.RuntimeIntent(line.ID)
	if err != nil {
		return err
	}
	if !found || !intent {
		return nil
	}
	_, err = config.Store.PutExitRecoveryExpected(line.ID, ledger, current.Revision)
	return err
}

func recoveryDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func exitNotice(line linecatalog.Line, id, text string, now time.Time) *recovery.ExitNotice {
	name := line.Name
	if name == "" {
		name = line.ID
	}
	return &recovery.ExitNotice{ID: id, LineID: line.ID, LineName: name, CardID: line.CardID, MSISDN: line.SIM.MSISDN, Text: text, OccurredAt: now.UTC()}
}
