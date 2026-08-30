// SPDX-License-Identifier: Apache-2.0

// This file follows gVisor's gonet.DialTCPWithBind. The gVisor Authors retain
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
