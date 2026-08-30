package cellulario

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maximumBufferedStream = 1024 * 1024

type privateAddr struct {
	network string
	value   string
}

func (address privateAddr) Network() string { return address.network }
func (address privateAddr) String() string  { return address.value }

type streamConn struct {
	client  *Client
	handle  uint32
	network string
	remote  string

	mu            sync.Mutex
	buffer        bytes.Buffer
	packets       [][]byte
	buffered      int
	notify        chan struct{}
	closed        bool
	remoteErr     error
	readDeadline  time.Time
	writeDeadline time.Time
	closeOnce     sync.Once
	closeErr      error
}

func newStream(client *Client, handle uint32, network, remote string) *streamConn {
	return &streamConn{client: client, handle: handle, network: network, remote: remote, notify: make(chan struct{}, 1)}
}

func (stream *streamConn) Read(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	for {
		stream.mu.Lock()
		if stream.network == "udp" && len(stream.packets) != 0 {
			packet := stream.packets[0]
			stream.packets = stream.packets[1:]
			stream.buffered -= len(packet)
			count := copy(value, packet)
			stream.mu.Unlock()
			return count, nil
		}
		if stream.buffer.Len() != 0 {
			count, err := stream.buffer.Read(value)
			stream.buffered -= count
			stream.mu.Unlock()
			return count, err
		}
		if stream.remoteErr != nil {
			err := stream.remoteErr
			stream.mu.Unlock()
			return 0, err
		}
		if stream.closed {
			stream.mu.Unlock()
			return 0, net.ErrClosed
		}
		deadline := stream.readDeadline
		stream.mu.Unlock()
		if err := waitNotify(stream.notify, deadline); err != nil {
			return 0, err
		}
	}
}

func (stream *streamConn) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	stream.mu.Lock()
	closed, deadline := stream.closed, stream.writeDeadline
	stream.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	ctx, cancel := streamContext(deadline)
	defer cancel()
	var message byte
	var payload []byte
	if stream.network == "tcp" {
		message = messageTCPWrite
		payload = make([]byte, 4+len(value))
		binary.BigEndian.PutUint32(payload, stream.handle)
		copy(payload[4:], value)
	} else {
		message = messageUDPSend
		target, err := targetPayload(stream.remote)
		if err != nil {
			return 0, err
		}
		payload = make([]byte, 4+len(target)+len(value))
		binary.BigEndian.PutUint32(payload, stream.handle)
		copy(payload[4:], target)
		copy(payload[4+len(target):], value)
	}
	if _, err := stream.client.request(ctx, message, payload); err != nil {
		return 0, err
	}
	return len(value), nil
}

func (stream *streamConn) Close() error {
	stream.closeOnce.Do(func() {
		stream.mu.Lock()
		stream.closed = true
		stream.mu.Unlock()
		stream.signal()
		stream.client.mu.Lock()
		if stream.network == "tcp" {
			delete(stream.client.tcp, stream.handle)
		} else {
			delete(stream.client.udp, stream.handle)
		}
		stream.client.mu.Unlock()
		if stream.client.Alive() {
			payload := make([]byte, 4)
			binary.BigEndian.PutUint32(payload, stream.handle)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			message := byte(messageTCPClose)
			if stream.network == "udp" {
				message = messageUDPClose
			}
			_, stream.closeErr = stream.client.request(ctx, message, payload)
		}
	})
	return stream.closeErr
}

func (stream *streamConn) LocalAddr() net.Addr {
	return privateAddr{network: stream.network, value: "private-cellular"}
}

func (stream *streamConn) RemoteAddr() net.Addr {
	return privateAddr{network: stream.network, value: stream.remote}
}

func (stream *streamConn) SetDeadline(deadline time.Time) error {
	stream.mu.Lock()
	stream.readDeadline, stream.writeDeadline = deadline, deadline
	stream.mu.Unlock()
	stream.signal()
	return nil
}

