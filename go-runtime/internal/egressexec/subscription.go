package egressexec

// Source: host/mdd_orchestrator.py:687-820,2000-2064 at ec620942.
// Keep the old country-token, UDP admission, TLS/REALITY and stable-name rules.
// Fetching feeds and activating a selector remain separate from conversion.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

type subscriptionNode struct {
	Name           string            `yaml:"name"`
	Type           string            `yaml:"type"`
	Server         string            `yaml:"server"`
	Port           int               `yaml:"port"`
	UDP            *bool             `yaml:"udp"`
	Plugin         string            `yaml:"plugin"`
	Network        string            `yaml:"network"`
	Password       string            `yaml:"password"`
	Auth           string            `yaml:"auth"`
	UUID           string            `yaml:"uuid"`
	Flow           string            `yaml:"flow"`
	Cipher         string            `yaml:"cipher"`
	AlterID        int               `yaml:"alterId"`
	TLS            bool              `yaml:"tls"`
	ServerName     string            `yaml:"servername"`
	SNI            string            `yaml:"sni"`
	SkipCertVerify bool              `yaml:"skip-cert-verify"`
	ALPN           []string          `yaml:"alpn"`
	Fingerprint    string            `yaml:"client-fingerprint"`
	Reality        map[string]string `yaml:"reality-opts"`
	Obfs           string            `yaml:"obfs"`
	ObfsPassword   string            `yaml:"obfs-password"`
	WS             struct {
		Path    string            `yaml:"path"`
		Headers map[string]string `yaml:"headers"`
	} `yaml:"ws-opts"`
}

func nodeKeywordMatches(name, keyword string) bool {
	name, keyword = strings.ToLower(name), strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return false
	}
	asciiToken := len(keyword) >= 2 && len(keyword) <= 3
	for _, r := range keyword {
		asciiToken = asciiToken && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}
	if !asciiToken {
		return strings.Contains(name, keyword)
	}
	for offset := 0; offset < len(name); {
		index := strings.Index(name[offset:], keyword)
		if index < 0 {
			return false
		}
		index += offset
		alphaNum := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' }
		end := index + len(keyword)
		if (index == 0 || !alphaNum(name[index-1])) && (end == len(name) || !alphaNum(name[end])) {
			return true
		}
		offset = index + 1
	}
	return false
}

func (node subscriptionNode) supportsUDP() bool {
	if node.UDP != nil && !*node.UDP {
		return false
	}
	switch strings.ToLower(node.Type) {
	case "ss", "shadowsocks":
		if node.Plugin != "" {
			return false
		}
	case "trojan", "vless", "vmess", "hysteria2", "hy2":
	default:
		return false
	}
	// xhttp needs the original separately managed Xray boundary. Never silently
	// render it as plain TCP, which would discard transport/authentication fields.
	return node.Network == "" || node.Network == "tcp" || node.Network == "ws"
}

