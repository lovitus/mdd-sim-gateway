package egressprobe

import (
	"context"
	"errors"
	"net"
	"time"
)

// ProbeDatagram reuses the exit probe's DNS packet and response validation,
// but the caller owns the exact remote data path. There is no host-network fallback.
func ProbeDatagram(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) (Result, error) {
	result := Result{Target: "1.1.1.1", AttemptedTargets: []string{"1.1.1.1"}}
	if ctx == nil || dial == nil {
		return result, errors.New("data probe dialer is required")
	}
	identifier, err := randomID(nil)
	if err != nil {
		return result, err
	}
	connection, err := dial(ctx, "udp", "1.1.1.1:53")
	if err != nil {
		return result, err
	}
	defer connection.Close()
	releaseDeadline := bindDeadline(ctx, connection)
	defer releaseDeadline()
	question := dnsQuestion("cloudflare.com")
	started := time.Now()
	if _, err := connection.Write(dnsQuery(identifier, question)); err != nil {
		return result, err
	}
	response := make([]byte, maximumPacketBytes)
	count, err := connection.Read(response)
	if err != nil {
		return result, err
	}
	if !validDNSAnswer(response[:count], identifier, question) {
		return result, errors.New("cellular data probe received an invalid DNS answer")
	}
	result.LatencyMS = max(1, int(time.Since(started).Milliseconds()))
	return result, nil
}
