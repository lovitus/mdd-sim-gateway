//go:build linux

package linuxmodem

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type serialModemProfile struct {
	Name        string `json:"name"`
	VID         string `json:"vid"`
	PID         string `json:"pid"`
	ATInterface *uint8 `json:"at_interface,omitempty"`
}

type serialModemDevice struct {
	ID, Name, TTY, USBPath, VID, PID string
	USB                              usbGeneration
}

var legacyModemSlug = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

func ValidateSerialProfiles(payload []byte) error { _, err := parseSerialProfiles(payload); return err }

func parseSerialProfiles(payload []byte) ([]serialModemProfile, error) {
	var profiles []serialModemProfile
	// The original MDD USB whitelist may be empty: no modem is then selected.
	if len(payload) == 0 {
		return nil, nil
	}
	if len(payload) > 1<<20 || json.Unmarshal(payload, &profiles) != nil {
		return nil, errors.New("invalid serial modem profiles")
	}
	seen := make(map[string]bool)
	for _, profile := range profiles {
		if len(profile.VID) != 4 || len(profile.PID) != 4 {
			return nil, errors.New("invalid modem USB profile")
		}
		if _, err := strconv.ParseUint(profile.VID, 16, 16); err != nil {
			return nil, err
		}
		if _, err := strconv.ParseUint(profile.PID, 16, 16); err != nil {
			return nil, err
		}
		key := strings.ToLower(profile.VID + ":" + profile.PID)
		if seen[key] {
			return nil, errors.New("duplicate modem USB profile")
		}
		seen[key] = true
	}
	return profiles, nil
}

// Direct adaptation of ec620942 host/mdd_orchestrator.py:usb_modems and slug.
// Discovery is sysfs-only and admits only explicitly profiled modem interfaces.
func discoverSerialModems(sysRoot string, profiles []serialModemProfile) ([]serialModemDevice, error) {
	root := filepath.Join(sysRoot, "bus", "usb", "devices")
	nodes, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	byUSB := make(map[string]serialModemProfile, len(profiles))
	for _, profile := range profiles {
		byUSB[strings.ToLower(profile.VID)+":"+strings.ToLower(profile.PID)] = profile
	}
	result := make([]serialModemDevice, 0)
	for _, node := range nodes {
		path := filepath.Join(root, node.Name())
		vid, err := readSysfsToken(filepath.Join(path, "idVendor"), 4, 4, 16)
		if err != nil {
			continue
		}
		pid, err := readSysfsToken(filepath.Join(path, "idProduct"), 4, 4, 16)
		if err != nil {
			continue
		}
		profile, ok := byUSB[strings.ToLower(vid)+":"+strings.ToLower(pid)]
		if !ok {
			continue
		}
		iface := uint8(2)
		if profile.ATInterface != nil {
			iface = *profile.ATInterface
		}
		interfacePath := filepath.Join(root, fmt.Sprintf("%s:1.%d", node.Name(), iface))
		ports, _ := filepath.Glob(filepath.Join(interfacePath, "ttyUSB*"))
		acm, _ := filepath.Glob(filepath.Join(interfacePath, "ttyACM*"))
		sort.Strings(ports)
		sort.Strings(acm)
		ports = append(ports, acm...)
		if len(ports) == 0 {
			continue
		}
		tty := filepath.Base(ports[0])
		usb, err := resolveUSBGeneration(sysRoot, []string{tty})
		if err != nil {
			return nil, err
		}
		serial, _ := os.ReadFile(filepath.Join(path, "serial"))
		identity := strings.TrimSpace(string(serial))
		if identity == "" {
			identity = node.Name()
		}
		id := strings.Trim(legacyModemSlug.ReplaceAllString(vid+"-"+pid+"-"+identity, "-"), "-")
		name := profile.Name
		if name == "" {
			name = "USB modem"
		}
		result = append(result, serialModemDevice{ID: id, Name: name, TTY: filepath.Join("/dev", tty), USBPath: node.Name(), VID: vid, PID: pid, USB: usb})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
