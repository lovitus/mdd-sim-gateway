package voicehost

import (
	"context"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"testing"
)

type byeDuringACK struct {
	*fakeIMSVoiceTransport
	agent *IMSOutboundAgent
	t     *testing.T
}

func (t *byeDuringACK) WriteRequest(ctx context.Context, msg voiceclient.SIPRequestMessage) error {
	if msg.Method == "ACK" {
		response, handled := t.agent.HandleRemoteBye(voiceclient.SIPIncomingRequest{Method: "BYE", Headers: map[string][]string{"Call-ID": {msg.Headers["Call-ID"]}, "From": {msg.Headers["To"]}, "To": {msg.Headers["From"]}, "CSeq": {"77 BYE"}}})
		if !handled || response.StatusCode != 200 {
			t.t.Fatalf("BYE racing ACK was rejected: %+v handled=%v", response, handled)
		}
	}
	return t.fakeIMSVoiceTransport.WriteRequest(ctx, msg)
}

func TestRemoteByeDuringFinalACKCannotResurrectDialog(t *testing.T) {
	transport := &byeDuringACK{t: t, fakeIMSVoiceTransport: &fakeIMSVoiceTransport{responses: []voiceclient.SIPResponse{{StatusCode: 200, Reason: "OK", Headers: map[string][]string{"To": {"<sip:peer@ims.example>;tag=remote"}, "Contact": {"<sip:peer@192.0.2.20:5060>"}}, Body: []byte(sampleAMRSDP("192.0.2.20", 49170))}}}}
	agent := &IMSOutboundAgent{Transport: transport, Profile: voiceclient.IMSProfile{IMPI: "impi@example", IMPU: "sip:self@ims.example", Domain: "ims.example"}, Registration: voiceclient.RegistrationBinding{ContactURI: "sip:self@192.0.2.10:5060", PublicIdentity: "sip:self@ims.example"}, LocalTag: "local"}
	transport.agent = agent
	ended := agent.WatchRemoteEnd("ack-race")
	result, err := agent.StartOutboundCall(t.Context(), OutboundCallRequest{DeviceID: "dev-1", CallID: "ack-race", Callee: "+18005551212", RawSDP: []byte(sampleAMRSDP("192.0.2.10", 4002)), RemoteSDP: SDPInfo{ConnectionIP: "192.0.2.10", MediaPort: 4002}})
	if err != nil || !result.Accepted {
		t.Fatalf("accepted call result=%+v err=%v", result, err)
	}
	select {
	case <-ended:
	default:
		t.Fatal("remote termination was lost")
	}
	if _, present := agent.dialog("ack-race"); present {
		t.Fatal("post-ACK processing resurrected the dialog")
	}
}

func TestOutboundRemoteByeRequiresExactDialogTags(t *testing.T) {
	agent := &IMSOutboundAgent{dialogs: map[string]imsDialogState{"call-1": {cfg: voiceclient.DialogRequestConfig{CallID: "call-1", LocalTag: "local", RemoteTag: "remote"}}}}
	ended := agent.WatchRemoteEnd("call-1")
	request := voiceclient.SIPIncomingRequest{Method: "BYE", Headers: map[string][]string{"Call-ID": {"call-1"}, "From": {"<sip:peer@example>;tag=remote"}, "To": {"<sip:self@example>;tag=wrong"}, "CSeq": {"4 BYE"}}}
	response, handled := agent.HandleRemoteBye(request)
	if !handled || response.StatusCode != 481 {
		t.Fatalf("spoofed BYE: %+v %v", response, handled)
	}
	select {
	case <-ended:
		t.Fatal("wrong tag ended call")
	default:
	}
	request.Headers["To"] = []string{"<sip:self@example>;tag=local"}
	response, handled = agent.HandleRemoteBye(request)
	if !handled || response.StatusCode != 200 {
		t.Fatalf("valid BYE: %+v %v", response, handled)
	}
	select {
	case <-ended:
	default:
		t.Fatal("exact remote end was not observable")
	}
	if _, found := agent.dialog("call-1"); found {
		t.Fatal("ended dialog retained")
	}
}
