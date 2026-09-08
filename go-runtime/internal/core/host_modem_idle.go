package core

import (
	"net/http"
	"sort"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const HostModemIdlePath = "/v1/host-modem/idle"

// The local authenticated helper receives a bounded read-only admission view.
// This snapshot is not itself a maintenance lease.
func HostModemIdleHandler(agentID string, catalog *linecatalog.Store, statuses func() []agentlink.ConnectionStatus, checks ...func(string) (bool, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet || r.URL.RawQuery != "" {
			writeJSON(w, 400, map[string]string{"code": "invalid_host_idle_request"})
			return
		}
		var target *agentlink.ConnectionStatus
		for _, status := range statuses() {
			if status.AgentID == agentID {
				copy := status
				target = &copy
				break
			}
		}
		if target == nil || target.Topology == nil || target.LastReport.IsZero() || time.Since(target.LastReport) > 30*time.Second || time.Until(target.LastReport) > 5*time.Second {
			writeJSON(w, 409, map[string]string{"code": "host_agent_snapshot_unavailable"})
			return
		}
		snapshot, err := catalog.Snapshot()
		if err != nil {
			writeJSON(w, 503, map[string]string{"code": "catalog_snapshot_unavailable"})
			return
		}
		code := hostModemIdleCode(*target.Topology, snapshot.Lines, checks)
		if code != "" {
			writeJSON(w, 409, map[string]string{"code": code})
			return
		}
		cards := map[string]bool{}
		for _, reader := range target.Topology.Readers {
			cards[reader.CardID] = true
		}
		for _, modem := range target.Topology.Modems {
			cards[modem.SIM.ICCID] = true
		}
		lineIDs := []string{}
		for _, line := range snapshot.Lines {
			if cards[line.CardID] {
				lineIDs = append(lineIDs, line.ID)
			}
		}
		sort.Strings(lineIDs)
		writeJSON(w, 200, map[string]any{"ready": true, "agent_id": agentID, "process_generation": target.ProcessGeneration, "catalog_revision": snapshot.Revision, "line_ids": lineIDs, "observed_at": target.LastReport})
	})
}

func hostModemIdleCode(topology agentlink.TopologySnapshot, lines []linecatalog.Line, checks []func(string) (bool, error)) string {
	if len(checks) == 0 || topology.ReaderCondition != agentlink.ReaderReady || (topology.ModemCondition != agentlink.ModemReady && topology.ModemCondition != agentlink.ModemDisabled) {
		return "host_agent_state_unconfirmed"
	}
	if len(topology.RawUSBSessions) > 0 || len(topology.RawUSBRecoveries) > 0 {
		return "host_raw_ownership_active"
	}
	cards := map[string]bool{}
	busyChip := func(chip *agentlink.EUICCFact) bool {
		if chip == nil || chip.Download == nil {
			return false
		}
		switch chip.Download.Job.State {
		case agentlink.EUICCDownloadCompleted, agentlink.EUICCDownloadFailed, agentlink.EUICCDownloadCanceled:
			return false
		default:
			return true
		}
	}
	for _, reader := range topology.Readers {
		if reader.CardID != "" {
			cards[reader.CardID] = true
		} else if reader.CardPresent {
			return "host_card_identity_unconfirmed"
		}
		if busyChip(reader.EUICC) {
			return "host_euicc_operation_active"
		}
		for _, slot := range reader.SecureElements {
			if busyChip(&slot.EUICC) {
				return "host_euicc_operation_active"
			}
		}
	}
	for _, modem := range topology.Modems {
		cards[modem.SIM.ICCID] = true
		if modem.Network.Data != "disconnected" || modem.Policy != nil && (modem.Policy.ConnectionActive || modem.Policy.DataLease != nil) {
			return "host_cellular_data_active_or_unknown"
		}
	}
	for _, line := range lines {
		if !cards[line.CardID] {
			continue
		}
		for _, check := range checks {
			if check == nil {
				return "host_activity_unavailable"
			}
			active, err := check(line.ID)
			if err != nil {
				return "host_activity_unavailable"
			}
			if active {
				return "host_line_busy"
			}
		}
	}
	return ""
}
