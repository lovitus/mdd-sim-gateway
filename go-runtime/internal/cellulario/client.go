package cellulario

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	protocolVersion = 1
	maximumFrame    = 1024 * 1024

	messageHello          = 1
	messageResolve        = 2
	messageTCPOpen        = 3
	messageTCPWrite       = 4
	messageTCPClose       = 5
	messageUDPOpen        = 6
	messageUDPSend        = 7
	messageUDPClose       = 8
	messageShutdown       = 10
	messageDataEnable     = 11
	messageDataDisable    = 12
	messageDNSServer      = 13
	messageIsolationCheck = 15
	messageATCommandV2    = 16
	messageSMSSubmit      = 17

	messageResponse  = 0x80
	messageTCPData   = 0x90
	messageTCPEOF    = 0x91
	messageUDPData   = 0x92
	messageLinkState = 0x93
)

var errClientClosed = errors.New("private cellular companion is closed")

type ProtocolError struct {
	Status int32
	Detail string
}

func (failure *ProtocolError) Error() string {
	if failure.Detail != "" {
		return failure.Detail
	}
	return fmt.Sprintf("private cellular companion error %d", failure.Status)
}

type response struct {
	status int32
	value  []byte
	err    error
}

type earlyStreamData struct {
	chunks [][]byte
	total  int
	eof    bool
}

type Client struct {
	transport io.ReadWriteCloser
	cleanup   func() error

	requestMu sync.Mutex
	writeMu   sync.Mutex
	mu        sync.Mutex
	nextID    uint32
	pending   map[uint32]chan response
	tcp       map[uint32]*streamConn
	udp       map[uint32]*streamConn
	earlyTCP  map[uint32]*earlyStreamData
	earlyUDP  map[uint32]*earlyStreamData
	earlySize int
	identity  map[string]string
	linkState string
	failure   error
	done      chan struct{}
	failOnce  sync.Once
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
}

func newClient(transport io.ReadWriteCloser, cleanup func() error) (*Client, error) {
	if transport == nil {
		return nil, errors.New("private cellular transport is required")
	}
	client := &Client{
		transport: transport, cleanup: cleanup, nextID: 1,
		pending: make(map[uint32]chan response), tcp: make(map[uint32]*streamConn),
		udp: make(map[uint32]*streamConn), earlyTCP: make(map[uint32]*earlyStreamData),
		earlyUDP: make(map[uint32]*earlyStreamData), linkState: "starting",
		done: make(chan struct{}), closeDone: make(chan struct{}),
	}
	go client.readLoop()
	return client, nil
}

func (client *Client) initialize(ctx context.Context) error {
	value, err := client.request(ctx, messageHello, nil)
	if err != nil {
		return err
	}
	identity := make(map[string]string)
	for _, item := range strings.Split(string(value), ";") {
		key, value, found := strings.Cut(item, "=")
		if found && strings.TrimSpace(key) != "" {
			identity[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if identity["version"] != "1" || identity["at_transactions"] != "2" || identity["sms_submit"] != "1" {
		return errors.New("cellular companion protocol is incompatible")
	}
	client.mu.Lock()
	client.identity = identity
	client.mu.Unlock()
	return nil
}

func (client *Client) Identity() map[string]string {
	client.mu.Lock()
	defer client.mu.Unlock()
	result := make(map[string]string, len(client.identity))
	for key, value := range client.identity {
		result[key] = value
	}
	return result
}

func (client *Client) Alive() bool {
	select {
	case <-client.done:
		return false
	default:
		return true
	}
}

func (client *Client) Done() <-chan struct{} { return client.done }

func (client *Client) Failure() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.failure
}

func (client *Client) LinkState() string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.linkState
}

func (client *Client) Qualify(ctx context.Context) error {
	ctx, cancel := boundedContext(ctx, 10*time.Second)
	defer cancel()
	_, err := client.request(ctx, messageIsolationCheck, nil)
	return err
}

func (client *Client) EnableData(ctx context.Context) error {
	ctx, cancel := boundedContext(ctx, 75*time.Second)
	defer cancel()
	_, err := client.request(ctx, messageDataEnable, nil)
	return err
}

func (client *Client) DisableData(ctx context.Context) error {
	ctx, cancel := boundedContext(ctx, 15*time.Second)
	defer cancel()
	_, err := client.request(ctx, messageDataDisable, nil)
	return err
}

func (client *Client) AT(ctx context.Context, command string, timeout time.Duration) ([]byte, error) {
	if timeout < 300*time.Millisecond || timeout > 30*time.Second || len(command) < 2 || len(command) > 499 ||
		!strings.HasPrefix(command, "AT") || strings.ContainsAny(command, "\r\n") {
		return nil, errors.New("invalid bounded AT command")
	}
	for _, character := range command {
		if character < 0x20 || character > 0x7e {
			return nil, errors.New("invalid bounded AT command")
		}
	}
	helperTimeout := timeout - 200*time.Millisecond
	if helperTimeout < 100*time.Millisecond {
		helperTimeout = 100 * time.Millisecond
	}
	payload := make([]byte, 4+len(command))
	binary.BigEndian.PutUint32(payload, uint32(helperTimeout.Milliseconds()))
	copy(payload[4:], command)
	ctx, cancel := boundedContext(ctx, timeout)
	defer cancel()
	return client.request(ctx, messageATCommandV2, payload)
}

func (client *Client) SubmitSMSPDU(ctx context.Context, length int, pdu string) ([]byte, bool, error) {
	if length < 1 || length > 140 || len(pdu) < 2 || len(pdu) > 1024 || len(pdu)%2 != 0 {
		return nil, false, errors.New("invalid SMS PDU")
	}
	for _, character := range pdu {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F') {
			return nil, false, errors.New("invalid SMS PDU")
		}
	}
	command := fmt.Sprintf("AT+CMGS=%d", length)
	payload := make([]byte, 2+len(command)+len(pdu))
	binary.BigEndian.PutUint16(payload, uint16(len(command)))
	copy(payload[2:], command)
	copy(payload[2+len(command):], pdu)
	ctx, cancel := boundedContext(ctx, 130*time.Second)
	defer cancel()
	value, err := client.request(ctx, messageSMSSubmit, payload)
	possiblySent := err != nil && strings.Contains(err.Error(), "sms_submit_unknown_after_pdu")
	return value, possiblySent, err
}

