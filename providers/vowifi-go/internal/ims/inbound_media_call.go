// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

var (
	ErrIncomingCallNotFound = errors.New("incoming call not found")
	ErrIncomingCallBusy     = errors.New("incoming call already owned")
	ErrIncomingCallEnded    = errors.New("incoming call ended by carrier")
)

type IncomingCallInfo struct {
	CallID     string
	Caller     string
	Callee     string
	ReceivedAt time.Time
}

type IncomingCarrierTerminator interface {
	EndCarrierCallWithResult(context.Context, string) (voicehost.DialogInfoResult, error)
}

type pendingIncomingCall struct {
	info       IncomingCallInfo
	codec      media.Codec
	remoteRTP  string
	remoteRTCP string
	decision   chan voiceclient.SIPResponse
	answering  bool
}

// IncomingCallController translates the upstream inbound B2BUA's local-client
// side into typed browser decisions and the existing userspace PCM bridge. It
// owns no listener, registration recovery, process lifecycle, or browser state.
type IncomingCallController struct {
	mu         sync.Mutex
	stack      *usernet.Stack
	localIP    string
	contactURI string
	localTag   string
	now        func() time.Time
	terminator IncomingCarrierTerminator
	available  func() bool
	pending    *pendingIncomingCall
	active     *InboundMediaCall
	closed     bool
}

func NewIncomingCallController(stack *usernet.Stack, localIP, contactURI, localTag string) (*IncomingCallController, error) {
	localIP = strings.TrimSpace(localIP)
	contactURI = strings.Trim(strings.TrimSpace(contactURI), "<>")
	localTag = strings.TrimSpace(localTag)
	if stack == nil || net.ParseIP(localIP) == nil || contactURI == "" || localTag == "" {
		return nil, errors.New("invalid incoming call controller configuration")
	}
	return &IncomingCallController{
		stack: stack, localIP: localIP, contactURI: contactURI, localTag: localTag, now: time.Now,
		available: func() bool { return true },
	}, nil
}

func (controller *IncomingCallController) SetCarrierTerminator(terminator IncomingCarrierTerminator) {
	if controller == nil {
		return
	}
	controller.mu.Lock()
	controller.terminator = terminator
	controller.mu.Unlock()
}

func (controller *IncomingCallController) SetAvailability(available func() bool) {
	if controller == nil {
		return
	}
	if available == nil {
		available = func() bool { return true }
	}
	controller.mu.Lock()
	controller.available = available
	controller.mu.Unlock()
}

