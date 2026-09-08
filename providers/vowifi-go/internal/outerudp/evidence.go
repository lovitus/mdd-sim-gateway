// SPDX-License-Identifier: AGPL-3.0-only

package outerudp

import (
	"context"
	"errors"
	"net"
)

// These count actual transport operations, not authenticated IKE results or
// retransmissions. Trying a different ePDG address is another request.
type IKEExchangeStats struct {
	RequestsSent      uint64
	ResponseDatagrams uint64
	ResponseTimeouts  uint64
}

func (transport *Transport) IKEStats() IKEExchangeStats {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.ikeStats
}

func (transport *Transport) recordIKERequest() {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.ikeStats.RequestsSent++
}

func (transport *Transport) recordIKEResult(err error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	var networkError net.Error
	switch {
	case err == nil:
		transport.ikeStats.ResponseDatagrams++
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout():
		transport.ikeStats.ResponseTimeouts++
	}
}
