package runtimehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
)

type refreshOwnerTransport func(context.Context, voiceclient.RegisterMessage) (voiceclient.RegisterResponse, error)

func (f refreshOwnerTransport) RoundTripRegister(ctx context.Context, msg voiceclient.RegisterMessage) (voiceclient.RegisterResponse, error) {
	return f(ctx, msg)
}

func TestLivenessReviewRefreshRemainsOwnedAcrossLeaseExpiry(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cancellation"}[cancelRequest], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			now := time.Now()
			m := &imsRegistrationMaintenance{registered: true, registeredAt: now,
				binding: voiceclient.RegistrationBinding{ContactURI: "sip:user@127.0.0.1:5060", Expires: 1}}
			m.session = voiceclient.RegisterSession{
				Profile:      voiceclient.IMSProfile{IMPI: "user@ims.example", IMPU: "sip:user@ims.example", Domain: "ims.example"},
				RegistrarURI: "sip:ims.example", ContactURI: m.binding.ContactURI,
				Transport: refreshOwnerTransport(func(ctx context.Context, _ voiceclient.RegisterMessage) (voiceclient.RegisterResponse, error) {
					close(entered)
					select {
					case <-ctx.Done():
						return voiceclient.RegisterResponse{}, ctx.Err()
					case <-release:
						return voiceclient.RegisterResponse{StatusCode: 200, Reason: "OK", Headers: map[string][]string{
							"Contact": {"<sip:user@127.0.0.1:5060>;expires=60"},
						}}, nil
					}
				}),
			}
			done := make(chan error, 1)
			go func() { done <- m.refresh(ctx) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("refresh did not reach the transport")
			}
			during := m.resultAt("pending refresh", now.Add(2*time.Second))
			if during.Registered || !during.RecoveryState.InProgress {
				t.Fatal("expired registration lost its in-flight refresh owner")
			}
			if _, err := m.Recover(ctx); !errors.Is(err, ErrIMSRegistrationInProgress) {
				t.Fatalf("manual registration overlapped refresh: %v", err)
			}
			if cancelRequest {
				cancel()
			} else {
				close(release)
			}
			if err := <-done; cancelRequest && !errors.Is(err, context.Canceled) || !cancelRequest && err != nil {
				t.Fatal(err)
			}
			if after := m.result("finished"); after.RecoveryState.InProgress || !cancelRequest && !after.Registered {
				t.Fatal("completed refresh retained ownership or failed to publish success")
			}
		})
	}
}
