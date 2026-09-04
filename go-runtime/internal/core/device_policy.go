package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const maximumDevicePolicyBody = 16 << 10

type devicePolicyView struct {
	SchemaVersion        int                        `json:"schema_version"`
	DeviceID             string                     `json:"device_id"`
	AgentID              string                     `json:"agent_id"`
	ProcessGeneration    string                     `json:"process_generation"`
	AttachmentID         string                     `json:"attachment_id"`
	EquipmentID          string                     `json:"equipment_id"`
	CardID               string                     `json:"card_id"`
	SIMSessionGeneration string                     `json:"sim_session_generation"`
	Policy               agentlink.ModemPolicyFact  `json:"policy"`
	Actual               agentlink.ModemNetworkFact `json:"actual"`
	LineID               string                     `json:"line_id,omitempty"`
}

func (s *Server) devicePolicy(response http.ResponseWriter, request *http.Request) {
	device, view, err := s.devicePolicyTarget(strings.TrimSpace(request.PathValue("deviceID")))
	if err != nil {
		writeDevicePolicyError(response, err)
		return
	}
	if request.Method == http.MethodGet {
		response.Header().Set("ETag", `"`+strconv.FormatUint(view.Policy.Revision, 10)+`"`)
		writeJSON(response, http.StatusOK, view)
		return
	}
	if request.Method != http.MethodPatch || request.URL.RawQuery != "" {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	expected, err := parseDeviceRevision(request.Header.Get("If-Match"))
	if err != nil {
		writeJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "device_policy_revision_required"})
		return
	}
	var input struct {
		OperationID       string `json:"operation_id"`
		CellularEnabled   *bool  `json:"cellular_enabled,omitempty"`
		ConnectionEnabled *bool  `json:"connection_enabled,omitempty"`
		FlightMode        *bool  `json:"flight_mode,omitempty"`
		RoamingEnabled    *bool  `json:"roaming_enabled,omitempty"`
	}
	if decodeDevicePolicyBody(request, &input) != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_device_policy"})
		return
	}
	if s.modemPolicies == nil {
		writeDevicePolicyError(response, agentlink.ErrModemOffline)
		return
	}
	result, err := s.modemPolicies.ExecuteModemPolicyCommand(request.Context(), agentlink.ModemPolicyCommand{
		OperationID: input.OperationID, EquipmentID: device.Modem.EquipmentID, CardID: device.Modem.SIM.ICCID,
		Action: agentlink.ModemPolicySet, ExpectedRevision: expected,
		Patch: agentlink.ModemPolicyPatch{CellularEnabled: input.CellularEnabled,
			ConnectionEnabled: input.ConnectionEnabled, FlightMode: input.FlightMode, RoamingEnabled: input.RoamingEnabled},
	})
	if err != nil {
		writeDevicePolicyError(response, err)
		return
	}
	view.Policy = *result.Policy
	response.Header().Set("ETag", `"`+strconv.FormatUint(view.Policy.Revision, 10)+`"`)
	writeJSON(response, http.StatusOK, view)
}

