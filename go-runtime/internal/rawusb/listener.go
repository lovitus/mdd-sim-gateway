package rawusb

import (
	"context"
	"errors"
	"net"
	"sync"
)

var errListenerClosed = errors.New("raw USB listener is closed")

const maximumPendingStreams = 32

// connListener adapts authenticated, Core-requested Agent streams to the
// net.Listener accepted by sing-usbip. It never opens a host TCP port.
type connListener struct {
	mu     sync.Mutex
	queue  []net.Conn
	closed bool
	wake   chan struct{}
}

func newConnListener() *connListener {
	return &connListener{wake: make(chan struct{}, 1)}
}

func (listener *connListener) Accept() (net.Conn, error) {
	for {
		listener.mu.Lock()
		if len(listener.queue) != 0 {
			conn := listener.queue[0]
			listener.queue[0] = nil
			listener.queue = listener.queue[1:]
			listener.mu.Unlock()
			return conn, nil
		}
		if listener.closed {
			listener.mu.Unlock()
			return nil, errListenerClosed
		}
		wake := listener.wake
		listener.mu.Unlock()
		<-wake
	}
}

func (listener *connListener) Enqueue(ctx context.Context, conn net.Conn) error {
	if conn == nil {
		return errors.New("raw USB stream is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	listener.mu.Lock()
	if listener.closed {
		listener.mu.Unlock()
		return errListenerClosed
	}
	if len(listener.queue) >= maximumPendingStreams {
		listener.mu.Unlock()
		return errors.New("raw USB listener queue is full")
	}
	listener.queue = append(listener.queue, conn)
	select {
	case listener.wake <- struct{}{}:
	default:
	}
	listener.mu.Unlock()
	return nil
}

func (listener *connListener) Close() error {
	listener.mu.Lock()
	if listener.closed {
		listener.mu.Unlock()
		return nil
	}
	listener.closed = true
	queued := listener.queue
	listener.queue = nil
	select {
	case listener.wake <- struct{}{}:
	default:
	}
	listener.mu.Unlock()
	for _, conn := range queued {
		_ = conn.Close()
	}
	return nil
}

func (listener *connListener) Addr() net.Addr { return rawUSBAddr("agent") }

type rawUSBAddr string

func (address rawUSBAddr) Network() string { return "mdd-raw-usb" }
func (address rawUSBAddr) String() string  { return string(address) }
