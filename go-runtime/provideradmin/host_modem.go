package provideradmin

import (
	"context"
	"net/http"
	"time"
)

const HostModemPath = "/v1/system/host-modem"

type HostModemBinding struct {
	ConfigPath string `json:"config_path"`
	AgentID    string `json:"agent_id"`
	CoreURL    string `json:"core_url"`
}
type HostModemProfile struct {
	Name        string `json:"name"`
	VID         string `json:"vid"`
	PID         string `json:"pid"`
	ATInterface *uint8 `json:"at_interface,omitempty"`
}
type HostModemSettings struct {
	Backend  string             `json:"modem_backend"`
	Profiles []HostModemProfile `json:"modem_profiles"`
}
type HostModemSnapshot struct {
	AgentState      string            `json:"agent_state,omitempty"`
	AgentGeneration uint64            `json:"agent_generation,omitempty"`
	Revision        string            `json:"revision"`
	Settings        HostModemSettings `json:"settings"`
	RuntimeState    string            `json:"runtime_state"`
}
type HostModemRequest struct {
	ExpectedRevision string            `json:"expected_revision"`
	Settings         HostModemSettings `json:"settings"`
}
type HostModemService interface {
	HostModemSettings(context.Context) (HostModemSnapshot, error)
	SaveHostModemSettings(context.Context, HostModemRequest) (HostModemSnapshot, error)
}

func HostModemHandler(service HostModemService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.RawQuery != "" {
			writeError(w, &Error{Status: 400, Code: "invalid_host_modem_request"})
			return
		}
		var result HostModemSnapshot
		var err error
		switch r.Method {
		case http.MethodGet:
			result, err = service.HostModemSettings(r.Context())
		case http.MethodPut:
			var request HostModemRequest
			if err := decodeRequest(w, r, &request); err != nil {
				writeError(w, err)
				return
			}
			result, err = service.SaveHostModemSettings(r.Context(), request)
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeError(w, &Error{Status: 405, Code: "method_not_allowed"})
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
}
func (client *Client) HostModemSettings(ctx context.Context) (HostModemSnapshot, error) {
	var result HostModemSnapshot
	err := client.requestPath(ctx, http.MethodGet, HostModemPath, nil, &result)
	return result, err
}
func (client *Client) SaveHostModemSettings(ctx context.Context, input HostModemRequest) (HostModemSnapshot, error) {
	var result HostModemSnapshot
	copy := *client
	httpCopy := *client.http
	httpCopy.Timeout = 5 * time.Minute
	copy.http = &httpCopy
	err := copy.requestPath(ctx, http.MethodPut, HostModemPath, input, &result)
	return result, err
}
