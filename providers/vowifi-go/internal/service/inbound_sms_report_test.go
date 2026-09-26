// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/messaging"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

type smsReportPeer func(context.Context, voiceclient.SIPRequestMessage) (voiceclient.SIPResponse, error)

func (peer smsReportPeer) RoundTripRequest(ctx context.Context, request voiceclient.SIPRequestMessage) (voiceclient.SIPResponse, error) {
	return peer(ctx, request)
}

func (peer smsReportPeer) WriteRequest(context.Context, voiceclient.SIPRequestMessage) error {
	return errors.New("report requires a SIP response")
}

func incomingSMSFixture(t *testing.T, id string, mr byte) voiceclient.SIPIncomingRequest {
	t.Helper()
	// Upstream's SMS-DELIVER fixture includes a real SCTS and GSM-7 "hello".
	tpdu, err := hex.DecodeString("0005810180F600006270502143650005E8329BFD06")
	if err != nil {
		t.Fatal(err)
	}
	return voiceclient.SIPIncomingRequest{Method: "MESSAGE", URI: "sip:user@ims.example", Headers: map[string][]string{
		"Via":  {"SIP/2.0/UDP 192.0.2.1:5060;branch=z9hG4bK-" + id},
		"From": {"<sip:origin@ims.example>;tag=remote"}, "To": {"<sip:user@ims.example>"},
		"Call-ID": {id}, "CSeq": {"1 MESSAGE"}, "Max-Forwards": {"70"},
		"P-Asserted-Identity": {"\"SMS gateway\" <sip:ipsmgw@ims.example>"},
		"Content-Type":        {messaging.IMS3GPPSMSContentType},
	}, Body: append([]byte{1, mr, 0, 0, byte(len(tpdu))}, tpdu...)}
}

func newSMSReportReceiver(t *testing.T, sink MessageSink, generation string) *inboundMessaging {
	t.Helper()
	tracker := newMessageTracker(sink, func(id string, kind providermessages.Kind) providermessages.Event {
		return providermessages.Event{SchemaVersion: providermessages.SchemaVersion, EventID: generation + ":" + id,
			LineID: "line-1", ProviderID: "provider-1", ProcessGeneration: generation, Kind: kind, ObservedAt: time.Now()}
	})
	inbound, err := newInboundMessaging(messaging.NewService("device-1", "234100000000001", tracker, nil), tracker, sink)
	if err != nil {
		t.Fatal(err)
	}
	inbound.server.Profile = voiceclient.IMSProfile{IMPU: "sip:user@ims.example", Domain: "ims.example"}
	inbound.server.Registration = voiceclient.RegistrationBinding{PublicIdentity: "sip:user@ims.example", ContactURI: "sip:user@192.0.2.2:5060"}
	inbound.server.ContactURI = inbound.server.Registration.ContactURI
	return inbound
}

