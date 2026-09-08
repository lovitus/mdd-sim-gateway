package egressexec

// Copied and adapted from host/mdd_orchestrator.py:xray_xhttp_outbound and
// xhttp_bridge_outbound at ec620942. XHTTP remains native Xray, never plain TCP.
// This renderer is not activation authority; the executor must own both children.

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

type xhttpOptions struct {
	Host  string         `yaml:"host"`
	Path  string         `yaml:"path"`
	Mode  string         `yaml:"mode"`
	Extra map[string]any `yaml:"extra"`
}

func (node subscriptionNode) supportsXHTTP() bool {
	_, err := node.xrayOutbound("validate")
	return err == nil
}

func (node subscriptionNode) xrayOutbound(tag string) (map[string]any, error) {
	if !strings.EqualFold(node.Type, "vless") || !strings.EqualFold(node.Network, "xhttp") {
		return nil, errors.New("XHTTP requires a VLESS node")
	}
	if node.Server == "" || node.Port < 1 || node.Port > 65535 || node.UUID == "" ||
		strings.IndexFunc(node.Server, unicode.IsControl) >= 0 || node.Reality["public-key"] == "" {
		return nil, errors.New("XHTTP endpoint, identity or REALITY public key is missing or invalid")
	}
	if node.UDP != nil && !*node.UDP {
		return nil, errors.New("XHTTP node explicitly disables UDP")
	}
	serverName, fingerprint := node.ServerName, node.Fingerprint
	if serverName == "" {
		serverName = node.Server
	}
	if fingerprint == "" {
		fingerprint = "chrome"
	}
	path, mode := node.XHTTP.Path, node.XHTTP.Mode
	if path == "" {
		path = "/"
	}
	if mode == "" {
		mode = "auto"
	}
	user := map[string]any{"id": node.UUID, "encryption": "none", "flow": node.Flow}
	if node.PacketEncoding != "" {
		user["packetEncoding"] = node.PacketEncoding
	}
	xhttp := map[string]any{"host": node.XHTTP.Host, "path": path, "mode": mode}
	if len(node.XHTTP.Extra) != 0 {
		// Retain nested upstream options without aliasing the feed cache. Reject
		// non-JSON YAML rather than silently dropping negotiated transport fields.
		payload, err := json.Marshal(node.XHTTP.Extra)
		if err != nil {
			return nil, errors.New("XHTTP extra options are not JSON compatible")
		}
		var extra map[string]any
		if err := json.Unmarshal(payload, &extra); err != nil {
			return nil, errors.New("XHTTP extra options are invalid")
		}
		xhttp["extra"] = extra
	}
	return map[string]any{
		"protocol": "vless", "tag": tag,
		"settings": map[string]any{"vnext": []map[string]any{{
			"address": node.Server, "port": node.Port, "users": []map[string]any{user},
		}}},
		"streamSettings": map[string]any{
			"network": "xhttp", "security": "reality",
			"realitySettings": map[string]any{
				"serverName": serverName, "fingerprint": fingerprint,
				"publicKey": node.Reality["public-key"], "shortId": node.Reality["short-id"],
			},
			"xhttpSettings": xhttp,
		},
	}, nil
}

type xhttpBridges struct {
	allocatePort func() (int, error)
	ports        map[string]int
	reserved     map[int]bool
	inbounds     []map[string]any
	outbounds    []map[string]any
	rules        []map[string]any
}

