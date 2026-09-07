package egressexec

// The selected-outbound/UDP admission behavior comes from
// host/mdd_orchestrator.py:745-753,1991-1999 at ec620942. Unlike the old
// shallow copy, include named detour dependencies so they cannot accidentally
// bind to another country's outbound after tags are renamed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
)

func existingUDP(outbound map[string]any) bool {
	if outbound["network"] == "tcp" {
		return false
	}
	kind, _ := outbound["type"].(string)
	switch strings.ToLower(kind) {
	case "socks":
		return outbound["version"] == nil || fmt.Sprint(outbound["version"]) == "5"
	case "shadowsocks", "trojan", "vless", "vmess", "hysteria", "hysteria2", "tuic", "wireguard":
		return true
	default:
		return false
	}
}

func renderExisting(path, selected, finalTag, expectedHash string) ([]map[string]any, string, error) {
	if selected == "" || len(expectedHash) != 64 {
		return nil, "", errors.New("existing outbound requires a selected tag and applied source digest")
	}
	payload, err := egressconfig.ReadExistingConfig(path)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != expectedHash {
		return nil, "", errors.New("existing outbound source changed; apply the configuration again")
	}
	var source struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if json.Unmarshal(payload, &source) != nil {
		return nil, "", errors.New("existing outbound configuration is not valid JSON")
	}
	byTag := make(map[string]map[string]any, len(source.Outbounds))
	for _, outbound := range source.Outbounds {
		tag, _ := outbound["tag"].(string)
		if tag == "" {
			continue
		}
		if byTag[tag] != nil {
			return nil, "", errors.New("existing outbound configuration has duplicate tags")
		}
		byTag[tag] = outbound
	}
	if !existingUDP(byTag[selected]) {
		return nil, "", errors.New("selected existing outbound is missing or not UDP-capable")
	}
	var built []map[string]any
	tags := map[string]string{selected: finalTag}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(tag string) error {
		if done[tag] {
			return nil
		}
		if visiting[tag] {
			return errors.New("existing outbound detour cycle")
		}
		if len(visiting) >= 64 {
			return errors.New("existing outbound dependency limit exceeded")
		}
		outbound := byTag[tag]
		if outbound == nil {
			return errors.New("existing outbound detour is missing")
		}
		if !existingUDP(outbound) && outbound["type"] != "direct" {
			return errors.New("existing outbound detour is not UDP-capable")
		}
		visiting[tag] = true
		if _, exists := tags[tag]; !exists {
			tags[tag] = fmt.Sprintf("%s-import-%d", finalTag, len(tags))
		}
		outbound["tag"] = tags[tag]
		if raw, exists := outbound["detour"]; exists {
			detour, ok := raw.(string)
			if !ok {
				return errors.New("existing outbound detour is invalid")
			}
			if detour != "" {
				if err := visit(detour); err != nil {
					return err
				}
				outbound["detour"] = tags[detour]
			}
		}
		built = append(built, outbound)
		done[tag] = true
		return nil
	}
	if err := visit(selected); err != nil {
		return nil, "", err
	}
	return built, selected, nil
}
