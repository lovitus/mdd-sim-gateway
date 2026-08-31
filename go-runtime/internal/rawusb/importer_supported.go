//go:build linux || windows

package rawusb

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"

	usbip "github.com/sagernet/sing-usbip"
	M "github.com/sagernet/sing/common/metadata"
)

// StreamDial opens one authenticated full-duplex byte stream to the matching
// Agent exporter. A sing-usbip control, inventory, or import connection always
// gets its own stream; implementations may therefore map each call to one WSS.
type StreamDial func(context.Context) (net.Conn, error)

type Importer struct {
	service   *usbip.ClientService
	transport interface{ Close() error }
	once      sync.Once
	closeErr  error
}

// NewImporter reuses sing-usbip's native importer and supplies only the
// transport. No TCP address is opened or dialled by this package.
func NewImporter(ctx context.Context, device Device, dial StreamDial) (*Importer, error) {
	return newImporter(ctx, device, dial, nil)
}

func newImporter(ctx context.Context, device Device, dial StreamDial, transport interface{ Close() error }) (*Importer, error) {
	if ctx == nil || strings.TrimSpace(device.BusID) == "" || device.VendorID == 0 || device.ProductID == 0 || dial == nil {
		return nil, errors.New("invalid raw USB importer configuration")
	}
	service, err := usbip.NewClientService(ctx, usbip.ClientOptions{
		Logger: dependencyLogger{component: "sing-usbip importer"},
		Dialer: streamDialer{dial: dial},
		// sing-usbip requires a syntactically valid address even when a custom
		// Dialer owns every connection. streamDialer ignores this sentinel.
		ServerAddress: M.ParseSocksaddrHostPort("127.0.0.1", usbip.DefaultPort),
		Devices: []usbip.DeviceMatch{{
			BusID: device.BusID, VendorID: device.VendorID,
			ProductID: device.ProductID, Serial: device.Serial,
		}},
	})
	if err != nil {
		return nil, err
	}
	return &Importer{service: service, transport: transport}, nil
}

func (importer *Importer) Start() error {
	if importer == nil || importer.service == nil {
		return errors.New("raw USB importer is unavailable")
	}
	return importer.service.Start()
}

func (importer *Importer) Close() error {
	if importer == nil {
		return nil
	}
	importer.once.Do(func() {
		var transportErr error
		if importer.transport != nil {
			transportErr = importer.transport.Close()
		}
		importer.closeErr = errors.Join(importer.service.Close(), transportErr)
	})
	return importer.closeErr
}

type streamDialer struct{ dial StreamDial }

func (dialer streamDialer) DialContext(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
	if network != "tcp" {
		return nil, errors.New("raw USB importer accepts byte streams only")
	}
	return dialer.dial(ctx)
}

func (streamDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("raw USB importer does not support packet sockets")
}
