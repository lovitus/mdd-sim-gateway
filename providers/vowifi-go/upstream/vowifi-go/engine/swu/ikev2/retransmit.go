package ikev2

import (
	"context"
	"errors"
	"net"
	"time"
)

var ErrExchangeUncertain = errors.New("IKE exchange remained unanswered after retransmission")

// RetransmitTransport preserves MDD's three bounded attempts without creating
// another nonce, IV or Message ID. A protocol rejection is a response, not a retry.
type RetransmitTransport struct {
	Transport      InitTransport
	AttemptTimeout time.Duration
}

func (transport RetransmitTransport) ExchangeIKE(ctx context.Context, request []byte) ([]byte, error) {
	if transport.Transport == nil {
		return nil, errors.New("missing IKE transport")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := transport.AttemptTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	wire := append([]byte(nil), request...)
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bounded, cancel := context.WithTimeout(ctx, timeout)
		response, err := transport.Transport.ExchangeIKE(bounded, append([]byte(nil), wire...))
		cancel()
		if err == nil {
			return response, nil
		}
		if ctx.Err() != nil {
			return nil, errors.Join(ErrExchangeUncertain, ctx.Err())
		}
		var network net.Error
		if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &network) && network.Timeout()) {
			return nil, errors.Join(ErrExchangeUncertain, err)
		}
		last = err
	}
	return nil, errors.Join(ErrExchangeUncertain, last)
}
