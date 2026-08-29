// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/messaging"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/ims"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

type MessageSink interface {
	Publish(providermessages.Event) error
}

type messageTracker struct {
	mu       sync.Mutex
	parts    map[string]messaging.DeliveryPartMatch
	status   map[string]messaging.DeliveryStatus
	failures map[string]error
	sink     MessageSink
	identity func(string, providermessages.Kind) providermessages.Event
}

func newMessageTracker(sink MessageSink, identity func(string, providermessages.Kind) providermessages.Event) *messageTracker {
	return &messageTracker{
		parts: make(map[string]messaging.DeliveryPartMatch), status: make(map[string]messaging.DeliveryStatus),
		failures: make(map[string]error), sink: sink, identity: identity,
	}
}

func (tracker *messageTracker) CreateSMSDelivery(messageID, imsi, deviceID, peer, content string, partsTotal int, at time.Time) error {
	tracker.mu.Lock()
	tracker.status[messageID] = messaging.DeliveryStatus{MessageID: messageID, IMSI: imsi, DeviceID: deviceID, Peer: peer, Content: content, PartsTotal: partsTotal, State: "pending", CreatedAt: at, UpdatedAt: at}
	tracker.mu.Unlock()
	return nil
}

func (tracker *messageTracker) UpsertSMSDeliveryPart(messageID string, partNo int, callID string, rpMR int, state string, sentAt time.Time) error {
	match := messaging.DeliveryPartMatch{MessageID: messageID, PartNo: partNo, State: state}
	tracker.mu.Lock()
	if strings.TrimSpace(callID) != "" {
		tracker.parts["call:"+strings.TrimSpace(callID)] = match
	}
	if rpMR > 0 {
		tracker.parts[fmt.Sprintf("mr:%d", rpMR)] = match
	}
	status := tracker.status[messageID]
	tracker.mu.Unlock()
	if tracker.sink == nil {
		return nil
	}
	event := tracker.identity(fmt.Sprintf("submitted:%s:%d", messageID, partNo), providermessages.KindSubmitted)
	event.MessageID, event.Part, event.Recipient, event.Body = messageID, partNo, status.Peer, status.Content
	event.CallID, event.RPMR, event.State, event.ObservedAt = strings.TrimSpace(callID), rpMR, strings.TrimSpace(state), sentAt
	err := tracker.sink.Publish(event)
	if err != nil {
		tracker.mu.Lock()
		if tracker.failures[messageID] == nil {
			tracker.failures[messageID] = err
		}
		tracker.mu.Unlock()
	}
	return err
}

func (tracker *messageTracker) takeFailure(messageID string) error {
	tracker.mu.Lock()
	err := tracker.failures[messageID]
	delete(tracker.failures, messageID)
	tracker.mu.Unlock()
	return err
}

func (tracker *messageTracker) MarkSMSDeliveryPartReport(inReplyTo, callID, _ string, rpMR int, state string, _ int, _ int, _ string, at time.Time) (messaging.DeliveryPartMatch, error) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for _, key := range []string{"call:" + strings.TrimSpace(inReplyTo), "call:" + strings.TrimSpace(callID), fmt.Sprintf("mr:%d", rpMR)} {
		if match, found := tracker.parts[key]; found && key != "call:" && key != "mr:0" {
			match.State = state
			return match, nil
		}
	}
	// Core retains the durable correlation map. A provider restart must not
	// reject an otherwise valid carrier report merely because this cache is cold.
	return messaging.DeliveryPartMatch{State: state}, nil
}

func (tracker *messageTracker) RecomputeSMSDelivery(string, time.Time) error { return nil }
func (tracker *messageTracker) UpdateSMSDeliveryState(messageID, state, lastError string, acks int, at time.Time) error {
	tracker.mu.Lock()
	status := tracker.status[messageID]
	status.State, status.LastError, status.Acks, status.UpdatedAt = state, lastError, acks, at
	tracker.status[messageID] = status
	tracker.mu.Unlock()
	return nil
}
func (tracker *messageTracker) GetSMSDeliveryStatus(messageID string) (*messaging.DeliveryStatus, error) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	status, found := tracker.status[messageID]
	if !found {
		return nil, messaging.ErrDeliveryNotFound
	}
	copy := status
	return &copy, nil
}

func (tracker *messageTracker) resolve(report *messaging.SMSDeliveryReport) messaging.DeliveryPartMatch {
	if report == nil {
		return messaging.DeliveryPartMatch{}
	}
	match, _ := tracker.MarkSMSDeliveryPartReport(report.InReplyTo, report.CallID, "", report.RPMR, report.State, report.SIPCode, report.RPCause, report.ErrorText, report.ReportAt)
	return match
}

type inboundMessaging struct {
	service *messaging.Service
	tracker *messageTracker
	sink    MessageSink
	server  *voicehost.IMSInboundWireServer
	calls   *ims.IncomingCallController
	cancel  context.CancelFunc
	done    chan error
	mu      sync.Mutex
	fault   error
	started bool
}

