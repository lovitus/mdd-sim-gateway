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
	if readback.EquipmentID != want.EquipmentID || readback.CardID != want.CardID ||
		readback.SIMSessionGeneration != want.SIMSessionGeneration || readback.IMSI != want.IMSI ||
		readback.MCC != want.MCC || readback.MNC != want.MNC || readback.IMEI != want.IMEI ||
		readback.SMSC != want.SMSC {
		return errors.New("provision readback does not match requested state")
	}
	// These values are optional on the request and are not exposed by every
	// modem control plane. A platform adapter must prove them when the request
	// supplies them, but an unavailable optional observation is not silently
	// treated as a mismatch.
	if want.IMEISV != "" && readback.IMEISV != want.IMEISV ||
		want.MSISDN != "" && readback.MSISDN != want.MSISDN ||
		want.ReaderPort != "" && readback.ReaderPort != want.ReaderPort ||
		want.APN != "" && readback.APN != want.APN {
		return errors.New("provision optional readback does not match requested state")
	}
	return nil
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

func provisionErrorCode(err error) string {
	if strings.Contains(err.Error(), "not ready") {
		return "provision_target_not_ready"
	}
	return "provision_target_replaced"
}
