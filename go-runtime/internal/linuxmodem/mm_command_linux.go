//go:build linux

package linuxmodem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

const mmCommandPrefix = "mm-command:"

func commandPortName(path dbus.ObjectPath) string {
	return mmCommandPrefix + strings.TrimPrefix(string(path), "/org/freedesktop/ModemManager1/Modem/")
}

type modemCommandRuntime interface {
	Command(context.Context, dbus.ObjectPath, string, time.Duration) ([]byte, error)
	SendText(context.Context, dbus.ObjectPath, string, string, func(context.Context) error) ([]int, error)
}

// Adapted from ec620942 host/vpcd_modem_bridge.py:ModemManagerCard.
// ModemManager remains the only physical AT owner while QMI carries data.
type modemCommandPort struct {
	manager  modemManager
	commands modemCommandRuntime
	snapshot modemSnapshot
	epoch    string
	closed   bool
}

func (port *modemCommandPort) check(ctx context.Context) error {
	if port.closed {
		return errors.New("ModemManager command port is closed")
	}
	if port.epoch != "" {
		source, ok := port.manager.(interface {
			SIMEpoch(dbus.ObjectPath, dbus.ObjectPath) (string, bool)
		})
		if !ok {
			return agentmodem.ErrOperationTargetReplaced
		}
		epoch, available := source.SIMEpoch(port.snapshot.ObjectPath, port.snapshot.SIMPath)
		if !available || epoch != port.epoch {
			return agentmodem.ErrOperationTargetReplaced
		}
	}
	current, err := port.manager.Inventory(ctx)
	if err != nil {
		return err
	}
	for _, modem := range current {
		if modem.ObjectPath == port.snapshot.ObjectPath && modem.UID == port.snapshot.UID &&
			modem.EquipmentID == port.snapshot.EquipmentID && modem.ICCID == port.snapshot.ICCID && modem.SIMState == agentmodem.SIMReady {
			return nil
		}
	}
	return agentmodem.ErrOperationTargetReplaced
}
func (port *modemCommandPort) Exchange(ctx context.Context, command string, timeout time.Duration) ([]byte, error) {
	if err := port.check(ctx); err != nil {
		return nil, err
	}
	return port.commands.Command(ctx, port.snapshot.ObjectPath, command, timeout)
}
func (port *modemCommandPort) SubmitSMSText(ctx context.Context, number, body string) ([]int, error) {
	if err := port.check(ctx); err != nil {
		return nil, err
	}
	return port.commands.SendText(ctx, port.snapshot.ObjectPath, number, body, port.check)
}
func (port *modemCommandPort) Close() error        { port.closed = true; return nil }
func (*modemCommandPort) Read([]byte) (int, error) { return 0, io.EOF }
func (*modemCommandPort) Write([]byte) (int, error) {
	return 0, errors.New("split ModemManager command write is forbidden")
}
func (*modemCommandPort) Drain() error {
	return errors.New("split ModemManager command drain is forbidden")
}
func (*modemCommandPort) ResetInputBuffer() error {
	return errors.New("split ModemManager command reset is forbidden")
}

func (manager *dbusModemManager) Command(ctx context.Context, path dbus.ObjectPath, command string, timeout time.Duration) ([]byte, error) {
	if !path.IsValid() || path == "/" || timeout <= 0 || !strings.HasPrefix(command, "AT") || strings.ContainsAny(command, "\r\n\x00") {
		return nil, errors.New("invalid ModemManager command transaction")
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var response string
	if err := manager.connection.Object(mmService, path).CallWithContext(bounded, mmModem+".Command", 0, command, uint32((timeout+time.Second-1)/time.Second)).Store(&response); err != nil {
		return nil, fmt.Errorf("ModemManager AT transaction: %w", err)
	}
	// Command returns its body after MM has consumed the terminal OK.
	return []byte(response + "\r\nOK\r\n"), nil
}

// Retain the old cellular_sms.py Create/Send split: Create is not submission;
// any failure after Send begins is uncertain and may not be retried as new SMS.
func (manager *dbusModemManager) SendText(ctx context.Context, path dbus.ObjectPath, number, body string, beforeSend func(context.Context) error) ([]int, error) {
	const messaging = "org.freedesktop.ModemManager1.Modem.Messaging"
	const smsInterface = "org.freedesktop.ModemManager1.Sms"
	var smsPath dbus.ObjectPath
	properties := map[string]dbus.Variant{"number": dbus.MakeVariant(number), "text": dbus.MakeVariant(body)}
	if err := manager.connection.Object(mmService, path).CallWithContext(ctx, messaging+".Create", 0, properties).Store(&smsPath); err != nil {
		return nil, err
	}
	if !smsPath.IsValid() || !strings.HasPrefix(string(smsPath), "/org/freedesktop/ModemManager1/SMS/") {
		return nil, errors.New("invalid created SMS path")
	}
	object := manager.connection.Object(mmService, smsPath)
	if beforeSend == nil {
		return nil, errors.New("missing SMS identity fence")
	}
	if err := beforeSend(ctx); err != nil {
		return nil, err
	}
	if err := object.CallWithContext(ctx, smsInterface+".Send", 0).Err; err != nil {
		return nil, &agentat.SMSSubmitError{PossiblySent: true, Err: err}
	}
	var result map[string]dbus.Variant
	if err := object.CallWithContext(ctx, dbusProperties, 0, smsInterface).Store(&result); err != nil {
		return nil, &agentat.SMSSubmitError{PossiblySent: true, Err: err}
	}
	reference, known := variantValue[uint32](result, "MessageReference")
	if uint32Property(result, "State") != 5 || !known || reference > 255 {
		return nil, &agentat.SMSSubmitError{PossiblySent: true, Err: errors.New("ModemManager SMS submission is not confirmed")}
	}
	return []int{int(reference)}, nil
}
