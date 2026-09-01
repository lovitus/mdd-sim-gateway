package agentevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

var errDurableEventStore = errors.New("durable Agent event store failed")

const (
	defaultScanEvery = time.Second
	callScanBudget   = 2 * time.Second
	smsScanBudget    = 3 * time.Second
)

type ScannerConfig struct {
	Store       *Store
	Operator    agentmodem.Operator
	Coordinator agentmodem.BackgroundScanCoordinator
	Topology    func() agentlink.TopologySnapshot
	Every       time.Duration
}

type Scanner struct {
	config            ScannerConfig
	smsCursor         int
	lastFenceRevision string
	nextSMS           time.Time
}

type scanTarget struct {
	Fence
	Call bool
	SMS  bool
}

func NewScanner(config ScannerConfig) (*Scanner, error) {
	if config.Store == nil || config.Operator == nil || config.Coordinator == nil || config.Topology == nil {
		return nil, errors.New("invalid Agent event scanner configuration")
	}
	if config.Every == 0 {
		config.Every = defaultScanEvery
	}
	if config.Every < 100*time.Millisecond || config.Every > time.Minute {
		return nil, errors.New("invalid Agent event scan interval")
	}
	return &Scanner{config: config}, nil
}

func (scanner *Scanner) Run(ctx context.Context) error {
	ticker := time.NewTicker(scanner.config.Every)
	defer ticker.Stop()
	for {
		if err := scanner.scan(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (scanner *Scanner) scan(ctx context.Context) error {
	topology := scanner.config.Topology()
	if topology.ModemCondition != agentlink.ModemReady {
		return nil
	}
	revision := scanFenceRevision(topology)
	if !scanner.config.Store.ready.Load() || revision != scanner.lastFenceRevision {
		if err := scanner.config.Store.ReconcileFences(topology); err != nil {
			return err
		}
		scanner.lastFenceRevision = revision
	}
	targets := scanTargets(topology)
	allIdle := true
	for _, target := range targets {
		if !target.Call {
			continue
		}
		var result agentmodem.OperationResult
		operationContext, cancel := context.WithTimeout(ctx, callScanBudget)
		err := scanner.config.Coordinator.DoBackgroundScan(operationContext, func(scanContext context.Context) error {
			var operationErr error
			result, operationErr = scanner.config.Operator.Operate(scanContext, agentmodem.Operation{
				OperationID: "event-call-scan", AttachmentID: target.AttachmentID,
				EquipmentID: target.EquipmentID, CardID: target.CardID,
				Action: agentmodem.OperationCallStatus,
			})
			if operationErr != nil {
				return operationErr
			}
			if !sameFence(scanner.config.Topology(), target) {
				return agentmodem.ErrOperationTargetReplaced
			}
			if err := scanner.config.Store.ObserveCall(target.Fence, result.Call, time.Now().UTC()); err != nil {
				return fmt.Errorf("%w: %v", errDurableEventStore, err)
			}
			return nil
		})
		cancel()
		if errors.Is(err, errDurableEventStore) {
			return err
		}
		if errors.Is(err, agentcall.ErrAuxiliaryDuringCall) {
			return nil
		}
		if err != nil || !result.Call.Authoritative || result.Call.State != "idle" || result.Call.VoiceCalls != 0 {
			allIdle = false
		}
	}
	if !allIdle {
		return nil
	}
	now := time.Now()
	if now.Before(scanner.nextSMS) {
		return nil
	}
	scanner.nextSMS = now.Add(5 * time.Second)
	smsTargets := make([]scanTarget, 0, len(targets))
	for _, target := range targets {
		if target.SMS {
			smsTargets = append(smsTargets, target)
		}
	}
	if len(smsTargets) == 0 {
		return nil
	}
	fences := make([]Fence, 0, len(smsTargets))
	for _, target := range smsTargets {
		fences = append(fences, target.Fence)
	}
	if scanner.smsCursor >= len(smsTargets) {
		scanner.smsCursor = 0
	}
	target := smsTargets[scanner.smsCursor]
	scanner.smsCursor = (scanner.smsCursor + 1) % len(smsTargets)
	if deletion, found, err := scanner.config.Store.PendingSMSDeletion(fences); err != nil {
		return err
	} else if found {
		deleteContext, cancel := context.WithTimeout(ctx, smsScanBudget)
		err := scanner.config.Coordinator.DoBackgroundScan(deleteContext, func(scanContext context.Context) error {
			_, operationErr := scanner.config.Operator.Operate(scanContext, agentmodem.Operation{
				OperationID: "event-sms-delete", AttachmentID: deletion.AttachmentID,
				EquipmentID: deletion.EquipmentID, CardID: deletion.CardID,
				Action: agentmodem.OperationSMSDelete, SMSIndices: append([]int(nil), deletion.Indices...),
				SMSFingerprint: deletion.Fingerprint,
			})
			if errors.Is(operationErr, agentmodem.ErrSMSStorageChanged) {
				return scanner.config.Store.CompleteSMSDeletion(deletion.CardID, deletion.Fingerprint)
			}
			if operationErr != nil {
				return operationErr
			}
			if !sameFence(scanner.config.Topology(), scanTarget{Fence: deletion.Fence, SMS: true}) {
				return agentmodem.ErrOperationTargetReplaced
			}
			if err := scanContext.Err(); err != nil {
				return err
			}
			return scanner.config.Store.CompleteSMSDeletion(deletion.CardID, deletion.Fingerprint)
		})
		cancel()
		if errors.Is(err, agentcall.ErrAuxiliaryDuringCall) || errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) || errors.Is(err, agentmodem.ErrOperationTargetReplaced) ||
			errors.Is(err, agentmodem.ErrOperationUnavailable) {
			return nil
		}
		return err
	}
	operationContext, cancel := context.WithTimeout(ctx, smsScanBudget)
	err := scanner.config.Coordinator.DoBackgroundScan(operationContext, func(scanContext context.Context) error {
		result, operationErr := scanner.config.Operator.Operate(scanContext, agentmodem.Operation{
			OperationID: "event-sms-scan", AttachmentID: target.AttachmentID,
			EquipmentID: target.EquipmentID, CardID: target.CardID,
			Action: agentmodem.OperationSMSList,
		})
		if operationErr != nil {
			return operationErr
		}
		if !sameFence(scanner.config.Topology(), target) {
			return agentmodem.ErrOperationTargetReplaced
		}
		if err := scanContext.Err(); err != nil {
			return err
		}
		if err := scanner.config.Store.ObserveSMS(target.Fence, result.SMS.Messages, time.Now().UTC()); err != nil {
			return fmt.Errorf("%w: %v", errDurableEventStore, err)
		}
		return nil
	})
	cancel()
	if errors.Is(err, agentcall.ErrAuxiliaryDuringCall) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, agentmodem.ErrOperationTargetReplaced) || errors.Is(err, agentmodem.ErrOperationUnavailable) {
		return nil
	}
	if errors.Is(err, errDurableEventStore) {
		return err
	}
	// Native AT failures are soft observations and are retried by the next
	// bounded scan. Store failures are returned by ObserveSMS/ObserveCall and
	// are intentionally fatal because continuing could lose durable facts.
	if err != nil {
		return nil
	}
	return nil
}

func scanFenceRevision(topology agentlink.TopologySnapshot) string {
	hash := sha256.New()
	for _, target := range scanTargets(topology) {
		_, _ = hash.Write([]byte(target.AttachmentID + "\x00" + target.EquipmentID + "\x00" + target.CardID +
			"\x00" + target.SIMSessionGeneration + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func scanTargets(topology agentlink.TopologySnapshot) []scanTarget {
	result := make([]scanTarget, 0, len(topology.Modems))
	if topology.ModemCondition != agentlink.ModemReady {
		return result
	}
	for _, modem := range topology.Modems {
		if modem.Condition != "ready" || modem.SIM.State != "ready" || modem.SIM.ICCID == "" ||
			modem.SIM.SessionGeneration == "" || modem.AT.State != "ready" ||
			!modem.AT.CallSignalling && !modem.AT.SMS {
			continue
		}
		result = append(result, scanTarget{Fence: Fence{AttachmentID: modem.AttachmentID,
			EquipmentID: modem.EquipmentID, CardID: modem.SIM.ICCID,
			SIMSessionGeneration: modem.SIM.SessionGeneration}, Call: modem.AT.CallSignalling, SMS: modem.AT.SMS})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].EquipmentID < result[right].EquipmentID })
	return result
}

func sameFence(topology agentlink.TopologySnapshot, expected scanTarget) bool {
	matches := 0
	for _, target := range scanTargets(topology) {
		if target.AttachmentID == expected.AttachmentID && target.EquipmentID == expected.EquipmentID &&
			target.CardID == expected.CardID && target.SIMSessionGeneration == expected.SIMSessionGeneration {
			matches++
		}
	}
	return matches == 1
}
