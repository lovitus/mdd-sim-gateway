package linebootstrap

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

// ReconcileDefaultDrafts runs within Core's existing reconciliation cycle.
func (service *Service) ReconcileDefaultDrafts(ctx context.Context, authority string, key []byte) error {
	snapshot, err := service.Project()
	if err != nil {
		return err
	}
	for _, candidate := range snapshot.Candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !defaultClaimAuthorized(candidate, authority, key) {
			continue
		}
		// The ID is stable across catalog revisions, so deletion or an unknown
		// response cannot turn a repeated observation into a second claim.
		operationID := defaultClaimID(candidate, authority)
		if candidate.ConfiguredLineID != "" {
			if err := service.prepareDefaultDraft(ctx, candidate, operationID); err != nil {
				return err
			}
			continue
		}
		if !candidate.CanClaim {
			continue
		}
		result, err := service.ClaimDefaultDraft(operationID, candidate.CandidateID, snapshot.CatalogRevision, authority, key)
		if err != nil {
			return err
		}
		snapshot.CatalogRevision = result.Revision
	}
	return nil
}

func defaultClaimID(candidate Candidate, authority string) string {
	firstSeen := ""
	if candidate.Enrollment != nil {
		firstSeen = candidate.Enrollment.FirstSeen.UTC().Format(time.RFC3339Nano)
	}
	digest := sha256.Sum256([]byte(authority + "\x00" + candidate.AgentID + "\x00" + candidate.EquipmentID + "\x00" + candidate.CardID + "\x00" + firstSeen))
	return fmt.Sprintf("default-%x", digest[:16])
}

// ClaimDefaultDraft is the stopped-draft step of ec620942's automatic setup.
// It shares the explicit claim ledger; it never provisions or starts a line.
func (service *Service) ClaimDefaultDraft(operationID, candidateID string, expectedRevision uint64, authority string, key []byte) (ClaimResult, error) {
	snapshot, err := service.Project()
	if err != nil {
		return ClaimResult{}, err
	}
	for _, candidate := range snapshot.Candidates {
		if candidate.CandidateID != candidateID {
			continue
		}
		if candidate.ConfiguredLineID != "" || !candidate.CanClaim || !defaultClaimAuthorized(candidate, authority, key) {
			return ClaimResult{}, ErrCandidateBlocked
		}
		return service.ClaimWithOperation(operationID, candidateID, "", expectedRevision)
	}
	return ClaimResult{}, ErrCandidateStale
}

func defaultClaimAuthorized(candidate Candidate, authority string, key []byte) bool {
	enrollment := candidate.Enrollment
	if candidate.Kind != "modem" || enrollment == nil || enrollment.Validate() != nil || enrollment.Protected || !enrollment.Initialized {
		return false
	}
	var template *agentlink.DeviceDefaults = enrollment.Template
	return template != nil && template.Authority == authority && template.VoWiFiEnabled && template.AuthorizedBy(key)
}
