package notifications

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

type HostAlertView struct {
	Key          string    `json:"key"`
	Code         string    `json:"code"`
	Scope        string    `json:"scope"`
	Severity     string    `json:"severity"`
	Occurrence   uint64    `json:"occurrence"`
	Acknowledged bool      `json:"acknowledged"`
	Recovering   bool      `json:"recovering"`
	LastObserved time.Time `json:"last_observed"`
}

func (store *Store) HostAlerts() ([]HostAlertView, error) {
	result := []HostAlertView{}
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketHostState).ForEach(func(key, wire []byte) error {
			var state hostAlertState
			if err := json.Unmarshal(wire, &state); err != nil {
				return err
			}
			if state.Active {
				result = append(result, HostAlertView{Key: string(key), Code: state.Code, Scope: state.Scope, Severity: state.Severity, Occurrence: state.Occurrence, Acknowledged: state.Acknowledged, Recovering: !state.MissingSince.IsZero(), LastObserved: state.LastObserved})
			}
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool {
		if result[i].Severity != result[j].Severity {
			return result[i].Severity == "critical"
		}
		return result[i].Key < result[j].Key
	})
	return result, err
}

func (store *Store) AcknowledgeHostAlert(key string, occurrence uint64) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketHostState)
		wire := bucket.Get([]byte(key))
		var state hostAlertState
		if wire == nil || json.Unmarshal(wire, &state) != nil || !state.Active || state.Occurrence != occurrence {
			return ErrConflict
		}
		state.Acknowledged = true
		return putJSON(bucket, []byte(key), state)
	})
}

func (handler *Handler) hostAlerts(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.RawQuery != "" {
		writeNotificationJSON(response, 400, map[string]string{"code": "invalid_host_alert_request"})
		return
	}
	if request.Method == http.MethodPost {
		var input struct {
			Key        string `json:"key"`
			Occurrence uint64 `json:"occurrence"`
		}
		if err := decodeNotificationRequest(request.Body, &input); err != nil || !identifier(input.Key, 256) {
			writeNotificationJSON(response, 400, map[string]string{"code": "invalid_host_alert_request"})
			return
		}
		if err := handler.store.AcknowledgeHostAlert(input.Key, input.Occurrence); err != nil {
			if errors.Is(err, ErrConflict) {
				writeNotificationJSON(response, 409, map[string]string{"code": "host_alert_changed"})
			} else {
				writeNotificationJSON(response, 503, map[string]string{"code": "host_alerts_unavailable"})
			}
			return
		}
	}
	items, err := handler.store.HostAlerts()
	if err != nil {
		writeNotificationJSON(response, 503, map[string]string{"code": "host_alerts_unavailable"})
		return
	}
	writeNotificationJSON(response, 200, map[string]any{"alerts": items})
}