func (node subscriptionNode) outbound(tag string) (map[string]any, error) {
	if !node.supportsUDP() || node.Server == "" || node.Port < 1 || node.Port > 65535 || strings.IndexFunc(node.Server, unicode.IsControl) >= 0 {
		return nil, errors.New("subscription node has invalid endpoint or unsupported UDP transport")
	}
	kind := strings.ToLower(node.Type)
	out := map[string]any{"type": kind, "tag": tag, "server": node.Server, "server_port": node.Port}
	switch kind {
	case "trojan":
		out["password"] = node.Password
	case "vless":
		out["uuid"] = node.UUID
		if node.Flow != "" {
			out["flow"] = node.Flow
		}
	case "vmess":
		out["uuid"] = node.UUID
		out["security"] = node.Cipher
		if node.Cipher == "" {
			out["security"] = "auto"
		}
		out["alter_id"] = node.AlterID
	case "ss", "shadowsocks":
		out["type"] = "shadowsocks"
		out["method"] = node.Cipher
		out["password"] = node.Password
		out["udp_fragment"] = true
	case "hy2", "hysteria2":
		out["type"] = "hysteria2"
		password := node.Password
		if password == "" {
			password = node.Auth
		}
		out["password"] = password
	}
	if node.TLS || kind == "trojan" || kind == "hysteria2" || kind == "hy2" {
		serverName := node.ServerName
		if serverName == "" {
			serverName = node.SNI
		}
		if serverName == "" {
			serverName = node.Server
		}
		tls := map[string]any{"enabled": true, "server_name": serverName, "insecure": node.SkipCertVerify}
		if len(node.ALPN) > 0 {
			tls["alpn"] = node.ALPN
		}
		fingerprint := node.Fingerprint
		if len(node.Reality) > 0 {
			if node.Reality["public-key"] == "" {
				return nil, errors.New("subscription REALITY public key is missing")
			}
			tls["reality"] = map[string]any{"enabled": true, "public_key": node.Reality["public-key"], "short_id": node.Reality["short-id"]}
			tls["insecure"] = false
			if fingerprint == "" {
				fingerprint = "chrome"
			}
		}
		if fingerprint != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fingerprint}
		}
		out["tls"] = tls
	}
	if (kind == "hysteria2" || kind == "hy2") && node.Obfs != "" {
		out["obfs"] = map[string]any{"type": node.Obfs, "password": node.ObfsPassword}
	}
	if node.Network == "ws" {
		path := node.WS.Path
		if path == "" {
			path = "/"
		}
		headers := node.WS.Headers
		if headers == nil {
			headers = map[string]string{}
		}
		out["transport"] = map[string]any{"type": "ws", "path": path, "headers": headers}
	}
	return out, nil
}

func parseSubscription(payload []byte) ([]subscriptionNode, error) {
	if len(payload) == 0 || len(payload) > 8<<20 {
		return nil, errors.New("subscription body exceeds size limit or is empty")
	}
	var document struct {
		Proxies []subscriptionNode `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(payload, &document); err != nil {
		return nil, errors.New("subscription is not a valid Clash document")
	}
	if len(document.Proxies) == 0 {
		return nil, errors.New("subscription contains no proxies")
	}
	return document.Proxies, nil
}

func subscriptionPool(nodes []subscriptionNode, keywords []string, tag, pinned, pinMode, running string) ([]map[string]any, []string, string, error) {
	var matches []subscriptionNode
	for _, node := range nodes {
		match := len(keywords) == 0
		for _, keyword := range keywords {
			match = match || nodeKeywordMatches(node.Name, keyword)
		}
		if match && node.supportsUDP() {
			matches = append(matches, node)
		}
	}
	if len(matches) == 0 {
		return nil, nil, "", errors.New("no UDP-capable subscription node matched country keywords")
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
	if len(matches) > 32 {
		matches = matches[:32]
	}
	var outbounds []map[string]any
	var tags, names []string
	seen := map[string]bool{}
	for i, node := range matches {
		if node.Name == "" || seen[node.Name] {
			return nil, nil, "", errors.New("subscription node names must be nonempty and unique")
		}
		seen[node.Name] = true
		member := fmt.Sprintf("%s-%d", tag, i)
		out, err := node.outbound(member)
		if err != nil {
			return nil, nil, "", err
		}
		outbounds = append(outbounds, out)
		tags = append(tags, member)
		names = append(names, node.Name)
	}
	selected := 0
	find := func(name string) int {
		for i, value := range names {
			if value == name {
				return i
			}
		}
		return -1
	}
	if i := find(pinned); pinned != "" && (pinMode == "lock" || pinMode == "") {
		if i < 0 {
			return nil, nil, "", errors.New("locked subscription node is unavailable")
		}
		selected = i
	} else if i := find(running); i >= 0 {
		selected = i
	} else if i := find(pinned); i >= 0 {
		selected = i
	}
	outbounds = append(outbounds, map[string]any{"type": "selector", "tag": tag, "outbounds": tags, "default": tags[selected], "interrupt_exist_connections": false})
	return outbounds, names, names[selected], nil
}