func (s *Server) deviceProfiles(response http.ResponseWriter, request *http.Request) {
	device, view, err := s.devicePolicyTarget(strings.TrimSpace(request.PathValue("deviceID")))
	if err != nil {
		writeDevicePolicyError(response, err)
		return
	}
	if request.Method == http.MethodGet {
		if s.modemPolicies == nil {
			writeDevicePolicyError(response, agentlink.ErrModemOffline)
			return
		}
		result, err := s.modemPolicies.ExecuteModemPolicyCommand(request.Context(), agentlink.ModemPolicyCommand{
			OperationID: "profiles-read", EquipmentID: device.Modem.EquipmentID, CardID: device.Modem.SIM.ICCID,
			Action: agentlink.ModemPolicyProfiles})
		if err != nil {
			writeDevicePolicyError(response, err)
			return
		}
		view.Policy = *result.Policy
		response.Header().Set("ETag", `"`+strconv.FormatUint(view.Policy.Revision, 10)+`"`)
		writeJSON(response, http.StatusOK, map[string]any{"schema_version": 1, "device": view, "profiles": result.Profiles})
		return
	}
	if request.Method != http.MethodPut || request.URL.RawQuery != "" {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	if view.Policy.ProfileMode == "system_managed" {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]string{
			"code": "modem_profile_system_managed",
		})
		return
	}
	expected, err := parseDeviceRevision(request.Header.Get("If-Match"))
	if err != nil {
		writeJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "device_policy_revision_required"})
		return
	}
	var input struct {
		OperationID string `json:"operation_id"`
		Name        string `json:"name"`
		APN         string `json:"apn"`
		Auth        string `json:"auth"`
		Username    string `json:"username,omitempty"`
		Password    string `json:"password,omitempty"`
		PasswordSet bool   `json:"password_set"`
	}
	if decodeDevicePolicyBody(request, &input) != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_modem_profile"})
		return
	}
	if s.modemPolicies == nil {
		writeDevicePolicyError(response, agentlink.ErrModemOffline)
		return
	}
	result, err := s.modemPolicies.ExecuteModemPolicyCommand(request.Context(), agentlink.ModemPolicyCommand{
		OperationID: input.OperationID, EquipmentID: device.Modem.EquipmentID, CardID: device.Modem.SIM.ICCID,
		Action: agentlink.ModemPolicyProfileSave, ExpectedRevision: expected,
		Profile: agentlink.ModemProfileInput{Name: input.Name, APN: input.APN, Auth: input.Auth,
			Username: input.Username, Password: input.Password, PasswordSet: input.PasswordSet},
	})
	if err != nil {
		writeDevicePolicyError(response, err)
		return
	}
	view.Policy = *result.Policy
	response.Header().Set("ETag", `"`+strconv.FormatUint(view.Policy.Revision, 10)+`"`)
	writeJSON(response, http.StatusOK, map[string]any{"schema_version": 1, "device": view, "profiles": result.Profiles})
}

func (s *Server) devicePolicyTarget(deviceID string) (DeviceProjection, devicePolicyView, error) {
	if s.agents == nil || deviceID == "" {
		return DeviceProjection{}, devicePolicyView{}, agentlink.ErrModemOffline
	}
	snapshot, err := s.currentDevices()
	if err != nil {
		return DeviceProjection{}, devicePolicyView{}, err
	}
	for _, device := range snapshot.Devices {
		if device.ID != deviceID {
			continue
		}
		if device.Kind != "modem" || device.Mode != "adapted" || device.Modem == nil || device.Modem.Policy == nil ||
			device.Modem.SIM.State != "ready" || device.Modem.SIM.SessionGeneration == "" {
			return DeviceProjection{}, devicePolicyView{}, agentlink.ErrModemOffline
		}
		lineID := ""
		for _, endpoint := range device.Endpoints {
			if endpoint.OperationCandidate && endpoint.Line != nil && endpoint.Association == "exact" {
				if lineID != "" && lineID != endpoint.Line.ID {
					return DeviceProjection{}, devicePolicyView{}, agentlink.ErrModemAmbiguous
				}
				lineID = endpoint.Line.ID
			}
		}
		return device, devicePolicyView{SchemaVersion: 1, DeviceID: device.ID, AgentID: device.AgentID,
			ProcessGeneration: device.ProcessGeneration, AttachmentID: device.Modem.AttachmentID,
			EquipmentID: device.Modem.EquipmentID, CardID: device.Modem.SIM.ICCID,
			SIMSessionGeneration: device.Modem.SIM.SessionGeneration, Policy: *device.Modem.Policy,
			Actual: device.Modem.Network, LineID: lineID}, nil
	}
	return DeviceProjection{}, devicePolicyView{}, agentlink.ErrModemOffline
}

func parseDeviceRevision(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("invalid revision")
	}
	return strconv.ParseUint(value[1:len(value)-1], 10, 64)
}

func decodeDevicePolicyBody(request *http.Request, target any) error {
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumDevicePolicyBody+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumDevicePolicyBody {
		return errors.New("invalid body")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid body")
	}
	return nil
}

func writeDevicePolicyError(response http.ResponseWriter, err error) {
	var remote *agentlink.RemoteError
	if errors.As(err, &remote) {
		status := http.StatusConflict
		if remote.Kind == "not_ready" {
			status = http.StatusPreconditionFailed
		}
		if remote.Kind == "transport" {
			status = http.StatusGatewayTimeout
		}
		writeJSON(response, status, map[string]string{"code": remote.Code})
		return
	}
	switch {
	case errors.Is(err, agentlink.ErrModemOffline):
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "device_offline"})
	case errors.Is(err, agentlink.ErrModemAmbiguous):
		writeJSON(response, http.StatusConflict, map[string]string{"code": "device_ambiguous"})
	default:
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "device_policy_unavailable"})
	}
}