func (bridges *xhttpBridges) outbound(node subscriptionNode, tag, runtimeID string) (map[string]any, error) {
	if runtimeID == "" {
		return nil, errors.New("XHTTP bridge requires a stable runtime identity")
	}
	if bridges.ports == nil {
		bridges.ports = map[string]int{}
	}
	if bridges.reserved == nil {
		bridges.reserved = map[int]bool{}
	}
	port, exists := bridges.ports[runtimeID]
	if !exists {
		digest := sha256.Sum256([]byte(runtimeID))
		start := (int(digest[0])<<16 | int(digest[1])<<8 | int(digest[2])) % 1000
		for offset := 0; offset < 1000; offset++ {
			candidate := 24000 + (start+offset)%1000
			if !bridges.reserved[candidate] {
				port = candidate
				break
			}
		}
		if port == 0 {
			return nil, errors.New("XHTTP loopback bridge ports exhausted")
		}
		if bridges.allocatePort != nil {
			var err error
			port, err = bridges.allocatePort()
			if err != nil || port < 1024 || port > 65535 || bridges.reserved[port] {
				return nil, errors.New("isolated XHTTP bridge port unavailable")
			}
		}
		inTag, outTag := fmt.Sprintf("in-%x", digest), fmt.Sprintf("out-%x", digest)
		outbound, err := node.xrayOutbound(outTag)
		if err != nil {
			return nil, err
		}
		bridges.ports[runtimeID], bridges.reserved[port] = port, true
		bridges.inbounds = append(bridges.inbounds, map[string]any{
			"listen": "127.0.0.1", "port": port, "protocol": "socks", "tag": inTag,
			"settings": map[string]any{"auth": "noauth", "udp": true, "ip": "127.0.0.1"},
		})
		bridges.outbounds = append(bridges.outbounds, outbound)
		bridges.rules = append(bridges.rules, map[string]any{
			"type": "field", "inboundTag": []string{inTag}, "outboundTag": outTag,
		})
	}
	return map[string]any{"type": "socks", "tag": tag, "version": "5", "server": "127.0.0.1", "server_port": port}, nil
}

// Original parse_share_link contract at ec620942:518-545. Malformed extra
// options are rejected instead of silently discarded; all other defaults stay.
func parseVLESSLink(raw string) (subscriptionNode, error) {
	var node subscriptionNode
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "vless") || parsed.Hostname() == "" || parsed.User == nil {
		return node, errors.New("VLESS link is invalid")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return node, errors.New("VLESS link port is invalid")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return node, errors.New("VLESS query is invalid")
	}
	first := func(keys ...string) string {
		for _, key := range keys {
			if value := query.Get(key); value != "" {
				return value
			}
		}
		return ""
	}
	node.Type, node.UUID, node.Server, node.Port = "vless", parsed.User.Username(), parsed.Hostname(), port
	node.Network = strings.ToLower(first("type", "network"))
	if node.Network == "" {
		node.Network = "tcp"
	}
	node.ServerName, node.Flow = first("sni", "peer", "host"), query.Get("flow")
	security := strings.ToLower(query.Get("security"))
	node.TLS = security == "tls" || security == "reality" || security == "xtls"
	insecure := strings.ToLower(first("allowInsecure", "insecure"))
	node.SkipCertVerify = insecure == "1" || insecure == "true"
	if security == "reality" {
		node.Reality = map[string]string{"public-key": first("pbk", "publicKey"), "short-id": first("sid", "shortId")}
		node.Fingerprint = query.Get("fp")
		if node.Fingerprint == "" {
			node.Fingerprint = "chrome"
		}
	}
	if alpn := query.Get("alpn"); alpn != "" {
		node.ALPN = splitALPN(alpn)
	}
	if node.Network == "xhttp" {
		node.XHTTP = xhttpOptions{Host: query.Get("host"), Path: query.Get("path"), Mode: query.Get("mode")}
		if extra := query.Get("extra"); extra != "" {
			if json.Unmarshal([]byte(extra), &node.XHTTP.Extra) != nil || node.XHTTP.Extra == nil {
				return subscriptionNode{}, errors.New("VLESS XHTTP extra must be a JSON object")
			}
		}
		node.PacketEncoding = query.Get("packetEncoding")
		if node.PacketEncoding == "" {
			node.PacketEncoding = "xudp"
		}
	}
	if node.Network == "ws" {
		node.WS.Path = query.Get("path")
		if host := query.Get("host"); host != "" {
			node.WS.Headers = map[string]string{"Host": host}
		}
	}
	return node, nil
}

func splitALPN(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func (bridges *xhttpBridges) config() ([]byte, error) {
	if len(bridges.inbounds) == 0 {
		return nil, nil
	}
	return json.MarshalIndent(map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": bridges.inbounds, "outbounds": bridges.outbounds,
		"routing": map[string]any{"domainStrategy": "AsIs", "rules": bridges.rules},
	}, "", "  ")
}
