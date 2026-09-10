package euiccprofiles

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"mime"
	"net/http"
	"strconv"
)

func (service *Service) archivedNotifications(w http.ResponseWriter, r *http.Request) {
	if service.deletions == nil {
		writeJSON(w, 503, map[string]string{"code": "euicc_deletion_store_unavailable"})
		return
	}
	eid := r.PathValue("eid")
	if r.Method == http.MethodGet {
		all, err := service.deletions.EUICCNotificationArchives(eid)
		if err != nil {
			writeJSON(w, 500, map[string]string{"code": "notification_archive_read_failed"})
			return
		}
		writeJSON(w, 200, map[string]any{"archives": all})
		return
	}
	seq, err := strconv.ParseInt(r.PathValue("sequence"), 10, 64)
	media, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var input struct {
		OperationID string `json:"operation_id"`
		Hash        string `json:"archive_sha256"`
		ICCID       string `json:"confirm_iccid"`
		Confirmed   bool   `json:"confirm_operator_deactivation"`
		Retain      bool   `json:"confirm_retain_notification"`
	}
	if err != nil || seq < 0 || mediaErr != nil || media != "application/json" || decodeStrict(r, &input) != nil || !input.Confirmed || !input.Retain {
		writeJSON(w, 400, map[string]string{"code": "notification_replay_multiple_confirmations_required"})
		return
	}
	archive, err := service.deletions.EUICCNotificationArchive(eid, seq)
	if err != nil || archive.Entry.ICCID != input.ICCID || archive.SHA256 != input.Hash {
		writeJSON(w, 409, map[string]string{"code": "notification_archive_identity_changed"})
		return
	}
	command := agentlink.EUICCNotificationCommand{OperationID: input.OperationID, EID: eid, Action: agentlink.EUICCNotificationReplay, Expected: &archive.Entry, Payload: archive.Payload}
	if command.Validate() != nil {
		writeJSON(w, 400, map[string]string{"code": "invalid_notification_replay"})
		return
	}
	if !service.replayMu.TryLock() {
		writeJSON(w, 409, map[string]string{"code": "notification_replay_in_progress"})
		return
	}
	defer service.replayMu.Unlock()
	archive, created, err := service.deletions.BeginEUICCReplay(eid, seq, input.OperationID, input.Hash)
	if err != nil {
		writeJSON(w, 409, map[string]string{"code": "notification_replay_record_failed"})
		return
	}
	if !created {
		archive.Payload = nil
		writeJSON(w, 200, map[string]any{"archive": archive, "resent": false})
		return
	}
	result, sendErr := service.agents.ExecuteEUICCNotificationCommand(r.Context(), command)
	state := "unknown"
	if result.Acknowledged {
		state = "acknowledged"
	} else if result.Failure != nil && result.Failure.Code == "euicc_notification_receiver_rejected" {
		state = "failed"
	}
	if err := service.deletions.FinishEUICCReplay(eid, seq, input.OperationID, state); err != nil {
		writeJSON(w, 500, map[string]string{"code": "notification_replay_result_unconfirmed"})
		return
	}
	_ = sendErr
	writeJSON(w, 200, map[string]any{"operation_id": input.OperationID, "state": state, "notification_retained": true, "removed": false})
}

func (service *Service) archiveDeletionNotification(r *http.Request, eid string, entry agentlink.EUICCNotificationEntry) error {
	if service.deletions == nil {
		return nil
	}
	if prior, err := service.deletions.EUICCNotificationArchive(eid, entry.SequenceNumber); err == nil && prior.Entry == entry {
		return nil
	}
	id, err := notificationOperationID()
	if err != nil {
		return err
	}
	result, err := service.agents.ExecuteEUICCNotificationCommand(r.Context(), agentlink.EUICCNotificationCommand{OperationID: id, EID: eid, Action: agentlink.EUICCNotificationArchive, Expected: &entry})
	if err != nil {
		return err
	}
	_, err = service.deletions.SaveEUICCNotification(eid, entry, result.Payload)
	return err
}
