package swu

import (
	"context"
	"errors"
	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
	"testing"
	"time"
)

// This also runs unmodified against the pre-fix commit. A failed synchronous
// probe must not consume the budget a second time at its scheduled deadline.

func TestLivenessReviewBusyControlDoesNotCountAsDeadPeer(t *testing.T) {
	now := time.Now()
	state, _ := NewIKELivenessState(IKELivenessConfig{DisableKeepalive: true, DPDInterval: time.Second, DPDTimeout: time.Second, MaxMissedDPDProbes: 2}, now)
	session, err := NewPacketSession(PacketSessionConfig{ChildSA: packetChildSA(true), Transport: &captureESPPacketTransport{}, Liveness: state, DPDHandler: func(context.Context) error { return ErrIKEControlBusy }})
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 5; n++ {
		if _, err := session.AdvanceIKELiveness(t.Context(), now.Add(time.Duration(n)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if snapshot := session.IKELivenessSnapshot(); snapshot.Dead || snapshot.MissedDPDProbes != 0 || snapshot.OutstandingDPD {
			t.Fatalf("unsent probe was treated as failed: %+v", snapshot)
		}
	}
}

func TestLivenessReviewProbeDeadlineAndCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		now := time.Now()
		state, _ := NewIKELivenessState(IKELivenessConfig{DisableKeepalive: true, DPDInterval: time.Second, DPDTimeout: 15 * time.Millisecond, MaxMissedDPDProbes: 3}, now)
		ctx, cancel := context.WithCancel(t.Context())
		session, err := NewPacketSession(PacketSessionConfig{ChildSA: packetChildSA(true), Transport: &captureESPPacketTransport{}, Liveness: state, DPDHandler: func(ctx context.Context) error {
			if cancelParent {
				cancel()
			}
			<-ctx.Done()
			return ctx.Err()
		}})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := session.AdvanceIKELiveness(ctx, now.Add(time.Second)); done <- err }()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			cancel()
			t.Fatal("probe ignored bounded deadline")
		}
		cancel()
		want := 1
		if cancelParent {
			want = 0
		}
		if got := session.IKELivenessSnapshot().MissedDPDProbes; got != want {
			t.Fatalf("parent canceled=%v missed=%d want=%d", cancelParent, got, want)
		}
	}
}

func TestLivenessReviewAuthenticatedDPDOnly(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		init := ikeControlInit(t)
		control := &ikePacketTunnelControl{init: init, keys: init.Keys, nextMessageID: 10}
		control.transport = mddIKEExchangeFunc(func(_ context.Context, request []byte) ([]byte, error) {
			header, err := ikev2.ParseHeader(request)
			if err != nil {
				return nil, err
			}
			if _, _, err := ikev2.ParseInformationalRequest(request, init, init.Keys, header.MessageID); err != nil {
				return nil, err
			}
			_, wire, err := ikev2.BuildInformationalResponse(init, init.Keys, header.MessageID, nil, nil)
			if tamper && len(wire) > 0 {
				wire[len(wire)-1] ^= 1
			}
			return wire, err
		})
		now := time.Now()
		state, _ := NewIKELivenessState(IKELivenessConfig{DisableKeepalive: true, DPDInterval: time.Second, DPDTimeout: time.Second, MaxMissedDPDProbes: 3}, now)
		session, err := NewPacketSession(PacketSessionConfig{ChildSA: packetChildSA(true), Transport: &captureESPPacketTransport{}, Liveness: state, DPDHandler: control.dpd})
		if err != nil {
			t.Fatal(err)
		}
		_, err = session.AdvanceIKELiveness(t.Context(), now.Add(time.Second))
		snapshot := session.IKELivenessSnapshot()
		if tamper {
			if err == nil || !snapshot.LastDPDSuccess.IsZero() || snapshot.MissedDPDProbes != 1 {
				t.Fatalf("forged response counted as life: %+v %v", snapshot, err)
			}
		} else if err != nil || snapshot.LastDPDSuccess.IsZero() || snapshot.MissedDPDProbes != 0 {
			t.Fatalf("authenticated response rejected: %+v %v", snapshot, err)
		}
	}
}
