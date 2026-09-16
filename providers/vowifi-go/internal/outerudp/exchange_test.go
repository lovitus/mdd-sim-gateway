// SPDX-License-Identifier: AGPL-3.0-only

package outerudp

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
)

type exchangeResult struct {
	packet []byte
	err    error
}

func beginTestExchange(transport *Transport, ctx context.Context, id uint32) <-chan exchangeResult {
	done := make(chan exchangeResult, 1)
	go func() {
		packet, err := transport.ExchangeIKE(ctx, testIKERequest(id))
		done <- exchangeResult{packet, err}
	}()
	return done
}

func awaitTestExchange(t *testing.T, done <-chan exchangeResult) exchangeResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("exchange did not return within test bound")
		return exchangeResult{}
	}
}

func boundTestTransport(t *testing.T, timeout time.Duration) (*Transport, *datagramConn) {
	t.Helper()
	conn := newDatagramConn()
	transport, err := New(Config{DialContext: func(context.Context, string, string, time.Duration) (net.Conn, error) { return conn, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close(context.Background()) })
	if err := transport.Bind("192.0.2.10:4500", timeout); err != nil {
		t.Fatal(err)
	}
	return transport, conn
}

func finishTestInit(t *testing.T, transport *Transport, conn *datagramConn) {
	t.Helper()
	done := beginTestExchange(transport, t.Context(), 0)
	receiveDatagram(t, conn.outbound)
	conn.inbound <- append([]byte{0, 0, 0, 0}, testIKEResponse(0)...)
	if result := awaitTestExchange(t, done); result.err != nil {
		t.Fatal(result.err)
	}
}

func TestPersistentExchangeIgnoresLateAndUnrelatedIKE(t *testing.T) {
	transport, conn := boundTestTransport(t, time.Second)
	finishTestInit(t, transport, conn)
	first := beginTestExchange(transport, t.Context(), 1)
	receiveDatagram(t, conn.outbound)
	conn.inbound <- append([]byte{0, 0, 0, 0}, testIKEResponse(1)...)
	if result := awaitTestExchange(t, first); result.err != nil {
		t.Fatal(result.err)
	}
	second := beginTestExchange(transport, t.Context(), 2)
	receiveDatagram(t, conn.outbound)
	// The duplicate arrives AFTER the next request was written: pre-send
	// queue draining cannot protect this interleaving on a persistent socket.
	conn.inbound <- append([]byte{0, 0, 0, 0}, testIKEResponse(1)...)
	mutations := []func(*ikev2.Header){
		func(h *ikev2.Header) { h.InitiatorSPI++ },
		func(h *ikev2.Header) { h.ResponderSPI++ },
		func(h *ikev2.Header) { h.ExchangeType = ikev2.ExchangeINFORMATIONAL },
		func(h *ikev2.Header) { h.Flags &^= ikev2.FlagResponse },
		func(h *ikev2.Header) { h.Flags |= ikev2.FlagInitiator },
	}
	for _, mutate := range mutations {
		header := testIKEHeader(2, true)
		mutate(&header)
		packet, err := header.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		conn.inbound <- append([]byte{0, 0, 0, 0}, packet...)
	}
	conn.inbound <- []byte{0, 0, 0, 0, 1, 2, 3}
	want := testIKEResponse(2)
	conn.inbound <- append([]byte{0, 0, 0, 0}, want...)
	result := awaitTestExchange(t, second)
	if result.err != nil || !bytes.Equal(result.packet, want) {
		t.Fatalf("wrong reply consumed exchange: %x err=%v", result.packet, result.err)
	}
	if stats := transport.IKEStats(); stats.RequestsSent != 3 || stats.ResponseDatagrams != 10 {
		t.Fatalf("transport evidence=%+v", stats)
	}
}

func TestInitialSelectionWaitsForMatchingIKEAndAllowsZeroResponderSPI(t *testing.T) {
	transport, conn := boundTestTransport(t, time.Second)
	done := beginTestExchange(transport, t.Context(), 0)
	receiveDatagram(t, conn.outbound)
	conn.inbound <- []byte{0xff}
	conn.inbound <- []byte{1, 2, 3, 4, 5, 6, 7, 8} // ESP must not select an ePDG.
	conn.inbound <- append([]byte{0, 0, 0, 0}, testIKEResponse(1)...)
	header := testIKEHeader(0, true)
	header.ResponderSPI = 0
	want, err := (ikev2.Message{Header: header, Payloads: []ikev2.Payload{
		ikev2.NotifyWithZeroSPI(ikev2.NotifyInvalidKEPayload, []byte{0, 14}),
	}}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	conn.inbound <- want // Existing unmarked-IKE compatibility stays available.
	result := awaitTestExchange(t, done)
	if result.err != nil || !bytes.Equal(result.packet, want) {
		t.Fatalf("INIT reply=%x err=%v", result.packet, result.err)
	}
	if transport.SelectedRemote() != "192.0.2.10:4500" {
		t.Fatal("matching responder not pinned")
	}
}

func TestExchangeHonorsConfiguredTimeoutAfterConnectionEstablished(t *testing.T) {
	transport, conn := boundTestTransport(t, time.Second)
	finishTestInit(t, transport, conn)
	if err := transport.Bind("192.0.2.10:4500", 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	done := beginTestExchange(transport, context.Background(), 1)
	receiveDatagram(t, conn.outbound)
	conn.inbound <- append([]byte{0, 0, 0, 0}, testIKEResponse(0)...)
	if result := awaitTestExchange(t, done); !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("unrelated response bypassed timeout: %v", result.err)
	}
	if stats := transport.IKEStats(); stats.ResponseTimeouts != 1 {
		t.Fatalf("timeout evidence=%+v", stats)
	}
}

func TestInitialExchangeCancellationClosesOnlyPendingSocket(t *testing.T) {
	transport, conn := boundTestTransport(t, time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := beginTestExchange(transport, ctx, 0)
	receiveDatagram(t, conn.outbound)
	cancel()
	if result := awaitTestExchange(t, done); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancel result=%v", result.err)
	}
	if transport.SelectedRemote() != "" || conn.closeCount.Load() != 1 {
		t.Fatal("cancelled pending socket was retained")
	}
}

func TestExchangeRejectsMalformedRequestBeforeDial(t *testing.T) {
	transport, conn := boundTestTransport(t, time.Second)
	for _, packet := range [][]byte{nil, []byte("not IKE"), testIKEResponse(0), append(testIKERequest(0), 0)} {
		if _, err := transport.ExchangeIKE(t.Context(), packet); err == nil {
			t.Fatal("invalid request was accepted")
		}
	}
	if len(conn.outbound) != 0 || transport.LocalNetworkAddr() != nil {
		t.Fatal("malformed request reached network")
	}
}
