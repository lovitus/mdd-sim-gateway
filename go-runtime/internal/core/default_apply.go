package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
)

type defaultProviderApplier interface {
	ApplyAdded(context.Context, uint64, string) (provideradmin.ApplyResult, error)
}

func (handler *ProvisionHandler) ApplyDefaultProvider(ctx context.Context, lineID, claimID string, apply defaultProviderApplier) error {
	if apply == nil {
		return nil
	}
	provision, found, err := handler.store.GetOperation(claimID + "-provision")
	if err != nil {
		return err
	}
	if !found || provision.Kind != linecatalog.OperationProvision || provision.State != linecatalog.OperationSucceeded ||
		provision.LineID != lineID || provision.EnableAfterSuccess == nil || !*provision.EnableAfterSuccess {
		return nil
	}
	line, revision, err := handler.store.GetWithRevision(lineID)
	if err != nil {
		return err
	}
	if !line.Enabled || line.Deleted || line.CardID != provision.CardID || line.HardwareProvisionState != "provisioned" {
		return nil
	}
	operationID := claimID + "-apply"
	if _, found, err := handler.store.GetOperation(operationID); err != nil || found {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d", operationID, revision))))
	now := handler.now().UTC()
	receipt := linecatalog.OperationReceipt{SchemaVersion: linecatalog.OperationSchemaVersion, OperationID: operationID,
		Kind: linecatalog.OperationDefaultApply, State: linecatalog.OperationInProgress, CreatedAt: now, UpdatedAt: now,
		RequestDigest: digest, ExpectedCatalogRevision: revision, LineID: line.ID, CardID: line.CardID, Step: "provider_apply", AttemptCount: 1}
	if err := handler.store.PutOperation(receipt); err != nil {
		return err
	}
	operationContext, cancel := context.WithTimeout(ctx, 130*time.Second)
	result, applyErr := apply.ApplyAdded(operationContext, revision, lineID)
	cancel()
	receipt.UpdatedAt = handler.now().UTC()
	receipt.State = linecatalog.OperationUnknown
	receipt.ErrorCode = "default_provider_apply_unconfirmed"
	var rejected *provideradmin.Error
	if errors.As(applyErr, &rejected) {
		switch rejected.Code {
		case "catalog_revision_changed", "configuration_apply_in_progress", "provider_apply_in_progress", "egress_status_invalid", "provider_render_failed", "provider_apply_blocked", "scoped_apply_unavailable":
			receipt.State = linecatalog.OperationFailed
			receipt.ErrorCode = rejected.Code
		}
	}
	if applyErr == nil && result.CatalogRevision == revision && result.State == "applied" {
		receipt.State = linecatalog.OperationSucceeded
		receipt.CommittedCatalogRevision = revision
		receipt.ErrorCode = ""
		receipt.OutcomeCode = "default_provider_applied"
	}
	return handler.store.UpdateOperationCAS(receipt, linecatalog.OperationInProgress, digest)
}
