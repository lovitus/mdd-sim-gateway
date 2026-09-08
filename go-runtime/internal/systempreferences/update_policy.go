package systempreferences

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/updatenetwork"
)

// The updater gets only a version fence here, not other security preferences.
func NewUpdatePolicyHandler(store *Store, token string) (http.Handler, error) {
	if store == nil || len(token) < 32 {
		return nil, errors.New("invalid update policy snapshot configuration")
	}
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		header := request.Header.Get("Authorization")
		actual := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
		if !strings.HasPrefix(header, "Bearer ") || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			writeJSON(response, 401, map[string]string{"code": "unauthorized"})
			return
		}
		if request.Method != http.MethodGet || request.URL.RawQuery != "" {
			writeJSON(response, 400, map[string]string{"code": "invalid_update_policy_request"})
			return
		}
		snapshot, err := store.Snapshot()
		if err != nil {
			writeJSON(response, 503, map[string]string{"code": "update_policy_unavailable"})
			return
		}
		writeJSON(response, 200, updatenetwork.PolicyRevision{SchemaVersion: 1, Revision: snapshot.Revision})
	}), nil
}
