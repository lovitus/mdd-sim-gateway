package updatenetwork

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const PolicyPath = "/v1/snapshots/update-policy"

type PolicyRevision struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      uint64 `json:"revision"`
}

func CheckPolicy(ctx context.Context, endpoint, token string, expected uint64, client *http.Client) error {
	if expected == 0 {
		return nil
	}
	if client == nil || len(token) < 32 {
		return errors.New("update policy client is invalid")
	}
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("update policy request invalid")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := bounded.Do(request)
	if err != nil {
		return errors.New("update policy unavailable")
	}
	defer response.Body.Close()
	var current PolicyRevision
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4097))
	decoder.DisallowUnknownFields()
	if response.StatusCode != http.StatusOK || decoder.Decode(&current) != nil || decoder.Decode(&struct{}{}) != io.EOF || current.SchemaVersion != 1 || current.Revision == 0 {
		return errors.New("update policy response invalid")
	}
	if current.Revision != expected {
		return errors.New("update networking settings changed")
	}
	return nil
}
