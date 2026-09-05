package agenthost

import (
	"context"
	"errors"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcall"
)

type ProvisionReadback = agentlink.ProvisionReadback

// ProvisionHardware is the platform boundary for the complete modem/SIM
// transaction. The adapter must not report success until all requested
// changes are committed and read back. It must keep credentials and raw
// transport details inside the platform implementation.
type ProvisionHardware interface {
	ApplyProvision(context.Context, agentlink.ProvisionRequest) (step string, readback ProvisionReadback, err error)
	ReadProvision(context.Context, agentlink.ProvisionRequest) (step string, readback ProvisionReadback, err error)
}

// ReconcileProvision performs a fresh, read-only observation for an exact
// attachment. It never changes durable desired state or writes the card.
func (worker *Worker) ReconcileProvision(ctx context.Context, request agentlink.ProvisionRequest) agentlink.ProvisionResponse {
	response := agentlink.ProvisionResponse{
		OperationID: request.OperationID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, SIMSessionGeneration: request.SIMSessionGeneration,
	}
	if err := request.Validate(); err != nil {
		response.State, response.Step, response.ErrorCode = agentlink.ProvisionFailed, "validate_request", "invalid_provision_request"
		return response
	}
	if worker.config.ProvisionHardware == nil {
		response.State, response.Step, response.ErrorCode = agentlink.ProvisionUnknown, "hardware_executor", "provision_executor_unavailable"
		return response
	}
	if worker.config.ModemAuxiliary == nil {
		response.State, response.Step, response.ErrorCode = agentlink.ProvisionUnknown, "call_coordination", "provision_call_coordination_unavailable"
		return response
	}
	currentGeneration, err := provisionReadbackTarget(worker.Topology(), request)
	if err != nil {
		response.State, response.Step, response.ErrorCode = agentlink.ProvisionUnknown, "identity_fence", provisionErrorCode(err)
		return response
	}
	request.SIMSessionGeneration = currentGeneration
	response.SIMSessionGeneration = currentGeneration
	var step string
	var readback ProvisionReadback
	err = worker.config.ModemAuxiliary.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
		var readErr error
		step, readback, readErr = worker.config.ProvisionHardware.ReadProvision(operationContext, request)
		if readErr != nil {
			return readErr
		}
		if err := validateProvisionReadback(request, readback); err != nil {
			return provisionReadbackError{err: err}
		}
		return provisionTarget(worker.Topology(), request)
	})
	response.Step = step
	if err != nil {
		response.State, response.ErrorCode = agentlink.ProvisionUnknown, provisionFailureCode(err, "provision_reconcile_failed")
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			response.ErrorCode = "provision_reconcile_interrupted"
		}
		if errors.Is(err, agentcall.ErrAuxiliaryDuringCall) {
			response.ErrorCode = "provision_active_call"
		}
		return response
	}
	response.State = agentlink.ProvisionApplied
	response.Step = "reconcile_readback"
	return response
}

// ExecuteProvision owns the identity fence around the platform transaction.
// A topology change after the transaction is deliberately reported unknown:
// the caller must reconcile observed state before changing durable desired
// state again.
func (worker *Worker) ExecuteProvision(ctx context.Context, request agentlink.ProvisionRequest) agentlink.ProvisionResponse {
	response := agentlink.ProvisionResponse{
		OperationID: request.OperationID, EquipmentID: request.EquipmentID,
		CardID: request.CardID, SIMSessionGeneration: request.SIMSessionGeneration,
	}
	if err := request.Validate(); err != nil {
		response.State = agentlink.ProvisionFailed
		response.Step = "validate_request"
		response.ErrorCode = "invalid_provision_request"
		return response
	}
	if worker.config.ProvisionHardware == nil {
		response.State = agentlink.ProvisionUnknown
		response.Step = "hardware_executor"
		response.ErrorCode = "provision_executor_unavailable"
		return response
	}
	if worker.config.ModemAuxiliary == nil {
		response.State = agentlink.ProvisionUnknown
		response.Step = "call_coordination"
		response.ErrorCode = "provision_call_coordination_unavailable"
		return response
	}
	before := worker.Topology()
	if err := provisionTarget(before, request); err != nil {
		response.State = agentlink.ProvisionUnknown
		response.Step = "identity_fence"
		response.ErrorCode = provisionErrorCode(err)
		return response
	}
	var step string
	err := worker.config.ModemAuxiliary.DoAuxiliary(ctx, request.EquipmentID, func(operationContext context.Context) error {
		var applyErr error
		var readback ProvisionReadback
		step, readback, applyErr = worker.config.ProvisionHardware.ApplyProvision(operationContext, request)
		if applyErr != nil {
			return applyErr
		}
		if err := validateProvisionReadback(request, readback); err != nil {
			return provisionReadbackError{err: err}
		}
		if postconditionErr := provisionTarget(worker.Topology(), request); postconditionErr != nil {
			return provisionPostconditionError{err: postconditionErr}
		}
		return nil
	})
	response.Step = step
	if err != nil {
		response.State = agentlink.ProvisionFailed
		response.ErrorCode = "provision_hardware_failed"
		var postconditionErr provisionPostconditionError
		if errors.As(err, &postconditionErr) {
			response.State = agentlink.ProvisionUnknown
			response.Step = "postcondition_fence"
			response.ErrorCode = provisionErrorCode(postconditionErr.err)
		}
		var readbackErr provisionReadbackError
		if errors.As(err, &readbackErr) {
			response.State = agentlink.ProvisionUnknown
			response.Step = "readback"
			response.ErrorCode = "provision_readback_mismatch"
		}
		if errors.Is(err, agentcall.ErrAuxiliaryDuringCall) {
			response.State = agentlink.ProvisionUnknown
			response.Step = "call_coordination"
			response.ErrorCode = "provision_active_call"
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			response.State = agentlink.ProvisionUnknown
			response.ErrorCode = "provision_hardware_interrupted"
		}
		return response
	}
	response.State = agentlink.ProvisionApplied
	return response
}

