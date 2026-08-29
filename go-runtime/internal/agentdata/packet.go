package agentdata

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// packetConn preserves one UDP datagram per WebSocket binary message while
// satisfying net.Conn for the SOCKS5 implementation.
type packetConn struct {
	socket  *websocket.Conn
	readMu  sync.Mutex
	writeMu sync.Mutex
	mu      sync.Mutex
	readBy  time.Time
	writeBy time.Time
}

func newPacketConn(socket *websocket.Conn) net.Conn { return &packetConn{socket: socket} }

func (conn *packetConn) Read(buffer []byte) (int, error) {
	conn.readMu.Lock()
	defer conn.readMu.Unlock()
	ctx, cancel := conn.deadlineContext(conn.readDeadline())
	defer cancel()
	kind, payload, err := conn.socket.Read(ctx)
	if err != nil {
		return 0, err
	}
	if kind != websocket.MessageBinary || len(payload) > maximumDatagram {
		return 0, errors.New("invalid UDP WebSocket frame")
	}
	if len(payload) > len(buffer) {
		return 0, io.ErrShortBuffer
	}
	return copy(buffer, payload), nil
}

func (conn *packetConn) Write(payload []byte) (int, error) {
	if len(payload) > maximumDatagram {
		return 0, errors.New("UDP datagram exceeds maximum size")
	}
	conn.writeMu.Lock()
	defer conn.writeMu.Unlock()
	ctx, cancel := conn.deadlineContext(conn.writeDeadline())
	defer cancel()
	if err := conn.socket.Write(ctx, websocket.MessageBinary, payload); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func (conn *packetConn) Close() error {
	return conn.socket.Close(websocket.StatusNormalClosure, "closed")
}
func (conn *packetConn) LocalAddr() net.Addr  { return dataAddr("core") }
func (conn *packetConn) RemoteAddr() net.Addr { return dataAddr("agent") }
func (conn *packetConn) SetDeadline(value time.Time) error {
	conn.mu.Lock()
	conn.readBy, conn.writeBy = value, value
	conn.mu.Unlock()
	return nil
}
func (conn *packetConn) SetReadDeadline(value time.Time) error {
	conn.mu.Lock()
	conn.readBy = value
	conn.mu.Unlock()
	return nil
}
func (conn *packetConn) SetWriteDeadline(value time.Time) error {
	conn.mu.Lock()
	conn.writeBy = value
	conn.mu.Unlock()
	return nil
}
func (conn *packetConn) readDeadline() time.Time {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.readBy
}
func (conn *packetConn) writeDeadline() time.Time {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.writeBy
}
func (conn *packetConn) deadlineContext(deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() {
		return context.WithCancel(context.Background())
	}
	return context.WithDeadline(context.Background(), deadline)
}

type dataAddr string

func (address dataAddr) Network() string { return "mdd-data" }
func (address dataAddr) String() string  { return string(address) }
