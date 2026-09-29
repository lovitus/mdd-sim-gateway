package voiceclient

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
)

// The peer may open its own TCP flow to the Contact server port (TS 33.203
// section 7.1). Dialing the registrar does not create that listening socket.
func (f *WireSIPFlow) listenIncomingLocked() error {
	if f.ListenContext == nil || f.IncomingHandler == nil || !strings.HasPrefix(f.network, "tcp") {
		return nil
	}
	address := f.incomingAddress
	if address == "" {
		address = f.conn.LocalAddr().String()
	}
	peerHost, _, err := net.SplitHostPort(f.conn.RemoteAddr().String())
	if err != nil {
		return err
	}
	listener, err := f.ListenContext(f.connContext, f.network, address)
	if err != nil {
		return err
	}
	f.incomingListener = listener
	if f.incomingConns == nil {
		f.incomingConns = make(map[net.Conn]struct{})
	}
	ctx := f.connContext
	peerPort := f.incomingRemotePort
	go f.acceptIncoming(ctx, listener, peerHost, peerPort)
	return nil
}

func (f *WireSIPFlow) acceptIncoming(ctx context.Context, listener net.Listener, peerHost, peerPort string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		host, port, err := net.SplitHostPort(conn.RemoteAddr().String())
		if err != nil || !net.ParseIP(host).Equal(net.ParseIP(peerHost)) || (peerPort != "" && port != peerPort) {
			_ = conn.Close()
			continue
		}
		f.mu.Lock()
		if f.incomingListener != listener || ctx.Err() != nil || len(f.incomingConns) >= 2 {
			f.mu.Unlock()
			_ = conn.Close()
			continue
		}
		f.incomingConns[conn] = struct{}{}
		f.mu.Unlock()
		go f.servePeerConnection(ctx, listener, conn)
	}
}

func (f *WireSIPFlow) servePeerConnection(parent context.Context, listener net.Listener, conn net.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer func() {
		cancel()
		_ = conn.Close()
		f.mu.Lock()
		delete(f.incomingConns, conn)
		f.mu.Unlock()
	}()
	reader := bufio.NewReader(conn)
	for ctx.Err() == nil {
		raw, err := readSIPStreamMessage(reader)
		if err != nil {
			return
		}
		f.mu.Lock()
		if f.incomingListener != listener || ctx.Err() != nil {
			err = errors.New("incoming SIP flow replaced")
		} else {
			_, err = f.handleIncomingWireLocked(ctx, conn, raw)
		}
		f.mu.Unlock()
		if err != nil {
			return
		}
	}
}