type provisionPostconditionError struct{ err error }

func (err provisionPostconditionError) Error() string { return err.err.Error() }
func (err provisionPostconditionError) Unwrap() error { return err.err }

type provisionReadbackError struct{ err error }

func (err provisionReadbackError) Error() string { return err.err.Error() }
func (err provisionReadbackError) Unwrap() error { return err.err }

func validateProvisionReadback(request agentlink.ProvisionRequest, readback ProvisionReadback) error {
	want := request.ProvisionCommand
	checks := []struct {
		matches bool
		code    string
	}{
		{readback.EquipmentID == want.EquipmentID, "provision_equipment_readback_mismatch"},
		{readback.CardID == want.CardID, "provision_card_readback_mismatch"},
		{readback.SIMSessionGeneration == want.SIMSessionGeneration, "provision_session_readback_mismatch"},
		{readback.IMSI == want.IMSI, "provision_imsi_readback_mismatch"},
		{readback.MCC == want.MCC && readback.MNC == want.MNC, "provision_plmn_readback_mismatch"},
		{readback.IMEI == want.IMEI, "provision_imei_readback_mismatch"},
		{readback.SMSC == want.SMSC, "provision_smsc_readback_mismatch"},
	}
	for _, check := range checks {
		if !check.matches {
			return provisionCodedError{code: check.code}
		}
	}
	// These values are optional on the request and are not exposed by every
	// modem control plane. A platform adapter must prove them when the request
	// supplies them, but an unavailable optional observation is not silently
	// treated as a mismatch.
	optional := []struct {
		requested bool
		matches   bool
		code      string
	}{
		{want.IMEISV != "", readback.IMEISV == want.IMEISV, "provision_imeisv_readback_mismatch"},
		{want.MSISDN != "", readback.MSISDN == want.MSISDN, "provision_msisdn_readback_mismatch"},
		{want.ReaderPort != "", readback.ReaderPort == want.ReaderPort, "provision_reader_readback_mismatch"},
		{want.APN != "", readback.APN == want.APN, "provision_apn_readback_mismatch"},
	}
	for _, check := range optional {
		if check.requested && !check.matches {
			return provisionCodedError{code: check.code}
		}
	}
	return nil
}

type provisionCodedError struct{ code string }

func (err provisionCodedError) Error() string                { return err.code }
func (err provisionCodedError) ProvisionFailureCode() string { return err.code }

func provisionFailureCode(err error, fallback string) string {
	var coded interface{ ProvisionFailureCode() string }
	if errors.As(err, &coded) {
		if code := strings.TrimSpace(coded.ProvisionFailureCode()); code != "" {
			return code
		}
	}
	return fallback
}

func provisionTarget(topology agentlink.TopologySnapshot, request agentlink.ProvisionRequest) error {
	matches := 0
	for _, modem := range topology.Modems {
		if modem.AttachmentID != request.AttachmentID || modem.EquipmentID != request.EquipmentID ||
			modem.SIM.ICCID != request.CardID || modem.SIM.SessionGeneration != request.SIMSessionGeneration {
			continue
		}
		matches++
		if modem.Condition != "ready" || modem.AT.State != "ready" || modem.SIM.State != "ready" {
			return errors.New("provision target is not ready")
		}
		if modem.Policy != nil && (modem.Policy.ConnectionActive || modem.Policy.DataLease != nil) {
			return errors.New("provision target has an active data lease")
		}
	}
	if matches != 1 {
		return errors.New("provision target identity changed")
	}
	return nil
}

func provisionReadbackTarget(topology agentlink.TopologySnapshot, request agentlink.ProvisionRequest) (string, error) {
	// Read-only reconciliation may rebind a stale session generation, but only
	// after every stable physical/card identity still selects exactly one target.
	// ExecuteProvision continues to use provisionTarget and never takes this path.
	matches := 0
	generation := ""
	for _, modem := range topology.Modems {
		if modem.AttachmentID != request.AttachmentID || modem.EquipmentID != request.EquipmentID ||
			modem.SIM.ICCID != request.CardID {
			continue
		}
		matches++
		if modem.Condition != "ready" || modem.AT.State != "ready" || modem.SIM.State != "ready" ||
			modem.SIM.SessionGeneration == "" {
			return "", errors.New("provision target is not ready")
		}
		if modem.Policy != nil && (modem.Policy.ConnectionActive || modem.Policy.DataLease != nil) {
			return "", errors.New("provision target has an active data lease")
		}
		generation = modem.SIM.SessionGeneration
	}
	if matches != 1 {
		return "", errors.New("provision target identity changed")
	}
	return generation, nil
}

func provisionErrorCode(err error) string {
	if strings.Contains(err.Error(), "not ready") {
		return "provision_target_not_ready"
	}
	return "provision_target_replaced"
}
