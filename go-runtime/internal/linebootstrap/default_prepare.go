package linebootstrap

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func (service *Service) prepareDefaultDraft(ctx context.Context, candidate Candidate, claimID string) error {
	claim, found, err := service.catalog.GetOperation(claimID)
	if err != nil {
		return err
	}
	if !found || claim.Kind != linecatalog.OperationClaim || claim.LineID != candidate.ConfiguredLineID || claim.CardID != candidate.CardID {
		return nil
	}
	line, revision, err := service.catalog.GetWithRevision(candidate.ConfiguredLineID)
	if err != nil {
		return err
	}
	if line.Enabled && !line.Deleted && line.HardwareProvisionState == "provisioned" && service.defaultProvision != nil {
		return service.defaultProvision(ctx, candidate, line, claimID)
	}
	if line.Enabled || line.Deleted || line.HardwareProvisionState != "draft" || candidate.PolicyRevision == 0 {
		return nil
	}
	updated, changed := completeDraftIdentity(line, candidate.Observed)
	if changed {
		if _, _, err := service.catalog.PutExpected(updated, revision); err != nil {
			return err
		}
		line = updated
	}
	executor, ok := service.agents.(interface {
		ExecuteModemPolicyCommand(context.Context, agentlink.ModemPolicyCommand) (agentlink.ModemPolicyResponse, error)
	})
	if !ok {
		return nil
	}
	sessionDigest := sha256.Sum256([]byte(candidate.CandidateID))
	operationID := fmt.Sprintf("%s-prepare-%x", claimID, sessionDigest[:8])
	if prepared, found, err := service.catalog.GetOperation(operationID); err != nil || found {
		if err == nil && prepared.State == linecatalog.OperationSucceeded && service.defaultProvision != nil {
			return service.defaultProvision(ctx, candidate, line, claimID)
		}
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(operationID+"\x00"+candidate.CandidateID)))
	now := service.now().UTC()
	receipt := linecatalog.OperationReceipt{SchemaVersion: linecatalog.OperationSchemaVersion, OperationID: operationID,
		Kind: linecatalog.OperationSIMPrepare, State: linecatalog.OperationInProgress, CreatedAt: now, UpdatedAt: now,
		RequestDigest: digest, LineID: line.ID, CardID: line.CardID, AgentID: candidate.AgentID, ProcessGeneration: candidate.ProcessGeneration,
		AttachmentID: candidate.AttachmentID, EquipmentID: candidate.EquipmentID, SIMSessionGeneration: candidate.SessionGeneration,
		Step: "prepare_sim_apdu", AttemptCount: 1}
	if err := service.catalog.PutOperation(receipt); err != nil {
		return err
	}
	operationContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	result, operationErr := executor.ExecuteModemPolicyCommand(operationContext, agentlink.ModemPolicyCommand{
		OperationID: operationID, EquipmentID: candidate.EquipmentID, CardID: candidate.CardID,
		Action: agentlink.ModemPolicyPrepareSIMAPDU, ExpectedRevision: candidate.PolicyRevision})
	cancel()
	receipt.UpdatedAt = service.now().UTC()
	receipt.State = linecatalog.OperationUnknown
	receipt.ErrorCode = "sim_prepare_unconfirmed"
	if operationErr == nil && result.OperationID == operationID && result.SIMAPDUReady != nil && *result.SIMAPDUReady {
		receipt.State = linecatalog.OperationSucceeded
		receipt.ErrorCode = ""
		receipt.OutcomeCode = "sim_apdu_ready"
	}
	return service.catalog.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, digest)
}

// Source: ec620942 _auto_promote_card_draft's observed/stored identity merge.
// Keep edits already saved by the user; this stage never promotes or enables.
func completeDraftIdentity(line linecatalog.Line, observed ObservedIdentity) (linecatalog.Line, bool) {
	if line.Enabled || line.Deleted || line.HardwareProvisionState != "draft" ||
		(line.SIM.IMSI != "" && observed.IMSI != "" && line.SIM.IMSI != observed.IMSI) {
		return line, false
	}
	before := line.SIM
	beforeCountry := line.Network.EgressCountry
	if line.SIM.IMSI == "" && validDigits(observed.IMSI, 5, 18) {
		line.SIM.IMSI = observed.IMSI
	}
	if line.SIM.MCC == "" && validDigits(observed.MCC, 3, 3) {
		line.SIM.MCC = observed.MCC
	}
	if line.SIM.MNC == "" && validDigits(observed.MNC, 2, 3) {
		line.SIM.MNC = observed.MNC
	}
	if line.SIM.IMEI == "" && validDigits(observed.IMEI, 15, 15) {
		line.SIM.IMEI = observed.IMEI
	}
	if line.SIM.SMSC == "" && validNumber(observed.SMSC) {
		line.SIM.SMSC = observed.SMSC
	}
	if line.SIM.MSISDN == "" && validNumber(observed.MSISDN) {
		line.SIM.MSISDN = observed.MSISDN
	}
	if line.Network.EgressCountry == "" {
		line.Network.EgressCountry = linecatalog.CountryForMCC(line.SIM.MCC)
	}
	return line, before != line.SIM || beforeCountry != line.Network.EgressCountry
}
