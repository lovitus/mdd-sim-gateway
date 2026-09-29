// SPDX-License-Identifier: Apache-2.0

// This file follows gVisor's gonet.DialTCPWithBind and ListenTCP. The gVisor Authors retain
// copyright in the upstream implementation.
package wgnetstack

import (
	"context"
	"errors"
	"fmt"
	"net"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// A SIP Contact can use the same local port as an established outbound flow.
// Like the caller-bound dial above, reuse-address permits distinct TCP tuples;
// reuse-port is not enabled, so a second listener cannot take the same endpoint.
func listenTCPWithBindReuse(tcpStack *stack.Stack, local tcpip.FullAddress, protocol tcpip.NetworkProtocolNumber) (*gonet.TCPListener, error) {
	var queue waiter.Queue
	endpoint, err := tcpStack.NewEndpoint(tcp.ProtocolNumber, protocol, &queue)
	if err != nil {
		return nil, errors.New(err.String())
	}
	endpoint.SocketOptions().SetReuseAddress(true)
	if err := endpoint.Bind(local); err != nil {
		endpoint.Close()
		return nil, fmt.Errorf("bind incoming TCP: %s", err)
	}
	if err := endpoint.Listen(4096); err != nil {
		endpoint.Close()
		return nil, fmt.Errorf("listen incoming TCP: %s", err)
	}
	return gonet.NewTCPListener(tcpStack, &queue, endpoint), nil
}

// dialTCPWithBindReuse matches gonet.DialTCPWithBind except that a
// caller-selected local port is reusable across distinct remote tuples. IMS
// registration resolves multiple P-CSCF candidates and must keep its source
// port while failing over between them. SO_REUSEADDR retains the normal TCP
// four-tuple uniqueness rule; it does not permit duplicate active connections.
func dialTCPWithBindReuse(ctx context.Context, tcpStack *stack.Stack, local, remote tcpip.FullAddress, protocol tcpip.NetworkProtocolNumber) (*gonet.TCPConn, error) {
	var queue waiter.Queue
	endpoint, err := tcpStack.NewEndpoint(tcp.ProtocolNumber, protocol, &queue)
	if err != nil {
		return nil, errors.New(err.String())
	}
	closeEndpoint := true
	defer func() {
		if closeEndpoint {
			endpoint.Close()
		}
	}()

	entry, ready := waiter.NewChannelEntry(waiter.WritableEvents)
	queue.EventRegister(&entry)
	defer queue.EventUnregister(&entry)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	endpoint.SocketOptions().SetReuseAddress(true)
	// Caller-bound TCP sockets are used by IMS Security-Agree, whose negotiated
	// client port must be reusable immediately when a security generation is
	// replaced. Abortive close avoids retaining that exact four-tuple in
	// TIME-WAIT; ordinary (non-bound) TCP and UDP dial paths are unaffected.
	endpoint.SocketOptions().SetLinger(tcpip.LingerOption{Enabled: true})
	if err := endpoint.Bind(local); err != nil {
		return nil, fmt.Errorf("ep.Bind(%+v) = %s", local, err)
	}
	connectErr := endpoint.Connect(remote)
	if _, started := connectErr.(*tcpip.ErrConnectStarted); started {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ready:
		}
		connectErr = endpoint.LastError()
	}
	if connectErr != nil {
		return nil, &net.OpError{
			Op:   "connect",
			Net:  "tcp",
			Addr: &net.TCPAddr{IP: net.IP(remote.Addr.AsSlice()), Port: int(remote.Port)},
			Err:  errors.New(connectErr.String()),
		}
	}

	closeEndpoint = false
	return gonet.NewTCPConn(&queue, endpoint), nil
}
