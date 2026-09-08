package core

import (
	"encoding/json"
	"io"
	"net/http"
)

const HostModemMaintenancePath = "/v1/host-modem/maintenance"

type hostAgentMaintenance interface {
	BeginHostMaintenance(string, string, string) error
	EndHostMaintenance(string, string) error
}

// Mounted only on the existing authenticated helper listener for one bound Agent.
func HostModemMaintenanceHandler(agentID string, agents hostAgentMaintenance) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost || r.URL.RawQuery != "" {
			writeJSON(w, 400, map[string]string{"code": "invalid_host_maintenance_request"})
			return
		}
		var input struct {
			Action     string `json:"action"`
			LeaseID    string `json:"lease_id"`
			Generation string `json:"process_generation"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeJSON(w, 400, map[string]string{"code": "invalid_host_maintenance_request"})
			return
		}
		var err error
		switch input.Action {
		case "begin":
			err = agents.BeginHostMaintenance(agentID, input.Generation, input.LeaseID)
		case "end":
			err = agents.EndHostMaintenance(agentID, input.LeaseID)
		default:
			writeJSON(w, 400, map[string]string{"code": "invalid_host_maintenance_action"})
			return
		}
		if err != nil {
			writeJSON(w, 409, map[string]string{"code": "host_maintenance_not_confirmed"})
			return
		}
		writeJSON(w, 200, map[string]any{"agent_id": agentID, "lease_id": input.LeaseID, "held": input.Action == "begin"})
	})
}
