package euiccprofiles

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
)

func (service *Service) deletionInventory(ctx context.Context, eid string) (*agentlink.EUICCFact, error) {
	id, err := notificationOperationID()
	if err != nil {
		return nil, err
	}
	r, err := service.agents.ExecuteEUICCProfileCommand(ctx, agentlink.EUICCProfileCommand{OperationID: id, EID: eid, Action: agentlink.EUICCProfileRefresh})
	if err != nil {
		return nil, err
	}
	if r.Outcome != agentlink.EUICCProfileRefreshed || r.Inventory == nil || r.Inventory.EID != eid || !r.Inventory.ProfilesAvailable || !r.Inventory.ProfileDeletion {
		return nil, errors.New("standard deletion inventory unavailable")
	}
	return r.Inventory, nil
}

func (service *Service) deletionDelivery(r events.EUICCDeletion) string {
	if r.State != "deleted" && r.State != "deleted_observed" {
		return "deletion_unconfirmed"
	}
	if len(r.Notifications) == 0 {
		return "pending_notification"
	}
	for _, seq := range r.Notifications {
		a, err := service.deletions.EUICCNotificationArchive(r.EID, seq)
		if err != nil {
			return "pending_notification"
		}
		ack := false
		for _, attempt := range a.Attempts {
			if attempt.State == "acknowledged" {
				ack = true
			}
		}
		if !ack {
			return "pending_delivery"
		}
	}
	return "receiver_acknowledged"
}

func (service *Service) captureDeletion(r *http.Request, record events.EUICCDeletion) error {
	if len(record.Notifications) > 0 {
		return nil
	}
	all, err := service.deletions.EUICCDeletions(record.EID)
	if err != nil {
		return err
	}
	for _, other := range all {
		if other.ICCID == record.ICCID && other.CreatedAt.After(record.CreatedAt) {
			return errors.New("notification attribution requires review")
		}
	}
	id, err := notificationOperationID()
	if err != nil {
		return err
	}
	result, err := service.agents.ExecuteEUICCNotificationCommand(r.Context(), agentlink.EUICCNotificationCommand{OperationID: id, EID: record.EID})
	if err != nil {
		return err
	}
	var sequences []int64
	for _, entry := range result.Entries {
		if entry.Event != "delete" || entry.ICCID != record.ICCID || slices.Contains(record.BeforeSequences, entry.SequenceNumber) {
			continue
		}
		if err := service.archiveDeletionNotification(r, record.EID, entry); err != nil {
			return err
		}
		sequences = append(sequences, entry.SequenceNumber)
	}
	return service.deletions.AttachEUICCDeletionNotifications(record.EID, record.OperationID, sequences)
}

