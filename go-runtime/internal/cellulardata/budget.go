package cellulardata

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
)

// Only a persisted owner absent from this process is recovered here. A lost
// Core cannot refund its unobserved allowance on the strength of a new login.
func (service *Service) recoverDonor(card string) error {
	if service.config.Budgets == nil {
		return nil
	}
	b, err := service.config.Budgets.DonorBudget(card)
	if err != nil || b.Grant == nil {
		return err
	}
	service.mu.Lock()
	live := service.byID[b.Grant.SessionID]
	service.mu.Unlock()
	if live != nil {
		return egressconfig.ErrDonorOwner
	}
	var target agentlink.ModemTarget
	if json.Unmarshal(b.Grant.Target, &target) != nil || target.CardID != card || target.AgentID == "" || target.ProcessGeneration == "" {
		return errors.New("persisted donor owner is invalid")
	}
	if err := service.stopAgent(target, b.Grant.SessionID, b.Grant.Purpose); err != nil {
		return err
	}
	return service.config.Budgets.RecordDonor(card, b.Grant.SessionID, b.Grant.UsedBytes, true, true)
}

type budgetView struct {
	egressconfig.DonorBudget
	ReservedBytes  uint64 `json:"reserved_bytes"`
	RemainingBytes uint64 `json:"remaining_bytes"`
	OwnerState     string `json:"owner_state"`
}

func (service *Service) donorView(card string) (budgetView, error) {
	b, err := service.config.Budgets.DonorBudget(card)
	if err != nil {
		return budgetView{}, err
	}
	view := budgetView{DonorBudget: b, OwnerState: "stopped"}
	if b.Grant != nil {
		used := b.Grant.UsedBytes
		service.mu.Lock()
		live := service.byID[b.Grant.SessionID]
		service.mu.Unlock()
		view.OwnerState = "unconfirmed"
		if live != nil {
			used = live.used.Load()
			view.OwnerState = live.view(false).State
		}
		if used >= b.Grant.UsedBytes && used <= b.Grant.MaxBytes {
			view.UsedBytes += used - b.Grant.UsedBytes
		} else {
			used = b.Grant.UsedBytes
		}
		view.ReservedBytes = b.Grant.MaxBytes - used
	}
	view.RemainingBytes = view.LimitBytes - view.UsedBytes - view.UncertainBytes - view.ReservedBytes
	return view, nil
}

func (service *Service) budgetHTTP(response http.ResponseWriter, request *http.Request, lineID string) {
	if service.config.Budgets == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "donor_budget_unavailable"})
		return
	}
	if request.URL.RawQuery != "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_donor_budget"})
		return
	}
	line, err := service.config.Catalog.Get(lineID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if request.Method == http.MethodPut {
		var input struct {
			ExpectedCardID string `json:"expected_card_id"`
			Revision       uint64 `json:"revision"`
			LimitBytes     uint64 `json:"limit_bytes"`
			Reset          bool   `json:"reset"`
			ConfirmReset   bool   `json:"confirm_reset"`
		}
		if decodeRequest(request.Body, &input) != nil || input.ExpectedCardID != line.CardID || input.Revision == 0 || input.Reset && !input.ConfirmReset {
			writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_donor_budget"})
			return
		}
		service.admission.Lock()
		latest, latestErr := service.config.Catalog.Get(lineID)
		if latestErr != nil || latest.CardID != input.ExpectedCardID {
			err = errors.New("donor identity changed")
		} else {
			_, err = service.config.Budgets.ConfigureDonor(line.CardID, input.Revision, input.LimitBytes, input.Reset)
		}
		service.admission.Unlock()
		if err != nil {
			writeServiceError(response, err)
			return
		}
	} else if request.Method != http.MethodGet {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	view, err := service.donorView(line.CardID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, view)
}
