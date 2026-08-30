// Package egressexec renders and runs the host-local country exit proxies.
// It deliberately owns no host routes, TUN interfaces, DNS settings, modems, or
// Provider lifecycle. VoWiFi Providers reach each exit only through a literal
// loopback SOCKS5 listener.
package egressexec

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressdesired"
)

const proxyPortBase = 22000

type ExitStatus struct {
	Ready          bool     `json:"ready"`
	Mode           string   `json:"mode,omitempty"`
	Node           string   `json:"node,omitempty"`
	CandidateCount int      `json:"candidate_count,omitempty"`
	Candidates     []string `json:"candidates,omitempty"`
	Error          string   `json:"error,omitempty"`
	HostProxyHost  string   `json:"host_proxy_host,omitempty"`
	ProxyPort      int      `json:"proxy_port,omitempty"`
}

type Status struct {
	SchemaVersion       int                   `json:"schema_version"`
	DesiredGeneration   string                `json:"desired_generation,omitempty"`
	RequestedGeneration string                `json:"requested_generation,omitempty"`
	Ready               bool                  `json:"ready"`
	Error               string                `json:"error,omitempty"`
	Exits               map[string]ExitStatus `json:"exits"`
}

type Rendered struct {
	Config []byte
	Status Status
	Ports  []int
}

func Render(document egressdesired.Document) (Rendered, error) {
	return RenderAtBase(document, proxyPortBase)
}

func RenderAtBase(document egressdesired.Document, portBase int) (Rendered, error) {
	result := Rendered{Status: Status{
		SchemaVersion: 1, RequestedGeneration: document.Generation,
		Exits: map[string]ExitStatus{},
	}}
	if (document.Version != 1 && document.Version != 2) || len(document.Generation) != 64 ||
		portBase < 1024 || portBase+675 > 65535 {
		return result, errors.New("country exit desired document is invalid")
	}
	if !document.Proxy.Enabled {
		result.Status.Ready = true
		result.Status.DesiredGeneration = document.Generation
		payload, err := json.MarshalIndent(baseConfig(nil, nil, nil), "", "  ")
		result.Config = append(payload, '\n')
		return result, err
	}

	var inbounds, outbounds, rules []map[string]any
	allReady := true
	for _, country := range sortedCountries(document.Proxy.Exits) {
		exit := document.Proxy.Exits[country]
		if !exit.Enabled {
			continue
		}
		status := ExitStatus{HostProxyHost: "127.0.0.1", ProxyPort: countryProxyPort(country, portBase)}
		profile, mode, err := effectiveProfile(document.Proxy, exit)
		status.Mode = mode
		if err == nil {
			var built []map[string]any
			built, status.Node, err = renderProfile(profile, mode, "exit-"+country)
			if err == nil {
				outbounds = append(outbounds, built...)
				inboundTag := "proxy-" + country
				inbounds = append(inbounds, map[string]any{
					"type": "socks", "tag": inboundTag, "listen": "127.0.0.1",
					"listen_port": status.ProxyPort, "udp_timeout": "2m",
				})
				rules = append(rules, map[string]any{
					"inbound": []string{inboundTag}, "action": "route", "outbound": "exit-" + country,
				})
				result.Ports = append(result.Ports, status.ProxyPort)
				status.Ready = true
			}
		}
		if err != nil {
			allReady = false
			status.Error = err.Error()
			status.HostProxyHost, status.ProxyPort = "", 0
		}
		result.Status.Exits[country] = status
	}
	if len(result.Status.Exits) == 0 {
		allReady = false
		result.Status.Error = "no enabled country exit"
	}
	result.Status.Ready = allReady
	if !allReady {
		return result, errors.New("one or more country exits could not be rendered")
	}
	config := baseConfig(inbounds, outbounds, rules)
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return result, err
	}
	result.Config = append(payload, '\n')
	return result, nil
}

func baseConfig(inbounds, outbounds, rules []map[string]any) map[string]any {
	if inbounds == nil {
		inbounds = []map[string]any{}
	}
	if outbounds == nil {
		outbounds = []map[string]any{}
	}
	if rules == nil {
		rules = []map[string]any{}
	}
	return map[string]any{
		"log":      map[string]any{"level": "info"},
		"dns":      map[string]any{"servers": []map[string]any{{"type": "local", "tag": "dns-bootstrap"}}},
		"inbounds": inbounds, "outbounds": outbounds,
		"route": map[string]any{"rules": rules, "default_domain_resolver": "dns-bootstrap"},
	}
}

func effectiveProfile(config egressconfig.Config, exit egressconfig.Exit) (egressconfig.Profile, string, error) {
	if exit.Mode == "direct" {
		return egressconfig.Profile{Name: "Direct", Type: "direct"}, "direct", nil
	}
	profile, ok := config.Profiles[exit.ProfileID]
	if !ok {
		return profile, "", errors.New("country exit references an unknown profile")
	}
	switch profile.Type {
	case "node", "socks5":
		return profile, "manual", nil
	case "subscription":
		return profile, "subscription", errors.New("subscription profile execution is not implemented")
	case "existing":
		return profile, "existing", errors.New("existing sing-box outbound execution is not implemented")
	case "cellular_sim":
		return profile, "cellular_sim", errors.New("cellular SIM exit has no Go data-session route")
	default:
		return profile, profile.Type, errors.New("country exit profile type is unsupported")
	}
}

