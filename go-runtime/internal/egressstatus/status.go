// Package egressstatus reads the host-owned country-exit runtime contract.
package egressstatus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maximumStatusBytes = 1 << 20

type Exit struct {
	Ready         bool   `json:"ready"`
	HostProxyHost string `json:"host_proxy_host"`
	ProxyPort     int    `json:"proxy_port"`
}

type Snapshot struct {
	Exits map[string]Exit `json:"exits"`
}

func Load(path string) (Snapshot, error) {
	var snapshot Snapshot
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return snapshot, errors.New("egress status path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return snapshot, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumStatusBytes {
		return snapshot, errors.New("egress status must be a non-empty regular file no larger than 1 MiB")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return snapshot, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode egress status: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("egress status must contain one JSON document")
	}
	if len(snapshot.Exits) == 0 {
		return Snapshot{}, errors.New("egress status has no exits")
	}
	normalized := make(map[string]Exit, len(snapshot.Exits))
	for rawCountry, exit := range snapshot.Exits {
		country, ok := normalizeCountry(rawCountry)
		if !ok {
			return Snapshot{}, errors.New("egress status contains an invalid country")
		}
		if _, duplicate := normalized[country]; duplicate {
			return Snapshot{}, errors.New("egress status contains duplicate countries")
		}
		normalized[country] = exit
	}
	snapshot.Exits = normalized
	return snapshot, nil
}

func (snapshot Snapshot) ProxyURL(country string) (string, error) {
	country, ok := normalizeCountry(country)
	if !ok {
		return "", errors.New("line egress country is invalid")
	}
	exit, exists := snapshot.Exits[country]
	if !exists {
		return "", fmt.Errorf("country exit %q is unavailable", country)
	}
	if !exit.Ready {
		return "", fmt.Errorf("country exit %q is not ready", country)
	}
	address, err := netip.ParseAddr(strings.TrimSpace(exit.HostProxyHost))
	if err != nil || !address.IsLoopback() {
		return "", fmt.Errorf("country exit %q has no host loopback proxy", country)
	}
	if exit.ProxyPort < 1 || exit.ProxyPort > 65535 {
		return "", fmt.Errorf("country exit %q has an invalid proxy port", country)
	}
	return "socks5://" + net.JoinHostPort(address.String(), strconv.Itoa(exit.ProxyPort)), nil
}

func normalizeCountry(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 2 || value[0] < 'a' || value[0] > 'z' || value[1] < 'a' || value[1] > 'z' {
		return "", false
	}
	return value, true
}