func (stream *streamConn) SetReadDeadline(deadline time.Time) error {
	stream.mu.Lock()
	stream.readDeadline = deadline
	stream.mu.Unlock()
	stream.signal()
	return nil
}

func (stream *streamConn) SetWriteDeadline(deadline time.Time) error {
	stream.mu.Lock()
	stream.writeDeadline = deadline
	stream.mu.Unlock()
	return nil
}

func (stream *streamConn) feed(value []byte) {
	stream.mu.Lock()
	if !stream.closed && stream.remoteErr == nil {
		if stream.buffered+len(value) > maximumBufferedStream {
			stream.remoteErr = errors.New("private cellular receive buffer exceeded its bound")
		} else if stream.network == "udp" {
			stream.packets = append(stream.packets, append([]byte(nil), value...))
			stream.buffered += len(value)
		} else {
			_, _ = stream.buffer.Write(value)
			stream.buffered += len(value)
		}
	}
	stream.mu.Unlock()
	stream.signal()
}

func (stream *streamConn) remoteClose(err error) {
	if err == nil {
		err = io.EOF
	}
	stream.mu.Lock()
	if stream.remoteErr == nil {
		stream.remoteErr = err
	}
	stream.mu.Unlock()
	stream.signal()
}

func (stream *streamConn) signal() {
	select {
	case stream.notify <- struct{}{}:
	default:
	}
}

func (client *Client) OpenTCP(ctx context.Context, address string) (net.Conn, error) {
	target, err := targetPayload(address)
	if err != nil {
		return nil, err
	}
	value, err := client.request(ctx, messageTCPOpen, target)
	if err != nil {
		return nil, err
	}
	if len(value) != 4 {
		err := errors.New("cellular companion returned an invalid TCP handle")
		client.fail(err)
		return nil, err
	}
	handle := binary.BigEndian.Uint32(value)
	stream := newStream(client, handle, "tcp", address)
	if err := client.registerStream(stream); err != nil {
		client.fail(err)
		return nil, err
	}
	return stream, nil
}

func (client *Client) OpenUDP(ctx context.Context, address string) (net.Conn, error) {
	if _, err := targetPayload(address); err != nil {
		return nil, err
	}
	value, err := client.request(ctx, messageUDPOpen, nil)
	if err != nil {
		return nil, err
	}
	if len(value) != 4 {
		err := errors.New("cellular companion returned an invalid UDP handle")
		client.fail(err)
		return nil, err
	}
	handle := binary.BigEndian.Uint32(value)
	stream := newStream(client, handle, "udp", address)
	if err := client.registerStream(stream); err != nil {
		client.fail(err)
		return nil, err
	}
	return stream, nil
}

func targetPayload(address string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || host == "" || len(host) > 253 {
		return nil, errors.New("invalid private cellular target")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("invalid private cellular target")
	}
	for _, character := range host {
		if character < 0x21 || character > 0x7e {
			return nil, errors.New("invalid private cellular target")
		}
	}
	payload := make([]byte, 4+len(host))
	binary.BigEndian.PutUint16(payload, uint16(len(host)))
	binary.BigEndian.PutUint16(payload[2:], uint16(port))
	copy(payload[4:], host)
	return payload, nil
}

func waitNotify(notify <-chan struct{}, deadline time.Time) error {
	if deadline.IsZero() {
		<-notify
		return nil
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return osDeadlineExceeded{}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-notify:
		return nil
	case <-timer.C:
		return osDeadlineExceeded{}
	}
}

type osDeadlineExceeded struct{}

func (osDeadlineExceeded) Error() string   { return "i/o timeout" }
func (osDeadlineExceeded) Timeout() bool   { return true }
func (osDeadlineExceeded) Temporary() bool { return true }

func streamContext(deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() {
		return context.WithTimeout(context.Background(), 30*time.Second)
	}
	return context.WithDeadline(context.Background(), deadline)
}
