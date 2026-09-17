//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
)

// MM permanently rejects a forced-closed port object. Releasing the exact
// device through the existing data cleanup recreates that object; no daemon
// restart, RF reset, policy mutation or uncoordinated tty fallback is needed.
func (prober *Prober) ConnectionNeedsRecovery(ctx context.Context, target agentpolicy.Target) (bool, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	claim := prober.data[target.EquipmentID]
	if _, exact := claim.policyProfiles(target); !exact {
		return false, agentmodem.ErrOperationTargetReplaced
	}
	commands, ok := prober.manager.(modemCommandRuntime)
	if !ok {
		return false, nil
	}
	port := modemCommandPort{manager: prober.manager, snapshot: claim.commandSnapshot, epoch: claim.commandEpoch}
	if err := port.check(ctx); err != nil {
		return false, err
	}
	_, err := commands.Command(ctx, claim.commandSnapshot.ObjectPath, "AT", 3*time.Second)
	if err == nil {
		return false, nil
	}
	if !isForcedClosedPort(err) {
		return false, err
	}
	return true, nil
}

func isForcedClosedPort(err error) bool {
	var failure dbus.Error
	if !errors.As(err, &failure) {
		var pointer *dbus.Error
		if !errors.As(err, &pointer) || pointer == nil {
			return false
		}
		failure = *pointer
	}
	if failure.Name != "org.freedesktop.ModemManager1.Error.Core.Connected" {
		return false
	}
	// This is MM's protocol error at the driver boundary, not UI text.
	return strings.Contains(failure.Error(), "has been forced close")
}

func (manager *dbusModemManager) VoiceIdle(ctx context.Context, path dbus.ObjectPath) (bool, error) {
	var properties map[string]dbus.Variant
	if err := manager.connection.Object(mmService, path).CallWithContext(ctx, dbusProperties, 0, mmModem+".Voice").Store(&properties); err != nil {
		return false, err
	}
	paths, known := variantValue[[]dbus.ObjectPath](properties, "Calls")
	if !known {
		return false, errors.New("ModemManager call inventory unavailable")
	}
	for _, call := range paths {
		var view map[string]dbus.Variant
		if err := manager.connection.Object(mmService, call).CallWithContext(ctx, dbusProperties, 0, "org.freedesktop.ModemManager1.Call").Store(&view); err != nil {
			return false, err
		}
		state, known := variantValue[int32](view, "State")
		if !known || state != 7 {
			return false, nil
		}
	}
	return true, nil
}

// A retained ownership claim is not a live bearer. ModemManager's referenced
// Bearer.Connected property is the authority, including network-initiated loss.
// Recovery remains with agentpolicy's existing serialized, backoff reconciler.
func (prober *Prober) observeDataClaims(inventory []modemSnapshot) {
	for _, claim := range prober.data {
		claim.observedData = agentmodem.DataUnknown
		matches := 0
		for _, snapshot := range inventory {
			if snapshot.UID != claim.uid || snapshot.ObjectPath != claim.commandSnapshot.ObjectPath ||
				snapshot.EquipmentID != claim.target.EquipmentID || snapshot.ICCID != claim.target.CardID ||
				snapshot.SIMState != agentmodem.SIMReady {
				continue
			}
			matches++
			if snapshot.BearerStates == nil {
				continue
			}
			if snapshot.BearerStates[claim.bearer.ObjectPath] {
				claim.observedData = agentmodem.DataConnected
			} else if !snapshot.Connected {
				claim.observedData = agentmodem.DataDisconnected
			}
		}
		if matches != 1 {
			claim.observedData = agentmodem.DataUnknown
		}
	}
}
