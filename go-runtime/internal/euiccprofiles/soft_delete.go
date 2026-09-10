package euiccprofiles

import (
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
)

func WithDeletionStore(store *events.BoltStore) Option {
	return func(service *Service) error {
		if store == nil {
			return errors.New("deletion event store required")
		}
		service.deletions = store
		return nil
	}
}

func (service *Service) softDelete(w http.ResponseWriter, r *http.Request) {
	if service.deletions == nil {
		writeJSON(w, 503, map[string]string{"code": "euicc_deletion_store_unavailable"})
		return
	}
	eid, iccid := r.PathValue("eid"), r.PathValue("iccid")
	if r.Method == http.MethodGet {
		event, found, err := service.deletions.EUICCSoftDelete(eid, iccid)
		if err != nil {
			writeJSON(w, 500, map[string]string{"code": "euicc_deletion_read_failed"})
			return
		}
		history, err := service.deletions.EUICCSoftDeleteHistory(eid, iccid)
		if err != nil {
			writeJSON(w, 500, map[string]string{"code": "euicc_deletion_read_failed"})
			return
		}
		writeJSON(w, 200, map[string]any{"found": found, "event": event, "history": history})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"code": "method_not_allowed"})
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var input struct {
		OperationID      string `json:"operation_id"`
		ExpectedNickname string `json:"expected_nickname"`
		ConfirmICCID     string `json:"confirm_iccid"`
		KeepProfile      bool   `json:"confirm_keep_profile"`
		BlockEnable      bool   `json:"confirm_block_enable"`
	}
	if err != nil || media != "application/json" || decodeStrict(r, &input) != nil || !input.KeepProfile || !input.BlockEnable || input.ConfirmICCID != iccid {
		writeJSON(w, 400, map[string]string{"code": "euicc_soft_delete_confirmation_required"})
		return
	}
	nickname := agentlink.EUICCSoftDeleteMarker + " " + input.ExpectedNickname
	if strings.TrimSpace(input.ExpectedNickname) == "" {
		nickname = agentlink.EUICCSoftDeleteMarker
	}
	command := agentlink.EUICCProfileCommand{OperationID: input.OperationID, EID: eid, ICCID: iccid, Action: agentlink.EUICCProfileNickname, Nickname: nickname, ExpectedNickname: input.ExpectedNickname}
	if command.Validate() != nil || agentlink.EUICCProfileSoftDeleted(input.ExpectedNickname) {
		writeJSON(w, 400, map[string]string{"code": "invalid_euicc_soft_delete_request"})
		return
	}
	var profile *agentlink.EUICCProfileFact
	for _, status := range service.agents.Statuses() {
		if status.Topology == nil || status.Topology.ReaderCondition != agentlink.ReaderReady {
			continue
		}
		for _, reader := range status.Topology.Readers {
			for _, slot := range agentlink.ReaderEUICCs(reader) {
				if slot.EUICC.EID != eid || !slot.EUICC.SoftDelete || !slot.EUICC.ProfilesAvailable {
					continue
				}
				for _, candidate := range slot.EUICC.Profiles {
					if candidate.ICCID == iccid {
						if profile != nil {
							writeJSON(w, 409, map[string]string{"code": "euicc_identity_ambiguous"})
							return
						}
						copy := candidate
						profile = &copy
					}
				}
			}
		}
	}
	if profile == nil || profile.State != agentlink.EUICCProfileDisabled {
		writeJSON(w, 409, map[string]string{"code": "euicc_soft_delete_requires_disabled_profile"})
		return
	}
	if profile.Nickname != input.ExpectedNickname {
		prior, found, err := service.deletions.EUICCSoftDelete(eid, iccid)
		if err != nil || !found || prior.OperationID != input.OperationID || profile.Nickname != nickname {
			writeJSON(w, 409, map[string]string{"code": "euicc_profile_nickname_changed"})
			return
		}
	}
	if err := service.profileMutationSafe(r.Context(), iccid); err != nil {
		writeEUICCError(w, err, "euicc_profile_line_active")
		return
	}
	event, err := service.deletions.BeginEUICCSoftDelete(events.EUICCSoftDeleteEvent{EID: eid, ICCID: iccid, OperationID: input.OperationID, OriginalNickname: input.ExpectedNickname, Marker: agentlink.EUICCSoftDeleteMarker})
	if err != nil {
		writeJSON(w, 409, map[string]string{"code": "euicc_deletion_event_conflict"})
		return
	}
	if event.State == "marked" {
		writeJSON(w, 200, map[string]any{"event": event})
		return
	}
	result, operationErr := service.agents.ExecuteEUICCProfileCommand(r.Context(), command)
	state := "unknown"
	if operationErr == nil && (result.Outcome == agentlink.EUICCProfileRefreshPending || result.Outcome == agentlink.EUICCProfileAlreadyApplied) {
		state = "marked"
	}
	event, err = service.deletions.FinishEUICCSoftDelete(eid, iccid, input.OperationID, state)
	if err != nil {
		writeJSON(w, 500, map[string]string{"code": "euicc_deletion_record_unconfirmed"})
		return
	}
	if state != "marked" {
		writeJSON(w, 202, map[string]any{"event": event, "code": "euicc_soft_delete_unknown"})
		return
	}
	writeJSON(w, 200, map[string]any{"event": event, "physical_profile_retained": true, "operator_notified": false})
}
