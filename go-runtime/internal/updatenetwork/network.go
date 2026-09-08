// Package updatenetwork adapts ec620942 control/app/update_check.py's network
// selection to existing Go proxy-library and confirmed country-exit facts.
package updatenetwork

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressstatus"
)

type Selection struct {
	Mode      string `json:"proxy_mode"`
	ProfileID string `json:"proxy_profile_id,omitempty"`
}

func (selection Selection) Validate() error {
	if selection.Mode != "auto" && selection.Mode != "direct" && selection.Mode != "library" {
		return errors.New("invalid update network mode")
	}
	if selection.Mode != "library" {
		if selection.ProfileID != "" {
			return errors.New("unexpected update proxy selection")
		}
		return nil
	}
	if len(selection.ProfileID) < 1 || len(selection.ProfileID) > 80 {
		return errors.New("update proxy selection is required")
	}
	for _, c := range selection.ProfileID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.-", c)) {
			return errors.New("invalid update proxy selection")
		}
	}
	return nil
}

// Only non-secret identity is serializable. A persisted route must be resolved
// again against its expected configuration before another process can use it.
type Route struct {
	PolicyRevision uint64 `json:"policy_revision,omitempty"`
	Mode           string `json:"proxy_mode"`
	ProfileID      string `json:"proxy_profile_id,omitempty"`
	ConfigRevision uint64 `json:"config_revision,omitempty"`
	Country        string `json:"country,omitempty"`
	Generation     string `json:"generation,omitempty"`
	proxy          *url.URL
}

func (route Route) ValidateIdentity() error {
	if err := (Selection{Mode: route.Mode, ProfileID: route.ProfileID}).Validate(); err != nil {
		return err
	}
	if route.Mode == "auto" {
		return errors.New("update route is not resolved")
	}
	if route.Mode == "direct" {
		if route.ConfigRevision != 0 || route.Country != "" || route.Generation != "" {
			return errors.New("invalid direct update route")
		}
		return nil
	}
	if route.ConfigRevision == 0 {
		return errors.New("update proxy revision is missing")
	}
	if route.Country == "" && route.Generation == "" {
		return nil
	}
	if len(route.Country) != 2 || strings.Trim(route.Country, "abcdefghijklmnopqrstuvwxyz") != "" || len(route.Generation) != 64 || strings.Trim(route.Generation, "0123456789abcdef") != "" {
		return errors.New("invalid update exit identity")
	}
	return nil
}

func ResolveRecorded(recorded Route, config egressconfig.Snapshot, actual egressstatus.Snapshot) (Route, error) {
	if err := recorded.ValidateIdentity(); err != nil {
		return Route{}, err
	}
	if recorded.Mode == "direct" {
		return Route{Mode: "direct", PolicyRevision: recorded.PolicyRevision}, nil
	}
	if config.Revision != recorded.ConfigRevision {
		return Route{}, errors.New("update proxy configuration changed")
	}
	if recorded.Country != "" {
		exit, ok := config.Config.Exits[recorded.Country]
		profile := config.Config.Profiles[recorded.ProfileID]
		if !config.Config.Enabled || !ok || !exit.Enabled || exit.Mode == "direct" || exit.ProfileID != recorded.ProfileID ||
			(profile.Type != "node" && profile.Type != "subscription" && profile.Type != "existing") || actual.DesiredGeneration != recorded.Generation {
			return Route{}, errors.New("verified update country exit changed")
		}
		value, err := actual.ProxyURL(recorded.Country)
		if err != nil {
			return Route{}, errors.New("verified update country exit is unavailable")
		}
		proxy, err := url.Parse(value)
		if err != nil {
			return Route{}, errors.New("verified update proxy is invalid")
		}
		recorded.proxy = proxy
		return recorded, nil
	}
	resolved, err := resolve(recorded.ProfileID, config, actual, recorded.Generation)
	if err != nil {
		return Route{}, err
	}
	if resolved.Country != recorded.Country || resolved.Generation != recorded.Generation {
		return Route{}, errors.New("verified update route changed")
	}
	resolved.PolicyRevision = recorded.PolicyRevision
	return resolved, nil
}

func Candidates(selection Selection, config egressconfig.Snapshot, actual egressstatus.Snapshot, expectedGeneration string) ([]Route, error) {
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	if selection.Mode == "direct" {
		return []Route{{Mode: "direct"}}, nil
	}
	if selection.Mode == "library" {
		route, err := resolve(selection.ProfileID, config, actual, expectedGeneration)
		if err != nil {
			return nil, err
		}
		return []Route{route}, nil
	}
	result := []Route{{Mode: "direct"}}
	ids := make([]string, 0, len(config.Config.Profiles))
	for id := range config.Config.Profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if route, err := resolve(id, config, actual, expectedGeneration); err == nil {
			result = append(result, route)
		}
	}
	return result, nil
}

func resolve(id string, config egressconfig.Snapshot, actual egressstatus.Snapshot, expectedGeneration string) (Route, error) {
	route := Route{Mode: "library", ProfileID: id, ConfigRevision: config.Revision}
	profile, ok := config.Config.Profiles[id]
	if !ok || config.Revision == 0 {
		return Route{}, errors.New("selected update proxy is unavailable")
	}
	switch profile.Type {
	case "socks5":
		if profile.Server == "" || strings.ContainsAny(profile.Server, " /\t\r\n@?#") || profile.Port < 1 || profile.Port > 65535 {
			return Route{}, errors.New("selected update SOCKS5 proxy is invalid")
		}
		route.proxy = &url.URL{Scheme: "socks5h", Host: net.JoinHostPort(profile.Server, strconv.Itoa(profile.Port))}
		if profile.Username != "" || profile.Password != "" {
			route.proxy.User = url.UserPassword(profile.Username, profile.Password)
		}
		return route, nil
	case "node", "subscription", "existing":
		if !config.Config.Enabled || expectedGeneration == "" || actual.DesiredGeneration != expectedGeneration {
			return Route{}, errors.New("update exit generation is unconfirmed")
		}
		countries := make([]string, 0, len(config.Config.Exits))
		for country, exit := range config.Config.Exits {
			if exit.Enabled && exit.Mode != "direct" && exit.ProfileID == id {
				countries = append(countries, country)
			}
		}
		sort.Strings(countries)
		for _, country := range countries {
			value, err := actual.ProxyURL(country)
			if err != nil {
				continue
			}
			route.proxy, err = url.Parse(value)
			if err != nil {
				continue
			}
			route.Country, route.Generation = country, expectedGeneration
			return route, nil
		}
		return Route{}, errors.New("selected update proxy has no confirmed ready exit")
	case "cellular_sim":
		return Route{}, errors.New("metered update route requires an explicit cost policy")
	default:
		return Route{}, errors.New("unsupported update proxy type")
	}
}

func (route Route) Client(timeout time.Duration) (*http.Client, error) {
	if timeout < 0 || route.Mode != "direct" && route.Mode != "library" || route.Mode == "library" && route.proxy == nil {
		return nil, errors.New("update route has not been resolved")
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport is unavailable")
	}
	transport := base.Clone()
	transport.Proxy = nil // Match the original session.trust_env = False.
	if route.proxy != nil {
		copy := *route.proxy
		transport.Proxy = http.ProxyURL(&copy)
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}
