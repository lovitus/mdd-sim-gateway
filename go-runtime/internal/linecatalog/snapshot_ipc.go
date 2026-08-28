package linecatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	SnapshotIPCPath         = "/v1/catalog/snapshot"
	maximumSnapshotIPCBytes = 1 << 20
)

type SnapshotHandler struct {
	store     *Store
	tokenHash [32]byte
}

func NewSnapshotHandler(store *Store, token string) (*SnapshotHandler, error) {
	token = strings.TrimSpace(token)
	if store == nil || len(token) < 32 {
		return nil, errors.New("invalid catalog snapshot handler")
	}
	return &SnapshotHandler{store: store, tokenHash: sha256.Sum256([]byte(token))}, nil
}

func (handler *SnapshotHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		writeCatalogJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	prefix := "Bearer "
	header := request.Header.Get("Authorization")
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	if !strings.HasPrefix(header, prefix) || subtle.ConstantTimeCompare(presented[:], handler.tokenHash[:]) != 1 {
		writeCatalogJSON(response, http.StatusUnauthorized, map[string]string{"code": "authentication_required"})
		return
	}
	snapshot, err := handler.store.Snapshot()
	if err != nil {
		writeCatalogJSON(response, http.StatusInternalServerError, map[string]string{"code": "catalog_read_failed"})
		return
	}
	writeCatalogJSON(response, http.StatusOK, snapshot)
}

func FetchSnapshot(ctx context.Context, url, token string, client *http.Client) (Snapshot, error) {
	var snapshot Snapshot
	if ctx == nil || strings.TrimSpace(url) == "" || len(strings.TrimSpace(token)) < 32 {
		return snapshot, errors.New("invalid catalog snapshot request")
	}
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return snapshot, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return snapshot, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumSnapshotIPCBytes))
		return snapshot, errors.New("catalog snapshot request failed")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumSnapshotIPCBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumSnapshotIPCBytes {
		return Snapshot{}, errors.New("catalog snapshot response is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(&struct{}{}) != io.EOF || snapshot.SchemaVersion != SchemaVersion {
		return Snapshot{}, errors.New("catalog snapshot response is invalid")
	}
	previousID := ""
	for index := range snapshot.Lines {
		line := cloneLine(snapshot.Lines[index])
		if line.normalizeAndValidate() != nil || (previousID != "" && line.ID <= previousID) {
			return Snapshot{}, errors.New("catalog snapshot response is invalid")
		}
		snapshot.Lines[index], previousID = line, line.ID
	}
	return snapshot, nil
}
