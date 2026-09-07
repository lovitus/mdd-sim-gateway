package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/boltsnapshot"
	bolt "go.etcd.io/bbolt"
)

var devicePresentationBucket = []byte("device-presentation-v1")
var ErrDevicePresentationChanged = errors.New("device presentation changed or device is online")

// DevicePresentation preserves the customized MDD device_state.py hide/unhide
// semantics. These records are observations only, never routing or desired state.
type DevicePresentation struct{ db *bolt.DB }
type devicePresentationRecord struct {
	Device  DeviceProjection `json:"device"`
	Version string           `json:"version"`
	SeenAt  time.Time        `json:"seen_at"`
	Live    bool             `json:"live"`
	Hidden  bool             `json:"hidden"`
}

func OpenDevicePresentation(path string) (*DevicePresentation, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("device presentation path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucketIfNotExists(devicePresentationBucket); return err }); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DevicePresentation{db: db}, nil
}
func (store *DevicePresentation) Close() error            { return store.db.Close() }
func (store *DevicePresentation) Backup() ([]byte, error) { return boltsnapshot.Read(store.db) }

func (store *DevicePresentation) project(snapshot DeviceSnapshot) (DeviceSnapshot, error) {
	result := snapshot
	result.Devices = []DeviceProjection{}
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(devicePresentationBucket)
		live := map[string]bool{}
		for _, device := range snapshot.Devices {
			if device.ID == "" || device.ObservedOnly || live[device.ID] {
				return errors.New("invalid live device presentation")
			}
			live[device.ID] = true
			payload, err := json.Marshal(device)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(payload)
			version := hex.EncodeToString(hash[:])
			var prior devicePresentationRecord
			if previous := bucket.Get([]byte(device.ID)); previous != nil {
				if err := json.Unmarshal(previous, &prior); err != nil {
					return err
				}
				if prior.Version == version && prior.Live && !prior.Hidden {
					continue
				}
			}
			seenAt := device.LastObservedAt
			if seenAt.IsZero() {
				seenAt = snapshot.At
			}
			record := devicePresentationRecord{Device: device, Version: version, SeenAt: seenAt, Live: true}
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if err := bucket.Put([]byte(device.ID), encoded); err != nil {
				return err
			}
		}
		updates := map[string][]byte{}
		err := bucket.ForEach(func(key, value []byte) error {
			var record devicePresentationRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			if record.Device.ID != string(key) || record.Version == "" {
				return errors.New("invalid stored device presentation")
			}
			if !live[string(key)] && record.Live {
				record.Live = false
				encoded, err := json.Marshal(record)
				if err != nil {
					return err
				}
				updates[string(key)] = encoded
			}
			if record.Hidden {
				return nil
			}
			device := record.Device
			device.ObservationVersion = record.Version
			device.LastObservedAt = record.SeenAt
			if !record.Live {
				device.ObservedOnly = true
				device.Condition = "offline"
				device.Code = "last_observed_device_offline"
				device.ProcessGeneration = ""
				for i := range device.Endpoints {
					device.Endpoints[i].OperationCandidate = false
				}
			}
			result.Devices = append(result.Devices, device)
			return nil
		})
		if err != nil {
			return err
		}
		for key, value := range updates {
			if err := bucket.Put([]byte(key), value); err != nil {
				return err
			}
		}
		return nil
	})
	sort.Slice(result.Devices, func(i, j int) bool { return result.Devices[i].ID < result.Devices[j].ID })
	return result, err
}

func (store *DevicePresentation) hide(id, version string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(devicePresentationBucket)
		payload := bucket.Get([]byte(id))
		if payload == nil {
			return ErrDevicePresentationChanged
		}
		var record devicePresentationRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			return err
		}
		if record.Live || record.Version != version {
			return ErrDevicePresentationChanged
		}
		record.Hidden = true
		next, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if bytes.Equal(payload, next) {
			return nil
		}
		return bucket.Put([]byte(id), next)
	})
}

func (s *Server) hideDevice(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if s.devicePresentation == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "device_presentation_unavailable"})
		return
	}
	var input struct {
		ExpectedObservation string `json:"expected_observation"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1024))
	decoder.DisallowUnknownFields()
	if request.URL.RawQuery != "" || decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(input.ExpectedObservation) != 64 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_device_hide_request"})
		return
	}
	if _, err := s.currentDevices(); err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "device_projection_unavailable"})
		return
	}
	if err := s.devicePresentation.hide(request.PathValue("deviceID"), input.ExpectedObservation); err != nil {
		code, status := "device_presentation_write_failed", http.StatusInternalServerError
		if errors.Is(err, ErrDevicePresentationChanged) {
			code, status = "device_not_offline_or_changed", http.StatusConflict
		}
		writeJSON(response, status, map[string]string{"code": code})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"hidden": true, "data_preserved": true, "reappears_on_heartbeat": true})
}
