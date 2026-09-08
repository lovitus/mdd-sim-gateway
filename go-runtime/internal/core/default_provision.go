package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linebootstrap"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

// AdvanceDefaultDraft reuses the existing manual hardware workflow, including
// its exact-card preflight and operation receipts. It does not start Providers.
func (handler *ProvisionHandler) AdvanceDefaultDraft(ctx context.Context, readback *ProvisionReadbackHandler, candidate linebootstrap.Candidate, line linecatalog.Line, claimID string) error {
	if handler.store == nil || readback == nil || line.Enabled || line.Deleted || line.HardwareProvisionState != "draft" || line.CardID != candidate.CardID {
		return nil
	}
	claim, found, err := handler.store.GetOperation(claimID)
	if err != nil {
		return err
	}
	if !found || claim.Kind != linecatalog.OperationClaim || claim.LineID != line.ID || claim.CardID != line.CardID {
		return nil
	}
	sessionDigest := sha256.Sum256([]byte(candidate.CandidateID))
	command := agentlink.ProvisionCommand{OperationID: fmt.Sprintf("%s-read-%x", claimID, sessionDigest[:8]), LineID: line.ID, LineName: line.Name, EquipmentID: candidate.EquipmentID,
		CardID: line.CardID, AttachmentID: candidate.AttachmentID, SIMSessionGeneration: candidate.SessionGeneration,
		IMSI: line.SIM.IMSI, MCC: line.SIM.MCC, MNC: line.SIM.MNC, IMEI: line.SIM.IMEI, IMEISV: line.SIM.IMEISV,
		SMSC: line.SIM.SMSC, MSISDN: line.SIM.MSISDN, APN: line.Network.ActiveAPN, IMSAPN: line.Network.IMSAPN,
		IDRMode: line.Network.IDRMode, CPMode: line.Network.CPMode, EgressCountry: line.Network.EgressCountry}
	if command.Validate() != nil {
		return nil
	}
	provisionID := claimID + "-provision"
	if _, found, err := handler.store.GetOperation(provisionID); err != nil || found {
		return err
	}
	operationContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	status, result := readback.executeReadback(operationContext, command)
	if status != http.StatusOK {
		return nil
	}
	proof, ok := result.(provisionReadbackStatus)
	if !ok || proof.State != linecatalog.OperationSucceeded {
		return errors.New("automatic draft readback was not confirmed")
	}
	preflightID := command.OperationID
	command.OperationID = provisionID
	if proof.SIMSessionGeneration != "" {
		command.SIMSessionGeneration = proof.SIMSessionGeneration
	}
	status, _ = handler.executeProvision(operationContext, provisionAPIRequest{ProvisionCommand: command, PreflightOperationID: preflightID, enableDefaultAfterSuccess: true})
	if status >= 400 {
		return errors.New("automatic draft provisioning was not completed")
	}
	return nil
}