func (inbound *inboundMessaging) ConfigureVoice(stack *usernet.Stack, localIP, contactURI, localTag, userAgent string, profile voiceclient.IMSProfile, binding voiceclient.RegistrationBinding, carrier voiceclient.SIPRequestTransport) error {
	if inbound == nil || stack == nil || carrier == nil || strings.TrimSpace(binding.ContactURI) == "" {
		return errors.New("invalid inbound voice configuration")
	}
	controller, err := ims.NewIncomingCallController(stack, localIP, contactURI, localTag)
	if err != nil {
		return err
	}
	inbound.mu.Lock()
	defer inbound.mu.Unlock()
	if inbound.started || inbound.calls != nil {
		return errors.New("inbound voice is already configured")
	}
	inbound.server.Agent = &voicehost.IMSInboundAgent{
		ClientTransport: controller, Profile: profile, Registration: binding,
		ClientContactURI: contactURI, LocalContactURI: binding.ContactURI,
		LocalTag: localTag, UserAgent: userAgent,
	}
	inbound.server.CarrierTransport = carrier
	inbound.server.Profile = profile
	inbound.server.Registration = binding
	inbound.server.ContactURI = binding.ContactURI
	inbound.server.LocalTag = localTag
	inbound.server.UserAgent = userAgent
	controller.SetCarrierTerminator(inbound.server)
	inbound.calls = controller
	return nil
}

type inboundSIPFlow interface {
	ServeIncoming(context.Context) error
}

func newInboundMessaging(service *messaging.Service, tracker *messageTracker, sink MessageSink) (*inboundMessaging, error) {
	if service == nil || tracker == nil || sink == nil {
		return nil, errors.New("invalid inbound messaging configuration")
	}
	inbound := &inboundMessaging{service: service, tracker: tracker, sink: sink, done: make(chan error, 1)}
	inbound.server = &voicehost.IMSInboundWireServer{MessageHandler: voicehost.IMSMessageHandlerFunc(inbound.handle)}
	return inbound, nil
}

