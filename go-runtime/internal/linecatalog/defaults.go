package linecatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	bolt "go.etcd.io/bbolt"
)

var providerDefaultsKey = []byte("provider-defaults-v1")

type ProviderDefaults struct {
	RekeyMinutes int `json:"rekey_minutes"`
}

func readProviderDefaults(tx *bolt.Tx) (ProviderDefaults, error) {
	var defaults ProviderDefaults
	if payload := tx.Bucket(metadataBucket).Get(providerDefaultsKey); payload != nil {
		if err := json.Unmarshal(payload, &defaults); err != nil {
			return defaults, err
		}
	}
	if defaults.RekeyMinutes < 0 || defaults.RekeyMinutes > 1440 {
		return defaults, errors.New("invalid stored provider defaults")
	}
	return defaults, nil
}

func (store *Store) PutProviderDefaults(value ProviderDefaults, expected uint64) (uint64, error) {
	if value.RekeyMinutes < 0 || value.RekeyMinutes > 1440 {
		return 0, errors.New("invalid provider rekey default")
	}
	var revision uint64
	payload, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	err = store.db.Update(func(tx *bolt.Tx) error {
		metadata := tx.Bucket(metadataBucket)
		revision = bytesUint64(metadata.Get(revisionKey))
		if revision != expected {
			return ErrRevision
		}
		if bytes.Equal(metadata.Get(providerDefaultsKey), payload) {
			return nil
		}
		if err := metadata.Put(providerDefaultsKey, payload); err != nil {
			return err
		}
		revision++
		return metadata.Put(revisionKey, uint64Bytes(revision))
	})
	return revision, err
}

type DefaultsHandler struct{ store *Store }

func NewDefaultsHandler(store *Store) *DefaultsHandler { return &DefaultsHandler{store: store} }
func (handler *DefaultsHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPut || request.URL.RawQuery != "" {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_provider_defaults_request"})
		return
	}
	revision, err := parseIfMatch(request.Header.Get("If-Match"))
	if err != nil {
		writeCatalogJSON(response, http.StatusPreconditionRequired, map[string]string{"code": "catalog_revision_required"})
		return
	}
	var input struct {
		RekeyMinutes *int `json:"rekey_minutes"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.RekeyMinutes == nil {
		writeCatalogJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_provider_defaults"})
		return
	}
	value := ProviderDefaults{RekeyMinutes: *input.RekeyMinutes}
	updated, err := handler.store.PutProviderDefaults(value, revision)
	if err != nil {
		status, code := http.StatusBadRequest, "invalid_provider_defaults"
		if errors.Is(err, ErrRevision) {
			status, code = http.StatusPreconditionFailed, "catalog_revision_changed"
		}
		writeCatalogJSON(response, status, map[string]string{"code": code})
		return
	}
	writeCatalogJSON(response, http.StatusOK, map[string]any{"revision": updated, "defaults": value})
}

func EffectiveRekeyMinutes(line Line, defaults ProviderDefaults) int {
	if line.Network.RekeyMinutes != nil {
		return *line.Network.RekeyMinutes
	}
	return defaults.RekeyMinutes
}