func (client *Client) request(ctx context.Context, messageType byte, payload []byte) ([]byte, error) {
	if len(payload) > maximumFrame {
		return nil, errors.New("private cellular payload is too large")
	}
	client.requestMu.Lock()
	defer client.requestMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client.mu.Lock()
	if client.failure != nil {
		err := client.failure
		client.mu.Unlock()
		return nil, err
	}
	requestID := client.nextID
	client.nextID++
	if requestID == 0 {
		requestID = client.nextID
		client.nextID++
	}
	answer := make(chan response, 1)
	client.pending[requestID] = answer
	client.mu.Unlock()

	frame := make([]byte, 12+len(payload))
	frame[0], frame[1] = protocolVersion, messageType
	binary.BigEndian.PutUint32(frame[4:], requestID)
	binary.BigEndian.PutUint32(frame[8:], uint32(len(payload)))
	copy(frame[12:], payload)
	client.writeMu.Lock()
	err := writeFull(client.transport, frame)
	client.writeMu.Unlock()
	if err != nil {
		client.removePending(requestID)
		client.fail(fmt.Errorf("write private cellular frame: %w", err))
		return nil, err
	}
	select {
	case current := <-answer:
		if current.err != nil {
			return nil, current.err
		}
		if current.status != 0 {
			return nil, &ProtocolError{Status: current.status, Detail: boundedText(string(current.value), 1024)}
		}
		return current.value, nil
	case <-ctx.Done():
		client.removePending(requestID)
		client.fail(fmt.Errorf("private cellular request interrupted: %w", ctx.Err()))
		return nil, ctx.Err()
	case <-client.done:
		client.removePending(requestID)
		return nil, client.Failure()
	}
}

func (client *Client) removePending(requestID uint32) {
	client.mu.Lock()
	delete(client.pending, requestID)
	client.mu.Unlock()
}

func (client *Client) readLoop() {
	for {
		header := make([]byte, 12)
		if _, err := io.ReadFull(client.transport, header); err != nil {
			client.fail(fmt.Errorf("read private cellular header: %w", err))
			return
		}
		length := binary.BigEndian.Uint32(header[8:])
		if header[0] != protocolVersion || length > maximumFrame {
			client.fail(errors.New("invalid private cellular frame"))
			return
		}
		payload := make([]byte, int(length))
		if _, err := io.ReadFull(client.transport, payload); err != nil {
			client.fail(fmt.Errorf("read private cellular payload: %w", err))
			return
		}
		client.dispatch(header[1], binary.BigEndian.Uint32(header[4:]), payload)
	}
}