func renderProfile(profile egressconfig.Profile, mode, finalTag string) ([]map[string]any, string, error) {
	if profile.Type == "direct" {
		return []map[string]any{{"type": "direct", "tag": finalTag}}, profile.Name, nil
	}
	if profile.Type == "socks5" {
		outbound := map[string]any{
			"type": "socks", "tag": finalTag, "version": "5",
			"server": profile.Server, "server_port": profile.Port,
		}
		if profile.Username != "" {
			outbound["username"] = profile.Username
		}
		if profile.Password != "" {
			outbound["password"] = profile.Password
		}
		return []map[string]any{outbound}, profile.Name, nil
	}
	if profile.Type != "node" {
		return nil, "", fmt.Errorf("%s profile execution is unavailable", mode)
	}
	hops := splitNonemptyLines(profile.Value)
	if len(hops) == 0 || len(hops) > 4 {
		return nil, "", errors.New("node profile must contain between one and four hops")
	}
	result := make([]map[string]any, 0, len(hops))
	previous := ""
	for index, raw := range hops {
		tag := finalTag
		if index != len(hops)-1 {
			tag = finalTag + "-hop-" + strconv.Itoa(index+1)
		}
		outbound, err := parseNode(raw, tag)
		if err != nil {
			return nil, "", fmt.Errorf("node hop %d: %w", index+1, err)
		}
		if previous != "" {
			outbound["detour"] = previous
		}
		result = append(result, outbound)
		previous = tag
	}
	return result, profile.Name, nil
}

func parseNode(raw, tag string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") {
		var outbound map[string]any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var trailing any
		if err := decoder.Decode(&outbound); err != nil || len(outbound) == 0 || decoder.Decode(&trailing) != io.EOF {
			return nil, errors.New("raw sing-box outbound is invalid")
		}
		kind, ok := outbound["type"].(string)
		if !ok || strings.TrimSpace(kind) == "" {
			return nil, errors.New("raw sing-box outbound has no type")
		}
		outbound["tag"] = tag
		return outbound, nil
	}
	scheme := strings.ToLower(strings.SplitN(raw, "://", 2)[0])
	switch scheme {
	case "ss":
		return parseShadowsocks(raw, tag)
	case "socks", "socks5":
		return parseSOCKS(raw, tag)
	default:
		return nil, fmt.Errorf("unsupported node scheme %q", scheme)
	}
}

func parseSOCKS(raw, tag string) (map[string]any, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("SOCKS5 node is invalid")
	}
	port := 1080
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
	}
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("SOCKS5 node port is invalid")
	}
	outbound := map[string]any{
		"type": "socks", "tag": tag, "version": "5",
		"server": parsed.Hostname(), "server_port": port,
	}
	if parsed.User != nil {
		outbound["username"] = parsed.User.Username()
		if password, ok := parsed.User.Password(); ok {
			outbound["password"] = password
		}
	}
	return outbound, nil
}

func parseShadowsocks(raw, tag string) (map[string]any, error) {
	body := strings.TrimPrefix(raw, "ss://")
	if index := strings.IndexAny(body, "?#"); index >= 0 {
		body = body[:index]
	}
	var credentials, address string
	if index := strings.LastIndex(body, "@"); index >= 0 {
		credentials, address = body[:index], body[index+1:]
		if !strings.Contains(credentials, ":") {
			decoded, err := decodeBase64(credentials)
			if err != nil {
				return nil, errors.New("Shadowsocks credentials are invalid")
			}
			credentials = decoded
		}
	} else {
		decoded, err := decodeBase64(body)
		if err != nil {
			return nil, errors.New("Shadowsocks node is invalid")
		}
		var found bool
		credentials, address, found = strings.Cut(decoded, "@")
		if !found {
			return nil, errors.New("Shadowsocks node has no server address")
		}
	}
	method, password, ok := strings.Cut(credentials, ":")
	host, portText, addressErr := net.SplitHostPort(address)
	port, portErr := strconv.Atoi(portText)
	if !ok || method == "" || password == "" || addressErr != nil || host == "" || portErr != nil || port < 1 || port > 65535 {
		return nil, errors.New("Shadowsocks node fields are invalid")
	}
	return map[string]any{
		"type": "shadowsocks", "tag": tag, "server": host, "server_port": port,
		"method": method, "password": password, "udp_fragment": true,
	}, nil
}

func decodeBase64(value string) (string, error) {
	value = strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if payload, err := encoding.DecodeString(value); err == nil {
			return string(payload), nil
		}
	}
	return "", errors.New("invalid base64")
}

func countryProxyPort(country string, base int) int {
	return base + int(country[0]-'a')*26 + int(country[1]-'a')
}

func splitNonemptyLines(value string) []string {
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func sortedCountries(exits map[string]egressconfig.Exit) []string {
	result := make([]string, 0, len(exits))
	for country := range exits {
		result = append(result, country)
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j] < result[j-1]; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}
