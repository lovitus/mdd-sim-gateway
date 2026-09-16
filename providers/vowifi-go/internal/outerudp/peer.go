// SPDX-License-Identifier: AGPL-3.0-only

package outerudp

import (
	"context"
	"errors"
	"time"
)

// PeerReply is produced only after the session owner authenticates a request.
// A terminal peer action is applied after its encrypted reply is sent.
type PeerReply struct {
	Packet    []byte
	AfterSend func()
	Abort     error
}

func (transport *Transport) ServePeerIKE(handler func([]byte) (PeerReply, error)) error {
	if handler == nil {
		return errors.New("peer IKE handler is required")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return transport.errLocked()
	}
	if transport.peerHandlerSet {
		return errors.New("peer IKE handler already installed")
	}
	transport.peerHandlerSet = true
	go func() {
		for {
			select {
			case <-transport.done:
				return
			case packet := <-transport.peerIKE:
				reply, err := handler(packet)
				if err != nil || len(reply.Packet) == 0 {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				wire := append([]byte{0, 0, 0, 0}, reply.Packet...)
				err = transport.write(ctx, wire)
				cancel()
				if err != nil {
					transport.fail(errors.Join(reply.Abort, err))
					return
				}
				if reply.AfterSend != nil {
					reply.AfterSend()
				}
				if reply.Abort != nil {
					transport.fail(reply.Abort)
					return
				}
			}
		}
	}()
	return nil
}