func (client *Client) dispatch(messageType byte, requestID uint32, payload []byte) {
	switch messageType {
	case messageResponse:
		if len(payload) < 4 {
			client.fail(errors.New("short private cellular response"))
			return
		}
		client.mu.Lock()
		answer := client.pending[requestID]
		delete(client.pending, requestID)
		client.mu.Unlock()
		if answer != nil {
			answer <- response{status: int32(binary.BigEndian.Uint32(payload[:4])), value: append([]byte(nil), payload[4:]...)}
		}
	case messageTCPData:
		if len(payload) >= 4 {
			client.feed("tcp", binary.BigEndian.Uint32(payload[:4]), payload[4:], false)
		}
	case messageTCPEOF:
		if len(payload) >= 4 {
			client.feed("tcp", binary.BigEndian.Uint32(payload[:4]), nil, true)
		}
	case messageUDPData:
		if len(payload) >= 10 {
			client.feed("udp", binary.BigEndian.Uint32(payload[:4]), payload[10:], false)
		}
	case messageLinkState:
		state := strings.TrimSpace(string(payload))
		if state == "down" || state == "connecting" || state == "up" {
			client.mu.Lock()
			client.linkState = state
			client.mu.Unlock()
		}
	}
}

func (client *Client) feed(network string, handle uint32, payload []byte, eof bool) {
	client.mu.Lock()
	values, early := client.tcp, client.earlyTCP
	if network == "udp" {
		values, early = client.udp, client.earlyUDP
	}
	stream := values[handle]
	var overflow bool
	if stream == nil && handle != 0 {
		pending := early[handle]
		if pending == nil {
			if len(early) >= 64 {
				overflow = true
			} else {
				pending = &earlyStreamData{}
				early[handle] = pending
			}
		}
		if pending != nil {
			if eof {
				pending.eof = true
			} else if len(payload) > maximumBufferedStream-client.earlySize {
				overflow = true
			} else {
				copy := append([]byte(nil), payload...)
				pending.chunks = append(pending.chunks, copy)
				pending.total += len(copy)
				client.earlySize += len(copy)
			}
		}
	}
	client.mu.Unlock()
	if overflow {
		client.fail(errors.New("private cellular early receive buffer exceeded its bound"))
		return
	}
	if stream == nil {
		return
	}
	if eof {
		stream.remoteClose(io.EOF)
	} else {
		stream.feed(payload)
	}
}

func (client *Client) registerStream(stream *streamConn) error {
	client.mu.Lock()
	if client.failure != nil {
		err := client.failure
		client.mu.Unlock()
		return err
	}
	values, early := client.tcp, client.earlyTCP
	if stream.network == "udp" {
		values, early = client.udp, client.earlyUDP
	}
	if stream.handle == 0 || client.tcp[stream.handle] != nil || client.udp[stream.handle] != nil {
		client.mu.Unlock()
		return errors.New("cellular companion returned a duplicate stream handle")
	}
	values[stream.handle] = stream
	pending := early[stream.handle]
	delete(early, stream.handle)
	if pending != nil {
		client.earlySize -= pending.total
	}
	client.mu.Unlock()
	if pending != nil {
		for _, payload := range pending.chunks {
			stream.feed(payload)
		}
		if pending.eof {
			stream.remoteClose(io.EOF)
		}
	}
	return nil
}

func (client *Client) fail(err error) {
	if err == nil {
		err = errClientClosed
	}
	client.failOnce.Do(func() {
		client.mu.Lock()
		client.failure = err
		pending := client.pending
		client.pending = make(map[uint32]chan response)
		streams := make([]*streamConn, 0, len(client.tcp)+len(client.udp))
		for _, stream := range client.tcp {
			streams = append(streams, stream)
		}
		for _, stream := range client.udp {
			streams = append(streams, stream)
		}
		client.mu.Unlock()
		for _, answer := range pending {
			answer <- response{err: err}
		}
		for _, stream := range streams {
			stream.remoteClose(err)
		}
		close(client.done)
		go client.closeResources()
	})
}

func (client *Client) closeResources() {
	client.closeOnce.Do(func() {
		transportErr := client.transport.Close()
		var cleanupErr error
		if client.cleanup != nil {
			cleanupErr = client.cleanup()
		}
		client.closeErr = errors.Join(transportErr, cleanupErr)
		close(client.closeDone)
	})
	<-client.closeDone
}

func (client *Client) Close() error {
	client.fail(errClientClosed)
	client.closeResources()
	return client.closeErr
}

func boundedContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if deadline, exists := parent.Deadline(); exists && time.Until(deadline) <= maximum {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, maximum)
}

func writeFull(writer io.Writer, value []byte) error {
	for len(value) != 0 {
		count, err := writer.Write(value)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		value = value[count:]
	}
	return nil
}
