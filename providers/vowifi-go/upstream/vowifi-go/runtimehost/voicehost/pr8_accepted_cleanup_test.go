package voicehost

import (
	"context"
	"errors"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"testing"
)

type pr8AckFault struct {
	*fakeIMSVoiceTransport
	fail bool
}

func (s *pr8AckFault) WriteRequest(ctx context.Context, q voiceclient.SIPRequestMessage) error {
	if s.fail && q.Method == "ACK" {
		return errors.New("injected ACK write failure")
	}
	return s.fakeIMSVoiceTransport.WriteRequest(ctx, q)
}
func TestPR8AcceptedAckOrSDPFailureKeepsDialogForBye(t *testing.T) {
	for _, name := range []string{"ack", "sdp"} {
		t.Run(name, func(t *testing.T) {
			body := []byte(sampleAMRSDP("203.0.113.10", 49170))
			if name == "sdp" {
				body = []byte("invalid")
			}
			transport := &pr8AckFault{fakeIMSVoiceTransport: &fakeIMSVoiceTransport{responses: []voiceclient.SIPResponse{{StatusCode: 200, Headers: map[string][]string{"To": {"<sip:peer@ims.test>;tag=remote"}, "Contact": {"<sip:peer@192.0.2.2:5060>"}}, Body: body}, {StatusCode: 200}}}, fail: name == "ack"}
			a := &IMSOutboundAgent{Transport: transport, Profile: voiceclient.IMSProfile{IMPU: "sip:user@ims.test", Domain: "ims.test"}, Registration: voiceclient.RegistrationBinding{ContactURI: "sip:user@192.0.2.1:5060", PublicIdentity: "sip:user@ims.test"}}
			_, err := a.StartOutboundCall(t.Context(), OutboundCallRequest{CallID: "original", Callee: "+100", RawSDP: []byte(sampleAMRSDP("192.0.2.1", 4000))})
			if err == nil {
				t.Fatal("post-acceptance error hidden")
			}
			if _, found := a.dialog("original"); !found {
				t.Fatal("accepted dialog disappeared after local SDP failure")
			}
			result, err := a.EndVoiceCallWithResult(t.Context(), DialogInfo{CallID: "original"})
			if err != nil || !result.Accepted {
				t.Fatal(result, err)
			}
			requests := transport.requestSnapshot()
			if len(requests) != 2 || requests[0].Method != "INVITE" || requests[1].Method != "BYE" {
				t.Fatal("cleanup issued another INVITE", requests)
			}
		})
	}
}