func (controller *IncomingCallController) Pending() (IncomingCallInfo, bool) {
	if controller == nil {
		return IncomingCallInfo{}, false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.pending == nil || controller.closed {
		return IncomingCallInfo{}, false
	}
	return controller.pending.info, true
}

func (controller *IncomingCallController) RoundTripInvite(ctx context.Context, request voiceclient.SIPRequestMessage, onProvisional voiceclient.ProvisionalResponseHandler) (voiceclient.SIPResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callID := requestHeader(request.Headers, "Call-ID")
	if callID == "" {
		return sipResponse(400, "Call-ID empty"), nil
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return sipResponse(480, "Temporarily Unavailable"), nil
	}
	if controller.active != nil && controller.active.callID == callID {
		call := controller.active
		controller.mu.Unlock()
		return call.answerReinvite(request)
	}
	if controller.pending != nil && controller.pending.info.CallID == callID {
		controller.mu.Unlock()
		return sipResponse(491, "Request Pending"), nil
	}
	available := controller.available
	if controller.pending != nil || controller.active != nil {
		controller.mu.Unlock()
		return sipResponse(486, "Busy Here"), nil
	}
	controller.mu.Unlock()
	if available == nil || !available() {
		return sipResponse(486, "Busy Here"), nil
	}
	codec, remoteRTP, remoteRTCP, err := inboundOfferEndpoints(request.Body)
	if err != nil {
		return sipResponse(488, "Not Acceptable Here"), nil
	}
	pending := &pendingIncomingCall{
		info: IncomingCallInfo{
			CallID: callID, Caller: requestHeaderURI(request.Headers, "From"),
			Callee: requestHeaderURI(request.Headers, "To"), ReceivedAt: controller.now().UTC(),
		},
		codec: codec, remoteRTP: remoteRTP, remoteRTCP: remoteRTCP,
		decision: make(chan voiceclient.SIPResponse, 1),
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return sipResponse(480, "Temporarily Unavailable"), nil
	}
	if controller.pending != nil || controller.active != nil {
		controller.mu.Unlock()
		return sipResponse(486, "Busy Here"), nil
	}
	controller.pending = pending
	controller.mu.Unlock()

	ringing := sipResponse(180, "Ringing")
	ringing.Headers = map[string][]string{
		"To":      {withHeaderTag(requestHeader(request.Headers, "To"), controller.localTag)},
		"Contact": {"<" + controller.contactURI + ">"},
	}
	if onProvisional != nil {
		if err := onProvisional(ctx, request, ringing); err != nil {
			controller.removePending(pending)
			return voiceclient.SIPResponse{}, err
		}
	}
	select {
	case response := <-pending.decision:
		return response, nil
	case <-ctx.Done():
		controller.removePending(pending)
		return voiceclient.SIPResponse{}, ctx.Err()
	}
}

func (controller *IncomingCallController) RoundTripRequest(ctx context.Context, request voiceclient.SIPRequestMessage) (voiceclient.SIPResponse, error) {
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	callID := requestHeader(request.Headers, "Call-ID")
	switch method {
	case "CANCEL":
		if controller.resolvePending(callID, sipResponse(487, "Request Terminated")) {
			return sipResponse(200, "OK"), nil
		}
		return sipResponse(481, "Call/Transaction Does Not Exist"), nil
	case "BYE":
		if controller.remoteEnd(callID) {
			return sipResponse(200, "OK"), nil
		}
		return sipResponse(481, "Call/Transaction Does Not Exist"), nil
	case "UPDATE":
		controller.mu.Lock()
		call := controller.active
		controller.mu.Unlock()
		if call == nil || call.callID != callID {
			return sipResponse(481, "Call/Transaction Does Not Exist"), nil
		}
		if len(request.Body) == 0 {
			return sipResponse(200, "OK"), nil
		}
		return call.answerReinvite(request)
	case "PRACK", "INFO", "MESSAGE", "OPTIONS":
		return sipResponse(200, "OK"), nil
	default:
		return sipResponse(501, "Not Implemented"), nil
	}
}

func (controller *IncomingCallController) WriteRequest(_ context.Context, request voiceclient.SIPRequestMessage) error {
	if strings.EqualFold(strings.TrimSpace(request.Method), "ACK") {
		return nil
	}
	return fmt.Errorf("unsupported one-way browser SIP request %q", strings.TrimSpace(request.Method))
}

func (controller *IncomingCallController) Answer(ctx context.Context, callID string, bufferMS int) (*InboundMediaCall, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callID = strings.TrimSpace(callID)
	if bufferMS < 100 || bufferMS > 2000 {
		return nil, errors.New("incoming media buffer must be between 100 and 2000 ms")
	}
	controller.mu.Lock()
	pending := controller.pending
	if controller.closed || pending == nil || pending.info.CallID != callID {
		controller.mu.Unlock()
		return nil, ErrIncomingCallNotFound
	}
	if pending.answering {
		controller.mu.Unlock()
		return nil, ErrIncomingCallBusy
	}
	if controller.active != nil || controller.terminator == nil {
		controller.mu.Unlock()
		return nil, ErrIncomingCallBusy
	}
	pending.answering = true
	stack, localIP, terminator := controller.stack, controller.localIP, controller.terminator
	controller.mu.Unlock()

	bridge, err := media.Open(context.Background(), stack, media.Config{
		LocalRTP: net.JoinHostPort(localIP, "0"), LocalRTCP: net.JoinHostPort(localIP, "0"),
		RemoteRTP: pending.remoteRTP, RemoteRTCP: pending.remoteRTCP,
		Codec: pending.codec, BufferMS: bufferMS,
	})
	if err != nil {
		controller.releaseAnswer(pending)
		return nil, err
	}
	localSDP, err := mediaOffer(bridge, pending.codec)
	if err != nil {
		_ = closeMedia(bridge)
		controller.releaseAnswer(pending)
		return nil, err
	}
	call := newInboundMediaCall(callID, pending.codec, bridge, terminator, controller.clearActive)
	response := sipResponse(200, "OK")
	response.Headers = map[string][]string{
		"To":      {withHeaderTag("<"+pending.info.Callee+">", controller.localTag)},
		"Contact": {"<" + controller.contactURI + ">"},
	}
	response.Body = voicehost.BuildSDPAnswerWithOptions(localSDP, voicehost.SDPAnswerOptions{
		Codecs: []voicehost.SDPCodec{sdpCodec(pending.codec)}, PTimeMS: 20, MaxPTimeMS: 20,
	})

	controller.mu.Lock()
	if controller.closed || controller.pending != pending || controller.active != nil {
		controller.mu.Unlock()
		_ = closeMedia(bridge)
		return nil, ErrIncomingCallNotFound
	}
	controller.pending = nil
	controller.active = call
	controller.mu.Unlock()
	// The decision channel is buffered and has exactly one producer. Once the
	// call has become active, always complete the carrier INVITE; a browser HTTP
	// cancellation at this boundary must not strand the IMS transaction.
	pending.decision <- response
	return call, nil
}

func (controller *IncomingCallController) Reject(callID string) error {
	if !controller.resolvePending(strings.TrimSpace(callID), sipResponse(486, "Busy Here")) {
		return ErrIncomingCallNotFound
	}
	return nil
}

func (controller *IncomingCallController) Close(ctx context.Context) error {
	if controller == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return nil
	}
	controller.closed = true
	pending, active := controller.pending, controller.active
	controller.pending, controller.active = nil, nil
	controller.mu.Unlock()
	if pending != nil {
		pending.decision <- sipResponse(480, "Temporarily Unavailable")
	}
	if active != nil {
		_, err := active.End(ctx)
		return err
	}
	return nil
}

