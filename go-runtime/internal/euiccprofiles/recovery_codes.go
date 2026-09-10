package euiccprofiles

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"mime"
	"net/http"
)

func (service *Service) recoveryCodes(w http.ResponseWriter, r *http.Request) {
	if service.deletions == nil {
		writeJSON(w, 503, map[string]string{"code": "profile_recovery_unavailable"})
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var input struct {
		Mode                string `json:"mode,omitempty"`
		ActivationCode      string `json:"activation_code,omitempty"`
		ConfirmationCode    string `json:"confirmation_code,omitempty"`
		IMEI                string `json:"imei,omitempty"`
		Confirmed           bool   `json:"confirmed"`
		ICCID               string `json:"confirm_iccid"`
		DownloadOperationID string `json:"download_operation_id"`
	}
	if r.Method != http.MethodPost || err != nil || media != "application/json" || decodeStrict(r, &input) != nil || !input.Confirmed || input.ICCID != r.PathValue("iccid") {
		writeJSON(w, 400, map[string]string{"code": "recovery_code_confirmation_required"})
		return
	}
	if input.Mode == "restore" {
		command := agentlink.EUICCDownloadCommand{EID: r.PathValue("eid"), OperationID: input.DownloadOperationID, Action: agentlink.EUICCDownloadStart, ActivationCode: input.ActivationCode, ConfirmationCode: input.ConfirmationCode, IMEI: input.IMEI}
		if err := service.deletions.RestoreEUICCRecoveryCodes(command, input.ICCID); err != nil {
			writeJSON(w, 409, map[string]string{"code": "recovery_code_restore_conflict"})
			return
		}
		writeJSON(w, 200, map[string]bool{"saved": true})
		return
	}
	if input.Mode != "" && input.Mode != "reveal" {
		writeJSON(w, 400, map[string]string{"code": "invalid_recovery_code_action"})
		return
	}
	record, found, err := service.deletions.EUICCDownloadRecoveryByOperation(r.PathValue("eid"), input.DownloadOperationID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"code": "profile_recovery_unavailable"})
		return
	}
	if !found || record.ICCID != input.ICCID || !record.RetainCodes {
		writeJSON(w, 404, map[string]string{"code": "recovery_codes_not_saved"})
		return
	}
	writeJSON(w, 200, map[string]string{"activation_code": record.ActivationCode, "confirmation_code": record.ConfirmationCode})
}
