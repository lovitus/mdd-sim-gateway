package core

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

// UpdateMaintenanceRuntime adapts existing business owners and exact Provider
// routing. It never interprets a missing Provider as a successfully released one.
type UpdateMaintenanceRuntime struct {
	Catalog    *linecatalog.Store
	Providers  *mediaauth.ProviderDirectory
	Agents     *agentlink.Server
	CheckMedia func() error
	Checks     []func(string) (bool, error)
}

func (runtime UpdateMaintenanceRuntime) Snapshot() (uint64, []mediaauth.ProviderFence, error) {
	snapshot, err := runtime.Catalog.Snapshot()
	if err != nil {
		return 0, nil, err
	}
	fences := []mediaauth.ProviderFence{}
	for _, line := range snapshot.Lines {
		if provider, found := runtime.Providers.CurrentProvider(line.ID); found {
			fences = append(fences, provider.Fence())
		}
	}
	return snapshot.Revision, fences, nil
}

func (runtime UpdateMaintenanceRuntime) Busy() error {
	if runtime.CheckMedia == nil || len(runtime.Checks) == 0 {
		return errors.New("update busy checks unavailable")
	}
	if err := runtime.CheckMedia(); err != nil {
		return err
	}
	snapshot, err := runtime.Catalog.Snapshot()
	if err != nil {
		return err
	}
	for _, line := range snapshot.Lines {
		for _, check := range runtime.Checks {
			active, err := check(line.ID)
			if err != nil {
				return err
			}
			if active {
				return errors.New("update has active line business")
			}
		}
	}
	return nil
}

func (runtime UpdateMaintenanceRuntime) FreezeAgents(lease string) error {
	return runtime.Agents.BeginUpdateMaintenance(lease)
}
func (runtime UpdateMaintenanceRuntime) ReleaseAgents(lease string) error {
	return runtime.Agents.EndUpdateMaintenance(lease)
}

func (runtime UpdateMaintenanceRuntime) Provider(ctx context.Context, expected mediaauth.ProviderFence, lease, action string) error {
	return runtime.Providers.UseExpected(ctx, expected, func(provider mediaauth.Provider) error {
		address, err := url.Parse(provider.BaseURL)
		if err != nil {
			return err
		}
		address.Scheme = "http"
		httpClient := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
		defer httpClient.CloseIdleConnections()
		client, err := vowifiipc.NewClient(address.String(), provider.Token, httpClient)
		if err != nil {
			return err
		}
		match := func(status vowifiipc.Snapshot) bool {
			return status.LineID == expected.LineID && status.ProviderID == expected.ProviderID && status.ProcessGeneration == expected.Generation
		}
		if action == "begin" {
			result, err := client.BeginDrain(ctx, vowifiipc.MaintenanceRequest{LeaseID: lease})
			if err != nil {
				return err
			}
			if !match(result.Status) || !result.Draining || result.LeaseID != lease {
				return errors.New("update drain identity mismatch")
			}
			return nil
		}
		status, err := client.Status(ctx)
		if err != nil {
			return err
		}
		if !match(status) {
			return errors.New("update provider identity mismatch")
		}
		if action == "verify" {
			if !status.Maintenance.Draining || status.Maintenance.LeaseID != lease {
				return errors.New("update drain ownership unconfirmed")
			}
			return nil
		}
		if action != "release" {
			return errors.New("invalid update provider action")
		}
		if !status.Maintenance.Draining && status.Maintenance.LeaseID == "" {
			return nil
		}
		if status.Maintenance.LeaseID != lease {
			return errors.New("update cannot release another maintenance lease")
		}
		result, err := client.EndDrain(ctx, vowifiipc.MaintenanceRequest{LeaseID: lease})
		if err != nil {
			return err
		}
		if !match(result.Status) || result.Draining || result.Status.Maintenance.Draining {
			return errors.New("update provider resume unconfirmed")
		}
		return nil
	})
}
