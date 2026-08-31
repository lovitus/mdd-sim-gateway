//go:build linux || windows

package rawusb

import (
	"context"
	"errors"
	"net"
	"sync"

	mux "github.com/sagernet/sing-mux"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// ServeMultiplexed exposes all sing-usbip connections for this exact device
// through one already-authenticated WSS byte stream. sing-mux owns logical
// connection framing; MDD does not implement another multiplex protocol.
func (exporter *Exporter) ServeMultiplexed(ctx context.Context, stream net.Conn) error {
	if exporter == nil || exporter.listener == nil || ctx == nil || stream == nil {
		return errors.New("invalid multiplexed raw USB exporter stream")
	}
	service, err := mux.NewService(mux.ServiceOptions{
		NewStreamContext: func(parent context.Context, _ net.Conn) context.Context { return parent },
		Logger:           logger.NOP(),
		Handler:          muxExportHandler{exporter: exporter},
	})
	if err != nil {
		return err
	}
	return service.NewConnection(ctx, stream, M.Metadata{})
}

type muxExportHandler struct{ exporter *Exporter }

func (handler muxExportHandler) NewConnection(ctx context.Context, conn net.Conn, _ M.Metadata) error {
	return handler.exporter.AcceptStream(ctx, conn)
}

func (handler muxExportHandler) NewPacketConnection(_ context.Context, conn N.PacketConn, _ M.Metadata) error {
	_ = conn.Close()
	return errors.New("raw USB multiplex accepts byte streams only")
}

// NewMultiplexedImporter runs sing-usbip over one WSS stream. The importer may
// open multiple logical control/data connections, but sing-mux is configured
// to keep exactly one underlying transport connection for this raw session.
func NewMultiplexedImporter(ctx context.Context, device Device, stream net.Conn) (*Importer, error) {
	if ctx == nil || stream == nil {
		return nil, errors.New("invalid multiplexed raw USB importer stream")
	}
	client, err := newMuxClient(stream)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	dial := func(streamContext context.Context) (net.Conn, error) {
		return client.DialContext(streamContext, N.NetworkTCP, M.ParseSocksaddrHostPort("usbip.mdd.internal", 3240))
	}
	importer, err := newImporter(ctx, device, dial, client)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return importer, nil
}

func newMuxClient(stream net.Conn) (*mux.Client, error) {
	if stream == nil {
		return nil, errors.New("raw USB multiplex transport is unavailable")
	}
	return newMuxClientWithDialer(&singleConnDialer{conn: stream})
}

func newMuxClientWithDialer(dialer *singleConnDialer) (*mux.Client, error) {
	if dialer == nil {
		return nil, errors.New("raw USB multiplex dialer is unavailable")
	}
	return mux.NewClient(mux.Options{
		// sing-usbip applies read/write deadlines to its control and import
		// streams. sing-mux h2mux deliberately returns os.ErrInvalid for those
		// net.Conn methods; yamux provides the required deadline contract while
		// retaining the same one-underlying-WSS, many-logical-stream boundary.
		Dialer: dialer, Logger: logger.NOP(), Protocol: "yamux", MaxConnections: 1,
	})
}

type singleConnDialer struct {
	mu    sync.Mutex
	conn  net.Conn
	dials uint32
}

func (dialer *singleConnDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	dialer.dials++
	if dialer.conn == nil {
		return nil, errors.New("raw USB WSS session cannot be re-dialled")
	}
	conn := dialer.conn
	dialer.conn = nil
	return conn, nil
}

func (dialer *singleConnDialer) dialCount() uint32 {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	return dialer.dials
}

func (*singleConnDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("raw USB WSS session does not support packet sockets")
}

var _ N.Dialer = (*singleConnDialer)(nil)
var _ N.TCPConnectionHandler = muxExportHandler{}
var _ N.UDPConnectionHandler = muxExportHandler{}