func (controller *IncomingCallController) resolvePending(callID string, response voiceclient.SIPResponse) bool {
	controller.mu.Lock()
	pending := controller.pending
	if pending == nil || pending.info.CallID != callID {
		controller.mu.Unlock()
		return false
	}
	controller.pending = nil
	controller.mu.Unlock()
	pending.decision <- response
	return true
}

func (controller *IncomingCallController) removePending(pending *pendingIncomingCall) {
	controller.mu.Lock()
	if controller.pending == pending {
		controller.pending = nil
	}
	controller.mu.Unlock()
}

func (controller *IncomingCallController) releaseAnswer(pending *pendingIncomingCall) {
	controller.mu.Lock()
	if controller.pending == pending {
		pending.answering = false
	}
	controller.mu.Unlock()
}

func (controller *IncomingCallController) remoteEnd(callID string) bool {
	controller.mu.Lock()
	call := controller.active
	if call == nil || call.callID != callID {
		controller.mu.Unlock()
		return false
	}
	controller.active = nil
	controller.mu.Unlock()
	call.endFromCarrier()
	return true
}

func (controller *IncomingCallController) clearActive(call *InboundMediaCall) {
	controller.mu.Lock()
	if controller.active == call {
		controller.active = nil
	}
	controller.mu.Unlock()
}

type InboundMediaCall struct {
	callID     string
	codec      media.Codec
	bridge     *media.Bridge
	terminator IncomingCarrierTerminator
	onEnded    func(*InboundMediaCall)
	errors     chan error
	remoteDone chan struct{}

	mu          sync.Mutex
	remoteEnded bool
	ended       bool
	endResult   voicehost.DialogInfoResult
	closeOnce   sync.Once
	closeErr    error
}

func newInboundMediaCall(callID string, codec media.Codec, bridge *media.Bridge, terminator IncomingCarrierTerminator, onEnded func(*InboundMediaCall)) *InboundMediaCall {
	call := &InboundMediaCall{
		callID: callID, codec: codec, bridge: bridge, terminator: terminator, onEnded: onEnded,
		errors: make(chan error, 2), remoteDone: make(chan struct{}),
	}
	go func() {
		if err, ok := <-bridge.Errors(); ok && err != nil {
			call.publishError(err)
		}
	}()
	return call
}