func (service *Service) standardDeletion(w http.ResponseWriter, r *http.Request) {
	if service.deletions == nil {
		writeJSON(w, 503, map[string]string{"code": "deletion_store_unavailable"})
		return
	}
	eid := r.PathValue("eid")
	if r.Method == http.MethodGet {
		records, err := service.deletions.EUICCDeletions(eid)
		if err != nil {
			writeJSON(w, 500, map[string]string{"code": "deletion_history_unavailable"})
			return
		}
		result := make([]map[string]any, 0, len(records))
		for _, record := range records {
			var summary *events.EUICCRecoverySummary
			recovery, found, readErr := service.deletions.EUICCProfileRecovery(eid, record.ICCID)
			if record.DownloadOperationID != "" {
				recovery, found, readErr = service.deletions.EUICCDownloadRecoveryByOperation(eid, record.DownloadOperationID)
			} else if recovery.CreatedAt.After(record.CreatedAt) {
				found = false
			}
			if readErr != nil {
				writeJSON(w, 500, map[string]string{"code": "profile_recovery_unavailable"})
				return
			} else if found && recovery.ICCID == record.ICCID {
				value := recovery.Summary()
				summary = &value
				if record.ProfileName == "" && record.ServiceProviderName == "" {
					record.ProfileName = value.ProfileName
					record.ServiceProviderName = value.ServiceProviderName
					record.MetadataSource = "download_receipt"
				}
			}
			result = append(result, map[string]any{"operation": record, "delivery": service.deletionDelivery(record), "recovery": summary})
		}
		writeJSON(w, 200, map[string]any{"deletions": result})
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeJSON(w, 415, map[string]string{"code": "json_required"})
		return
	}
	if !service.deleteMu.TryLock() {
		writeJSON(w, 409, map[string]string{"code": "deletion_in_progress"})
		return
	}
	defer service.deleteMu.Unlock()
	if strings.HasSuffix(r.URL.Path, "/recover") {
		var empty struct{}
		if decodeStrict(r, &empty) != nil {
			writeJSON(w, 400, map[string]string{"code": "invalid_recovery_request"})
			return
		}
		record, found, err := service.deletions.EUICCDeletion(eid, r.PathValue("operation_id"))
		if err != nil || !found {
			writeJSON(w, 404, map[string]string{"code": "deletion_not_found"})
			return
		}
		inventory, err := service.deletionInventory(r.Context(), eid)
		if err != nil {
			writeEUICCError(w, err, "deletion_readback_unavailable")
			return
		}
		present := false
		for _, profile := range inventory.Profiles {
			if profile.ICCID == record.ICCID {
				present = true
			}
		}
		if record.State == "pending" || record.State == "unknown" {
			state := "deleted_observed"
			if present {
				state = "not_deleted_observed"
			}
			record, err = service.deletions.FinishEUICCDeletion(eid, record.OperationID, state, "reconciled_from_card")
			if err != nil {
				writeJSON(w, 500, map[string]string{"code": "deletion_record_unavailable"})
				return
			}
		}
		if record.State == "deleted" || record.State == "deleted_observed" {
			_ = service.captureDeletion(r, record)
		}
		record, _, err = service.deletions.EUICCDeletion(eid, record.OperationID)
		if err != nil {
			writeJSON(w, 500, map[string]string{"code": "deletion_record_unavailable"})
			return
		}
		writeJSON(w, 200, map[string]any{"operation": record, "delivery": service.deletionDelivery(record), "card_profile_present": present})
		return
	}
	var input struct {
		OperationID string `json:"operation_id"`
		ICCID       string `json:"confirm_iccid"`
		Permanent   bool   `json:"confirm_permanent_delete"`
		Pending     bool   `json:"confirm_delivery_may_be_pending"`
	}
	iccid := r.PathValue("iccid")
	if decodeStrict(r, &input) != nil || input.ICCID != iccid || !input.Permanent || !input.Pending {
		writeJSON(w, 400, map[string]string{"code": "permanent_deletion_confirmation_required"})
		return
	}
	command := agentlink.EUICCProfileCommand{OperationID: input.OperationID, EID: eid, ICCID: iccid, Action: agentlink.EUICCProfileDelete, ExpectedState: agentlink.EUICCProfileDisabled}
	if command.Validate() != nil {
		writeJSON(w, 400, map[string]string{"code": "invalid_deletion_identity"})
		return
	}
	if prior, found, err := service.deletions.EUICCDeletion(eid, input.OperationID); err != nil {
		writeJSON(w, 500, map[string]string{"code": "deletion_record_unavailable"})
		return
	} else if found {
		if prior.ICCID != iccid {
			writeJSON(w, 409, map[string]string{"code": "deletion_identity_changed"})
			return
		}
		writeJSON(w, 200, map[string]any{"operation": prior, "delivery": service.deletionDelivery(prior), "resent": false})
		return
	}
	if err := service.profileMutationSafe(r.Context(), iccid); err != nil {
		writeEUICCError(w, err, "profile_in_use")
		return
	}
	inventory, err := service.deletionInventory(r.Context(), eid)
	if err != nil {
		writeEUICCError(w, err, "deletion_readback_unavailable")
		return
	}
	matched := false
	var identity agentlink.EUICCProfileFact
	for _, p := range inventory.Profiles {
		if p.ICCID == iccid && p.State == agentlink.EUICCProfileDisabled {
			matched = true
			identity = p
		}
	}
	if !matched {
		writeJSON(w, 409, map[string]string{"code": "delete_requires_present_disabled_profile"})
		return
	}
	id, err := notificationOperationID()
	if err != nil {
		writeJSON(w, 500, map[string]string{"code": "operation_identity_unavailable"})
		return
	}
	before, err := service.agents.ExecuteEUICCNotificationCommand(r.Context(), agentlink.EUICCNotificationCommand{OperationID: id, EID: eid})
	if err != nil {
		writeEUICCError(w, err, "notification_inventory_unavailable")
		return
	}
	var sequences []int64
	for _, entry := range before.Entries {
		if entry.Event == "delete" && entry.ICCID == iccid {
			sequences = append(sequences, entry.SequenceNumber)
			if err := service.archiveDeletionNotification(r, eid, entry); err != nil {
				writeEUICCError(w, err, "deletion_notification_archive_failed")
				return
			}
		}
	}
	downloadOperation := ""
	if recovery, found, readErr := service.deletions.EUICCProfileRecovery(eid, iccid); readErr != nil {
		writeJSON(w, 500, map[string]string{"code": "profile_recovery_unavailable"})
		return
	} else if found {
		downloadOperation = recovery.OperationID
	}
	record, created, err := service.deletions.BeginEUICCDeletion(events.EUICCDeletion{EID: eid, ICCID: iccid, OperationID: input.OperationID, BeforeSequences: sequences, ProfileName: identity.ProfileName, ProfileNickname: identity.Nickname, ServiceProviderName: identity.ServiceProviderName, MetadataSource: "card_before_delete", DownloadOperationID: downloadOperation})
	if err != nil {
		writeJSON(w, 409, map[string]string{"code": "deletion_requires_reconciliation"})
		return
	}
	if !created {
		writeJSON(w, 200, map[string]any{"operation": record, "delivery": service.deletionDelivery(record), "resent": false})
		return
	}
	result, sendErr := service.agents.ExecuteEUICCProfileCommand(r.Context(), command)
	state, code := "unknown", "deletion_outcome_unknown"
	if sendErr == nil && result.Outcome == agentlink.EUICCProfileRefreshPending && result.Changed {
		state, code = "deleted", "card_delete_applied"
	}
	if result.Code != "" {
		code = result.Code
	}
	if result.Failure != nil {
		code = result.Failure.Code
	}
	record, err = service.deletions.FinishEUICCDeletion(eid, input.OperationID, state, code)
	if err != nil {
		writeJSON(w, 500, map[string]string{"code": "deletion_record_unconfirmed"})
		return
	}
	if state == "deleted" {
		_ = service.captureDeletion(r, record)
	}
	record, _, err = service.deletions.EUICCDeletion(eid, input.OperationID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"code": "deletion_record_unavailable"})
		return
	}
	writeJSON(w, 202, map[string]any{"operation": record, "delivery": service.deletionDelivery(record)})
}
