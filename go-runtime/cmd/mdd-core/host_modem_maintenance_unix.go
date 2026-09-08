//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/core"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
)

type hostIdleSnapshot struct {
	Ready      bool      `json:"ready"`
	AgentID    string    `json:"agent_id"`
	Generation string    `json:"process_generation"`
	Revision   uint64    `json:"catalog_revision"`
	LineIDs    []string  `json:"line_ids"`
	ObservedAt time.Time `json:"observed_at"`
}

func (service *providerApplyService) hostModemIPC(ctx context.Context, path string, body any, result any) error {
	base, err := providerCoreAddress(service.settings.Local.Listen)
	if err != nil {
		return err
	}
	method := http.MethodGet
	var payload []byte
	if body != nil {
		method = http.MethodPost
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("X-MDD-Provider-Apply-Token", service.settings.Local.Token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	wire, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(wire) > 64<<10 {
		return errors.New("host maintenance response too large")
	}
	if response.StatusCode != 200 {
		var failure struct {
			Code string `json:"code"`
		}
		json.Unmarshal(wire, &failure)
		if failure.Code == "" {
			failure.Code = "host_maintenance_unavailable"
		}
		return &provideradmin.Error{Status: response.StatusCode, Code: failure.Code}
	}
	return json.Unmarshal(wire, result)
}

func (service *providerApplyService) hostIdle(ctx context.Context, agentID string) (hostIdleSnapshot, error) {
	var snapshot hostIdleSnapshot
	if err := service.hostModemIPC(ctx, core.HostModemIdlePath, nil, &snapshot); err != nil {
		return snapshot, err
	}
	if !snapshot.Ready || snapshot.AgentID != agentID || snapshot.Generation == "" || snapshot.Revision == 0 || snapshot.ObservedAt.IsZero() {
		return snapshot, errors.New("host idle identity unconfirmed")
	}
	return snapshot, nil
}

func (service *providerApplyService) hostAgentLease(ctx context.Context, agentID, generation, leaseID string, begin bool) error {
	action := "end"
	if begin {
		action = "begin"
	}
	var result struct {
		AgentID string `json:"agent_id"`
		LeaseID string `json:"lease_id"`
		Held    bool   `json:"held"`
	}
	if err := service.hostModemIPC(ctx, core.HostModemMaintenancePath, map[string]string{"action": action, "lease_id": leaseID, "process_generation": generation}, &result); err != nil {
		return err
	}
	if result.AgentID != agentID || result.LeaseID != leaseID || result.Held != begin {
		return errors.New("host maintenance lease response mismatch")
	}
	return nil
}

type hostModeLease struct {
	service                       *providerApplyService
	agentID, generation, id, base string
	providers                     providerapply.DrainRequest
	agentHeld, providersHeld      bool
}

func (lease *hostModeLease) release(ctx context.Context) error {
	if lease.agentHeld {
		if err := lease.service.hostAgentLease(ctx, lease.agentID, lease.generation, lease.id, false); err != nil {
			return err
		}
		lease.agentHeld = false
	}
	if lease.providersHeld {
		result, err := providerapply.RequestMaintenance(ctx, lease.base, lease.service.settings.Local.Token, lease.providers, false, nil)
		if err != nil {
			return err
		}
		if !result.Ready {
			return errors.New("Provider resume not confirmed")
		}
		lease.providersHeld = false
	}
	return nil
}

func (service *providerApplyService) acquireHostModeLease(ctx context.Context, agentID string) (*hostModeLease, error) {
	first, err := service.hostIdle(ctx, agentID)
	if err != nil {
		return nil, err
	}
	base, err := providerCoreAddress(service.settings.Local.Listen)
	if err != nil {
		return nil, err
	}
	preflight, err := providerapply.Fetch(ctx, base+providerapply.Path, service.settings.Local.Token, nil)
	if err != nil {
		return nil, err
	}
	if preflight.CatalogRevision != first.Revision {
		return nil, errors.New("host catalog revision changed")
	}
	selected := map[string]bool{}
	for _, id := range first.LineIDs {
		selected[id] = true
	}
	ids := []string{}
	for _, line := range preflight.Lines {
		if selected[line.LineID] && line.ProviderPresent {
			ids = append(ids, line.LineID)
		}
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	id := "host-mode-" + hex.EncodeToString(random)
	lease := &hostModeLease{service: service, agentID: agentID, generation: first.Generation, id: id, base: base, providers: providerapply.DrainRequest{SchemaVersion: 1, CatalogRevision: first.Revision, LeaseID: id, LineIDs: ids}}
	cleanup := func(cause error) (*hostModeLease, error) {
		clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if releaseErr := lease.release(clean); releaseErr != nil {
			return lease, errors.Join(cause, releaseErr)
		}
		return nil, cause
	}
	if len(ids) > 0 {
		lease.providersHeld = true
		result, err := providerapply.RequestMaintenance(ctx, base, service.settings.Local.Token, lease.providers, true, nil)
		if err != nil {
			return cleanup(err)
		}
		if !result.Ready {
			return cleanup(errors.New("Provider drain not confirmed"))
		}
	}
	lease.agentHeld = true
	if err := service.hostAgentLease(ctx, agentID, first.Generation, id, true); err != nil {
		var rejected *provideradmin.Error
		if errors.As(err, &rejected) && rejected.Status == 409 {
			lease.agentHeld = false
		}
		return cleanup(err)
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return cleanup(ctx.Err())
	case <-timer.C:
	}
	current, err := service.hostIdle(ctx, agentID)
	if err != nil {
		return cleanup(err)
	}
	if current.Generation != first.Generation || current.Revision != first.Revision || !current.ObservedAt.After(first.ObservedAt) {
		return cleanup(errors.New("fresh maintenance observation unavailable"))
	}
	return lease, nil
}
