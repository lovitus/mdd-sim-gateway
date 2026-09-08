//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"path/filepath"
	"sort"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

type serialInventory struct {
	sysRoot         string
	profiles        []serialModemProfile
	open            agentat.Opener
	verifyProtected func(context.Context, string, []string) error
	identities      map[string]string
}

// Inventory retains the original profiled serial discovery, but hardware IMEI
// is proved through AT rather than inferred from the USB display identifier.
func (inventory *serialInventory) Inventory(ctx context.Context) ([]modemSnapshot, error) {
	if inventory.open == nil || inventory.verifyProtected == nil {
		return nil, errors.New("serial inventory requires protected USB ownership")
	}
	devices, err := discoverSerialModems(inventory.sysRoot, inventory.profiles)
	if err != nil {
		return nil, err
	}
	next := make(map[string]string, len(devices))
	result := make([]modemSnapshot, 0, len(devices))
	for _, device := range devices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		paths, err := filepath.Glob(filepath.Join(device.USB.PhysicalID, "*", "net", "*"))
		if err != nil {
			return nil, err
		}
		netPorts := make([]string, 0, len(paths))
		for _, path := range paths {
			netPorts = append(netPorts, filepath.Base(path))
		}
		sort.Strings(netPorts)
		if err := inventory.verifyProtected(ctx, device.USB.PhysicalID, netPorts); err != nil {
			return nil, err
		}
		key := device.USB.Generation + "\x00" + device.TTY
		equipment := inventory.identities[key]
		if equipment == "" {
			equipment, err = agentat.IdentifyProfiledPort(ctx, agentat.Candidate{Name: filepath.Base(device.TTY), Product: device.Name, USB: true, PhysicalID: device.USB.PhysicalID}, inventory.open)
			if err != nil {
				return nil, err
			}
		}
		next[key] = equipment
		result = append(result, modemSnapshot{UID: device.USB.PhysicalID, EquipmentID: equipment, Model: device.Name,
			ATPorts: []string{filepath.Base(device.TTY)}, NetPorts: netPorts, SIMState: agentmodem.SIMUnknown, Registration: agentmodem.RegistrationUnknown})
	}
	inventory.identities = next
	return result, nil
}