func TestInboundSMSReportIsSeparateFromSIPResponse(t *testing.T) {
	for _, scenario := range []string{"success", "gateway_list", "gateway_repeated_headers", "queue_failure", "response_write_failure", "report_rejected", "missing_asserted_gateway", "ambiguous_gateway"} {
		t.Run(scenario, func(t *testing.T) {
			sink := &recoverableMessageSink{}
			if scenario == "queue_failure" {
				sink.err = errors.New("durable queue unavailable")
			}
			inbound := newSMSReportReceiver(t, sink, "generation-1")
			request := incomingSMSFixture(t, "inbound-one", 42)
			if scenario == "missing_asserted_gateway" {
				delete(request.Headers, "P-Asserted-Identity")
			}
			if scenario == "gateway_list" {
				request.Headers["P-Asserted-Identity"] = []string{"<tel:+441234567890>, \"SMS, gateway\" <sip:ipsmgw@ims.example>"}
			}
			if scenario == "gateway_repeated_headers" {
				request.Headers["P-Asserted-Identity"] = []string{"<tel:+441234567890>", "<sip:ipsmgw@ims.example>"}
			}
			if scenario == "ambiguous_gateway" {
				request.Headers["P-Asserted-Identity"] = []string{"<sip:ipsmgw@ims.example>, <sip:another@ims.example>"}
			}
			success := scenario == "success" || scenario == "gateway_list" || scenario == "gateway_repeated_headers"
			responseWritten, sends, status := false, 0, 0
			inbound.server.CarrierTransport = smsReportPeer(func(ctx context.Context, report voiceclient.SIPRequestMessage) (voiceclient.SIPResponse, error) {
				sends++
				if !responseWritten || len(sink.events) != 1 {
					t.Fatal("report sent before durable ingress and SIP response")
				}
				if _, bounded := ctx.Deadline(); !bounded {
					t.Error("report request has no deadline")
				}
				if report.Method != "MESSAGE" || report.URI != "sip:ipsmgw@ims.example" ||
					report.Headers["In-Reply-To"] != "inbound-one" || report.Headers["Content-Type"] != messaging.IMS3GPPSMSContentType ||
					report.Headers["Call-ID"] == "inbound-one" || !bytes.Equal(report.Body, messaging.BuildSMSRPAck(42)) {
					t.Errorf("invalid separate report: %+v", report)
				}
				if scenario == "report_rejected" {
					return voiceclient.SIPResponse{StatusCode: 503, Reason: "Unavailable"}, nil
				}
				return voiceclient.SIPResponse{StatusCode: 202, Reason: "Accepted"}, nil
			})
			err := inbound.HandleSIPIncomingStreaming(t.Context(), request, func(response voiceclient.SIPIncomingResponse) error {
				status = response.StatusCode
				if len(response.Body) != 0 {
					t.Error("RP report incorrectly embedded in SIP response")
				}
				if scenario == "response_write_failure" {
					return errors.New("SIP response write failed")
				}
				responseWritten = true
				return nil
			})
			wantSends := 0
			if success || scenario == "report_rejected" {
				wantSends = 1
			}
			if sends != wantSends {
				t.Errorf("report requests=%d want=%d", sends, wantSends)
			}
			if success && (err != nil || status != 200) {
				t.Errorf("successful report: status=%d error=%v", status, err)
			}
			if !success && err == nil {
				t.Error("request failure was lost")
			}
			if scenario == "queue_failure" && (status != 500 || len(sink.events) != 0) {
				t.Error("unpersisted SMS was acknowledged")
			}
		})
	}
}

func TestInboundSMSBufferedTransportCannotPretendReportWasSent(t *testing.T) {
	inbound := newSMSReportReceiver(t, &recoverableMessageSink{}, "generation-1")
	responses := inbound.HandleSIPIncoming(t.Context(), incomingSMSFixture(t, "buffered", 42))
	if len(responses) != 1 || responses[0].StatusCode != 503 || len(responses[0].Body) != 0 {
		t.Fatalf("buffered transport silently accepted an unsent report: %+v", responses)
	}
}

type notificationStoreSink struct{ store *providermessages.Store }

func (sink notificationStoreSink) Publish(event providermessages.Event) error {
	_, _, err := sink.store.AcceptWithNotification(event, "8944100000000000001", time.Now())
	return err
}

func TestInboundSMSRedeliveryKeepsOneDurableNotification(t *testing.T) {
	store, err := providermessages.OpenStore(filepath.Join(t.TempDir(), "messages.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sink := notificationStoreSink{store}
	for attempt := 0; attempt < 3; attempt++ {
		inbound := newSMSReportReceiver(t, sink, fmt.Sprintf("generation-%d", attempt))
		request := incomingSMSFixture(t, fmt.Sprintf("carrier-redelivery-%d", attempt), byte(40+attempt))
		if attempt == 2 {
			// Same sender/body but a different service-centre timestamp is another SMS.
			request.Body[5+13] = 0x75
		}
		if _, err := inbound.server.HandleRequest(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		notifications, err := store.PendingNotificationSources(10)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if attempt == 2 {
			want = 2
		}
		if len(notifications) != want {
			t.Fatalf("attempt %d: notifications=%d want=%d", attempt, len(notifications), want)
		}
	}
}
