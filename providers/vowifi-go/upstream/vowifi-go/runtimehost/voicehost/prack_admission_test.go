package voicehost

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
)

// The emitter has made 183 visible to its peer, but has not returned yet.
// This is possible on the real streaming and packet response paths. No sleeps
// or scheduler assumptions are needed to reproduce the admission ordering.
func TestPRACKAdmissionBeforeProvisionalEmissionReturns(t *testing.T) {
	transport := newReliableProvisionalInboundTransport([]voiceclient.SIPResponse{{
		StatusCode: 183, Reason: "Session Progress",
		Headers: map[string][]string{"To": {"<sip:user@ims.example>;tag=early-tag"}, "Contact": {"<sip:client@192.0.2.70:5060>"}, "Require": {"100rel"}, "RSeq": {"42"}},
		Body:    []byte(sampleSDP("127.0.0.1", 4002)),
	}})
	server := &IMSInboundWireServer{Agent: &IMSInboundAgent{ClientTransport: transport,
		ClientContactURI: "sip:client@127.0.0.1:5070", LocalContactURI: "sip:vowifi@127.0.0.1:5060"},
		LocalTag: "ue-tag", ContactURI: "sip:vowifi@127.0.0.1:5060", TransactionTTL: time.Second}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	emitted, release, inviteDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() {
		unblock()
		cancel()
		select {
		case <-inviteDone:
		case <-time.After(time.Second):
			t.Error("INVITE worker did not finish")
		}
	}()
	invite, err := voiceclient.ParseSIPRequest(wireIMSInvite("prack-admission", "INVITE", 1, []byte(sampleSDP("203.0.113.10", 49170))))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		inviteDone <- server.HandleRequestStreaming(ctx, invite, func(r IMSInboundWireResponse) error {
			if r.StatusCode == 183 {
				close(emitted)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()
	select {
	case <-emitted:
	case <-ctx.Done():
		t.Fatal("183 was not emitted")
	}
	// Preserve RAck identity checks: a different RSeq is never admitted.
	wrong, err := voiceclient.ParseSIPRequest(wireIMSRequest("prack-admission", "PRACK", 2, nil, "RAck: 41 1 INVITE\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	responses, err := server.HandleRequest(ctx, wrong)
	if err != nil || len(responses) != 1 || responses[0].StatusCode != 481 {
		t.Fatalf("unrelated RAck accepted: %+v %v", responses, err)
	}
	request, err := voiceclient.ParseSIPRequest(wireIMSRequest("prack-admission", "PRACK", 3, nil, "RAck: 42 1 INVITE\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	transport.respondRequest(voiceclient.SIPResponse{StatusCode: 200, Reason: "OK"})
	responses, err = server.HandleRequest(ctx, request)
	if err != nil || len(responses) != 1 || responses[0].StatusCode != 200 {
		t.Fatalf("PRACK raced provisional publication: responses=%+v err=%v", responses, err)
	}
	select {
	case forwarded := <-transport.requests:
		if forwarded.Method != "PRACK" || forwarded.Headers["RAck"] != "42 1 INVITE" {
			t.Fatalf("wrong forwarded PRACK: %+v", forwarded)
		}
	default:
		t.Fatal("accepted PRACK was not forwarded")
	}
	unblock()
	select {
	case <-transport.provisionalsDone:
	case <-ctx.Done():
		t.Fatal("emitter did not finish")
	}
	if server.hasPendingReliableProvisionalForPrack(request) {
		t.Fatal("emitter completion resurrected acknowledged provisional")
	}
	duplicate, err := server.HandleRequest(ctx, request)
	if err != nil || len(duplicate) != 1 || duplicate[0].StatusCode != 200 {
		t.Fatalf("duplicate lost cached response: %+v %v", duplicate, err)
	}
	select {
	case <-transport.requests:
		t.Fatal("duplicate PRACK forwarded twice")
	default:
	}
}