func (inbound *inboundMessaging) Start(flow inboundSIPFlow) error {
	if inbound == nil || flow == nil {
		return errors.New("inbound SIP flow is unavailable")
	}
	inbound.mu.Lock()
	defer inbound.mu.Unlock()
	if inbound.started {
		return errors.New("inbound SIP flow is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	inbound.cancel = cancel
	inbound.started = true
	go func() { inbound.finish(flow.ServeIncoming(ctx)) }()
	return nil
}

func (inbound *inboundMessaging) HandleSIPIncoming(ctx context.Context, request voiceclient.SIPIncomingRequest) []voiceclient.SIPIncomingResponse {
	responses, err := inbound.server.HandleRequest(ctx, request)
	if strings.EqualFold(strings.TrimSpace(request.Method), "MESSAGE") {
		inbound.mu.Lock()
		inbound.fault = err
		inbound.mu.Unlock()
	}
	out := make([]voiceclient.SIPIncomingResponse, 0, len(responses))
	for _, response := range responses {
		out = append(out, voiceclient.SIPIncomingResponse{
			StatusCode: response.StatusCode, Reason: response.Reason, Headers: response.Headers,
			Body: append([]byte(nil), response.Body...), NoResponse: response.NoResponse,
		})
	}
	return out
}

func (inbound *inboundMessaging) HandleSIPIncomingStreaming(ctx context.Context, request voiceclient.SIPIncomingRequest, emit func(voiceclient.SIPIncomingResponse) error) error {
	if inbound == nil || emit == nil {
		return errors.New("inbound SIP streaming handler is unavailable")
	}
	err := inbound.server.HandleRequestStreaming(ctx, request, func(response voicehost.IMSInboundWireResponse) error {
		return emit(voiceclient.SIPIncomingResponse{
			StatusCode: response.StatusCode, Reason: response.Reason, Headers: response.Headers,
			Body: append([]byte(nil), response.Body...), NoResponse: response.NoResponse,
		})
	})
	if strings.EqualFold(strings.TrimSpace(request.Method), "MESSAGE") {
		inbound.mu.Lock()
		inbound.fault = err
		inbound.mu.Unlock()
	}
	return err
}

func (inbound *inboundMessaging) PendingCall() (ims.IncomingCallInfo, bool) {
	if inbound == nil {
		return ims.IncomingCallInfo{}, false
	}
	inbound.mu.Lock()
	calls := inbound.calls
	inbound.mu.Unlock()
	if calls == nil {
		return ims.IncomingCallInfo{}, false
	}
	return calls.Pending()
}

func (inbound *inboundMessaging) SetCallAvailability(available func() bool) {
	if inbound == nil {
		return
	}
	inbound.mu.Lock()
	calls := inbound.calls
	inbound.mu.Unlock()
	if calls != nil {
		calls.SetAvailability(available)
	}
}

func (inbound *inboundMessaging) AnswerCall(ctx context.Context, callID string, bufferMS int) (*ims.InboundMediaCall, error) {
	if inbound == nil {
		return nil, ims.ErrIncomingCallNotFound
	}
	inbound.mu.Lock()
	calls := inbound.calls
	inbound.mu.Unlock()
	if calls == nil {
		return nil, ims.ErrIncomingCallNotFound
	}
	return calls.Answer(ctx, callID, bufferMS)
}

func (inbound *inboundMessaging) RejectCall(callID string) error {
	if inbound == nil {
		return ims.ErrIncomingCallNotFound
	}
	inbound.mu.Lock()
	calls := inbound.calls
	inbound.mu.Unlock()
	if calls == nil {
		return ims.ErrIncomingCallNotFound
	}
	return calls.Reject(callID)
}

func (inbound *inboundMessaging) handle(ctx context.Context, request voicehost.IMSMessageRequest) (voicehost.IMSMessageResult, error) {
	result, err := inbound.service.HandleIMSMessage(ctx, messaging.IMSMessageRequest{
		FromURI: request.FromURI, ToURI: request.ToURI, CallID: request.CallID, CSeq: request.CSeq,
		ContentType: request.ContentType, Body: append([]byte(nil), request.Body...), Headers: request.Headers,
	})
	var publishErr error
	if err == nil && result.Incoming != nil {
		event := inbound.tracker.identity(fmt.Sprintf("received:%s:%d", request.CallID, request.CSeq), providermessages.KindReceived)
		event.MessageID = fmt.Sprintf("ims:%s:%d", request.CallID, request.CSeq)
		event.Sender, event.Recipient, event.Body = result.Incoming.Sender, result.Incoming.Recipient, result.Incoming.Content
		if !result.Incoming.Timestamp.IsZero() {
			event.ObservedAt = result.Incoming.Timestamp
		}
		publishErr = inbound.sink.Publish(event)
	}
	if err == nil && publishErr == nil && result.DeliveryReport != nil {
		match := inbound.tracker.resolve(result.DeliveryReport)
		event := inbound.tracker.identity(fmt.Sprintf("delivery:%s:%d", request.CallID, request.CSeq), providermessages.KindDelivery)
		event.MessageID, event.Part = match.MessageID, match.PartNo
		event.CallID, event.InReplyTo, event.RPMR = result.DeliveryReport.CallID, result.DeliveryReport.InReplyTo, result.DeliveryReport.RPMR
		event.State, event.SIPCode, event.RPCause, event.Error = firstNonEmpty(result.DeliveryReport.State, match.State), result.DeliveryReport.SIPCode, result.DeliveryReport.RPCause, result.DeliveryReport.ErrorText
		if !result.DeliveryReport.ReportAt.IsZero() {
			event.ObservedAt = result.DeliveryReport.ReportAt
		}
		publishErr = inbound.sink.Publish(event)
	}
	if publishErr != nil {
		return voicehost.IMSMessageResult{StatusCode: 500, Reason: "message persistence unavailable"}, publishErr
	}
	return voicehost.IMSMessageResult{
		StatusCode: result.StatusCode, Reason: result.Reason,
		ContentType: result.ReplyContentType, Body: append([]byte(nil), result.ReplyBody...),
	}, err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (inbound *inboundMessaging) finish(err error) {
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, voiceclient.ErrSIPFlowClosed) {
		inbound.mu.Lock()
		inbound.fault = err
		inbound.mu.Unlock()
	}
	inbound.done <- err
}

func (inbound *inboundMessaging) Fault() error {
	if inbound == nil {
		return errors.New("inbound messaging is not running")
	}
	inbound.mu.Lock()
	defer inbound.mu.Unlock()
	return inbound.fault
}

func (inbound *inboundMessaging) Close(ctx context.Context) error {
	if inbound == nil {
		return nil
	}
	inbound.mu.Lock()
	started := inbound.started
	cancel := inbound.cancel
	calls := inbound.calls
	inbound.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	var callErr error
	if calls != nil {
		callErr = calls.Close(ctx)
	}
	if !started {
		return callErr
	}
	cancel()
	select {
	case serveErr := <-inbound.done:
		if errors.Is(serveErr, context.Canceled) || errors.Is(serveErr, voiceclient.ErrSIPFlowClosed) {
			return callErr
		}
		return errors.Join(callErr, serveErr)
	case <-ctx.Done():
		return errors.Join(callErr, ctx.Err())
	}
}

var _ messaging.DeliveryStore = (*messageTracker)(nil)
var _ voiceclient.SIPIncomingRequestHandler = (*inboundMessaging)(nil)
var _ voiceclient.SIPStreamingIncomingRequestHandler = (*inboundMessaging)(nil)