func (call *InboundMediaCall) End(ctx context.Context) (voicehost.DialogInfoResult, error) {
	if call == nil {
		return voicehost.DialogInfoResult{}, nil
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.remoteEnded || call.ended {
		return call.endResult, nil
	}
	result, err := call.terminator.EndCarrierCallWithResult(ctx, call.callID)
	if closeErr := call.closeMedia(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err == nil && result.Accepted {
		call.ended, call.endResult = true, result
		if call.onEnded != nil {
			call.onEnded(call)
		}
	}
	return result, err
}

func (call *InboundMediaCall) WritePCM(frame []byte, capturedAt time.Time) (bool, error) {
	if call == nil {
		return false, media.ErrClosed
	}
	return call.bridge.WritePCM(frame, capturedAt)
}

func (call *InboundMediaCall) PCM() <-chan media.PCMFrame {
	if call == nil {
		return (*media.Bridge)(nil).PCM()
	}
	return call.bridge.PCM()
}

func (call *InboundMediaCall) Errors() <-chan error {
	if call == nil {
		return nil
	}
	return call.errors
}

func (call *InboundMediaCall) RemoteEnded() <-chan struct{} {
	if call == nil {
		return nil
	}
	return call.remoteDone
}

func (call *InboundMediaCall) answerReinvite(request voiceclient.SIPRequestMessage) (voiceclient.SIPResponse, error) {
	if len(request.Body) == 0 {
		return sipResponse(200, "OK"), nil
	}
	codec, remoteRTP, remoteRTCP, err := inboundOfferEndpointsForCodec(request.Body, call.codec)
	if err != nil || codec != call.codec {
		return sipResponse(488, "Not Acceptable Here"), nil
	}
	if err := call.bridge.SetRemote(remoteRTP, remoteRTCP); err != nil {
		return sipResponse(503, "Media Update Failed"), nil
	}
	localSDP, err := mediaOffer(call.bridge, call.codec)
	if err != nil {
		return sipResponse(503, "Media Update Failed"), nil
	}
	response := sipResponse(200, "OK")
	response.Body = voicehost.BuildSDPAnswerWithOptions(localSDP, voicehost.SDPAnswerOptions{
		Codecs: []voicehost.SDPCodec{sdpCodec(call.codec)}, PTimeMS: 20, MaxPTimeMS: 20,
	})
	return response, nil
}

func (call *InboundMediaCall) endFromCarrier() {
	call.mu.Lock()
	if call.remoteEnded || call.ended {
		call.mu.Unlock()
		return
	}
	call.remoteEnded, call.ended = true, true
	call.endResult = voicehost.DialogInfoResult{Accepted: true, StatusCode: 200, Reason: "remote ended"}
	close(call.remoteDone)
	call.mu.Unlock()
	_ = call.closeMedia()
	call.publishError(ErrIncomingCallEnded)
}

func (call *InboundMediaCall) closeMedia() error {
	call.closeOnce.Do(func() { call.closeErr = closeMedia(call.bridge) })
	return call.closeErr
}

func (call *InboundMediaCall) publishError(err error) {
	select {
	case call.errors <- err:
	default:
	}
}

func inboundOfferEndpoints(body []byte) (media.Codec, string, string, error) {
	if codec, rtp, rtcp, err := inboundOfferEndpointsForCodec(body, media.CodecPCMU); err == nil {
		return codec, rtp, rtcp, nil
	}
	return inboundOfferEndpointsForCodec(body, media.CodecPCMA)
}

func inboundOfferEndpointsForCodec(body []byte, codec media.Codec) (media.Codec, string, string, error) {
	rtp, rtcp, err := acceptedMediaEndpoints(voicehost.OutboundCallResult{RawSDP: append([]byte(nil), body...)}, codec)
	return codec, rtp, rtcp, err
}

func sipResponse(status int, reason string) voiceclient.SIPResponse {
	return voiceclient.SIPResponse{StatusCode: status, Reason: reason}
}

func requestHeader(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func requestHeaderURI(headers map[string]string, name string) string {
	value := requestHeader(headers, name)
	if start := strings.Index(value, "<"); start >= 0 {
		if end := strings.Index(value[start+1:], ">"); end >= 0 {
			return strings.TrimSpace(value[start+1 : start+1+end])
		}
	}
	if index := strings.Index(value, ";"); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func withHeaderTag(value, tag string) string {
	value, tag = strings.TrimSpace(value), strings.TrimSpace(tag)
	if value == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(value), ";tag=") || tag == "" {
		return value
	}
	return value + ";tag=" + tag
}

var _ voiceclient.SIPRequestTransport = (*IncomingCallController)(nil)
var _ voiceclient.SIPInviteTransport = (*IncomingCallController)(nil)
