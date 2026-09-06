package agentlink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Client struct {
	URL               string
	Token             string
	Hello             Hello
	HTTPClient        *http.Client
	Authenticator     Authenticator
	Modems            ModemExecutor
	SMSSessionFencing bool
	PIN               SIMPINExecutor
	PINConfiguration  bool
	Recovery          ModemRecoveryExecutor
	Media             ModemMediaExecutor
	Data              ModemDataExecutor
	Policies          ModemPolicyExecutor
	RawUSB            RawUSBExecutor
	EUICC             EUICCProfileExecutor
	Downloads         EUICCDownloadExecutor
	Discovery         EUICCDiscoveryExecutor
	Notifications     EUICCNotificationExecutor
	Provision         ProvisionExecutor
	ReaderReadback    ReaderReadbackExecutor
	HostHealth        bool
	Events            ModemEventSource
	OperationTimeout  time.Duration
	Connected         func()
	Health            func() TopologySnapshot
	HealthEvery       time.Duration
}

const maximumConcurrentRequests = 16

const maximumOperationTimeout = 3 * time.Minute

const smsSubmitOperationTimeout = 130 * time.Second

const euiccDiscoveryOperationTimeout = 120 * time.Second

const euiccNotificationOperationTimeout = 60 * time.Second
const provisionOperationTimeout = 90 * time.Second

const euiccNotificationMutationTimeout = 120 * time.Second

const modemDataPrepareTimeout = 60 * time.Second

const rawUSBStartTimeout = 2 * time.Minute

const defaultHealthEvery = 10 * time.Second

const ModemEventRetryEvery = 5 * time.Second

func (client Client) Run(ctx context.Context) error {
	if err := client.validate(); err != nil {
		return err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+client.Token)
	headers.Set("X-MDD-Agent-ID", client.Hello.AgentID)
	capabilities := []string{}
	if client.Events != nil {
		capabilities = append(capabilities, modemEventsFeature)
	}
	if client.Policies != nil {
		capabilities = append(capabilities, modemPolicyFeature)
		capabilities = append(capabilities, modemSIMAPDUPrepareFeature)
	}
	if client.Data != nil {
		capabilities = append(capabilities, modemDataRenewFeature)
	}
	if client.SMSSessionFencing {
		capabilities = append(capabilities, modemSMSSessionFeature)
	}
	if client.Recovery != nil {
		capabilities = append(capabilities, modemRecoveryFeature)
	}
	if client.PIN != nil {
		capabilities = append(capabilities, simPINFeature)
		if client.PINConfiguration {
			capabilities = append(capabilities, simPINConfigFeature)
		}
	}
	if client.ReaderReadback != nil {
		capabilities = append(capabilities, readerReadbackFeature)
	}
	if client.HostHealth {
		capabilities = append(capabilities, agentHostHealthFeature)
	}
	if len(capabilities) != 0 {
		headers.Set(agentCapabilitiesHeader, strings.Join(capabilities, ","))
	}
	httpClient := cloneHTTPClient(client.HTTPClient)
	socket, upgrade, err := websocket.Dial(ctx, client.URL, &websocket.DialOptions{
		HTTPClient: httpClient, HTTPHeader: headers,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return fmt.Errorf("connect Agent WSS: %w", err)
	}
	eventsEnabled := upgrade != nil && featureEnabled(upgrade.Header.Get(agentFeaturesHeader), modemEventsFeature)
	policiesEnabled := upgrade != nil && featureEnabled(upgrade.Header.Get(agentFeaturesHeader), modemPolicyFeature)
	simAPDUPrepareEnabled := upgrade != nil && featureEnabled(upgrade.Header.Get(agentFeaturesHeader), modemSIMAPDUPrepareFeature)
	hostHealthEnabled := upgrade != nil && featureEnabled(upgrade.Header.Get(agentFeaturesHeader), agentHostHealthFeature)
	defer socket.CloseNow()
	socket.SetReadLimit(maximumMessage)
	if err := writeEnvelope(ctx, socket, envelope{Kind: kindHello, Hello: &client.Hello}); err != nil {
		return fmt.Errorf("send Agent hello: %w", err)
	}
	ackContext, cancelAck := context.WithTimeout(ctx, 10*time.Second)
	acknowledgement, err := readEnvelope(ackContext, socket)
	cancelAck()
	if err != nil || acknowledgement.validate() != nil || acknowledgement.Kind != kindHelloAck {
		return errors.New("Core rejected or did not acknowledge Agent hello")
	}
	if client.Connected != nil {
		client.Connected()
	}
	var writes sync.Mutex
	reportContext, stopReports := context.WithCancel(ctx)
	var reportDone chan error
	var eventDone chan error
	if client.Health != nil {
		reportDone = make(chan error, 1)
		go func() {
			err := client.reportHealth(reportContext, socket, &writes, policiesEnabled, simAPDUPrepareEnabled, hostHealthEnabled)
			if err != nil && reportContext.Err() == nil {
				socket.CloseNow()
			}
			reportDone <- err
		}()
	}
	if client.Events != nil && eventsEnabled {
		eventDone = make(chan error, 1)
		go func() {
			err := client.reportEvents(reportContext, socket, &writes)
			if err != nil && reportContext.Err() == nil {
				socket.CloseNow()
			}
			eventDone <- err
		}()
	}
	defer func() {
		stopReports()
		if reportDone != nil {
			<-reportDone
		}
		if eventDone != nil {
			<-eventDone
		}
	}()
	var workers sync.WaitGroup
	slots := make(chan struct{}, maximumConcurrentRequests)
	defer workers.Wait()
	for {
		message, err := readEnvelope(ctx, socket)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read Agent request: %w", err)
		}
		if err := message.validate(); err != nil {
			_ = socket.Close(websocket.StatusPolicyViolation, "invalid request")
			return errors.New("Core sent an invalid Agent request")
		}
		if message.Kind == kindModemEventAck {
			if client.Events == nil || !eventsEnabled {
				_ = socket.Close(websocket.StatusPolicyViolation, "unexpected event acknowledgement")
				return errors.New("Core sent an unexpected modem event acknowledgement")
			}
			ack := *message.ModemEventAck
			if ack.Accepted {
				err = client.Events.AckModemEvent(ack.EventID)
			} else if !ack.Retryable {
				err = client.Events.RejectModemEvent(ack.EventID, ack.Code)
			}
			if err != nil {
				return fmt.Errorf("apply modem event acknowledgement: %w", err)
			}
			continue
		}
		if message.Kind == kindPolicyRequest && (!policiesEnabled || client.Policies == nil) {
			_ = socket.Close(websocket.StatusPolicyViolation, "unexpected modem policy request")
			return errors.New("Core sent an unexpected modem policy request")
		}
		if message.Kind != kindReaderReadbackRequest &&
			message.Kind != kindAKARequest && message.Kind != kindModemRequest && message.Kind != kindMediaRequest &&
			message.Kind != kindSIMPINRequest && message.Kind != kindModemRecoveryRequest &&
			message.Kind != kindDataRequest && message.Kind != kindPolicyRequest && message.Kind != kindRawUSBRequest &&
			message.Kind != kindEUICCRequest && message.Kind != kindDownloadRequest && message.Kind != kindDiscoveryRequest &&
			message.Kind != kindNotificationRequest && message.Kind != kindProvisionRequest {
			_ = socket.Close(websocket.StatusPolicyViolation, "invalid request")
			return errors.New("Core sent an invalid Agent request")
		}
		requestID := message.RequestID
		select {
		case slots <- struct{}{}:
		default:
			writes.Lock()
			err := client.writeOverload(ctx, socket, requestID, message)
			writes.Unlock()
			if err != nil {
				return fmt.Errorf("send Agent overload response: %w", err)
			}
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			operationContext, cancel := context.WithTimeout(ctx, client.timeoutFor(message))
			result := client.execute(operationContext, message)
			cancel()
			writes.Lock()
			defer writes.Unlock()
			result.RequestID = requestID
			if err := writeEnvelope(ctx, socket, result); err != nil {
				socket.CloseNow()
			}
		}()
	}
}

func (client Client) reportEvents(ctx context.Context, socket *websocket.Conn, writes *sync.Mutex) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastSent := make(map[string]time.Time)
	for {
		now := time.Now()
		events, err := client.Events.PendingModemEvents(now, 64)
		if err != nil {
			return err
		}
		for index := range events {
			event := events[index]
			if sent := lastSent[event.EventID]; !sent.IsZero() && now.Sub(sent) < ModemEventRetryEvery {
				continue
			}
			writes.Lock()
			err = writeEnvelope(ctx, socket, envelope{Kind: kindModemEvent, ModemEvent: &event})
			writes.Unlock()
			if err != nil {
				return err
			}
			lastSent[event.EventID] = now
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-client.Events.ModemEventWake():
		case <-ticker.C:
		}
	}
}

func (client Client) timeoutFor(message envelope) time.Duration {
	if message.Kind == kindRawUSBRequest && message.RawUSBRequest != nil &&
		message.RawUSBRequest.Action != RawUSBStop && client.OperationTimeout < rawUSBStartTimeout {
		return rawUSBStartTimeout
	}
	if message.Kind == kindDataRequest && message.DataRequest != nil &&
		message.DataRequest.Action == ModemDataPrepare && client.OperationTimeout < modemDataPrepareTimeout {
		return modemDataPrepareTimeout
	}
	if message.Kind == kindNotificationRequest && message.NotificationRequest != nil &&
		message.NotificationRequest.Action != "" && client.OperationTimeout < euiccNotificationMutationTimeout {
		return euiccNotificationMutationTimeout
	}
	if message.Kind == kindNotificationRequest && client.OperationTimeout < euiccNotificationOperationTimeout {
		return euiccNotificationOperationTimeout
	}
	if message.Kind == kindDiscoveryRequest && client.OperationTimeout < euiccDiscoveryOperationTimeout {
		return euiccDiscoveryOperationTimeout
	}
	if message.Kind == kindModemRequest && message.ModemRequest != nil &&
		message.ModemRequest.Action == ModemSMSSend && client.OperationTimeout < smsSubmitOperationTimeout {
		return smsSubmitOperationTimeout
	}
	if message.Kind == kindProvisionRequest && client.OperationTimeout < provisionOperationTimeout {
		return provisionOperationTimeout
	}
	return client.OperationTimeout
}

func (client Client) writeOverload(ctx context.Context, socket *websocket.Conn, requestID string, message envelope) error {
	failure := &RemoteError{Kind: "conflict", Code: "agent_operation_limit", Retryable: true}
	if message.Kind == kindPolicyRequest {
		request := *message.PolicyRequest
		result := ModemPolicyResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
			EquipmentID: request.EquipmentID, CardID: request.CardID,
			SIMSessionGeneration: request.SIMSessionGeneration, Failure: failure}
		return writeEnvelope(ctx, socket, envelope{Kind: kindPolicyResponse, RequestID: requestID, PolicyResult: &result})
	}
	if message.Kind == kindRawUSBRequest {
		request := *message.RawUSBRequest
		result := rawUSBResponse(request)
		result.Failure = failure
		return writeEnvelope(ctx, socket, envelope{Kind: kindRawUSBResponse, RequestID: requestID, RawUSBResult: &result})
	}
	if message.Kind == kindDataRequest {
		request := *message.DataRequest
		result := ModemDataResponse{
			OperationID: request.OperationID, AttachmentID: request.AttachmentID,
			EquipmentID: request.EquipmentID, CardID: request.CardID,
			SIMSessionGeneration: request.SIMSessionGeneration, SessionID: request.SessionID,
			StreamID: request.StreamID, Failure: failure,
		}
		return writeEnvelope(ctx, socket, envelope{Kind: kindDataResponse, RequestID: requestID, DataResult: &result})
	}
	if message.Kind == kindNotificationRequest {
		request := *message.NotificationRequest
		result := EUICCNotificationResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			EID: request.EID, Failure: failure,
		}
		return writeEnvelope(ctx, socket, envelope{Kind: kindNotificationResponse, RequestID: requestID, NotificationResult: &result})
	}
	if message.Kind == kindProvisionRequest {
		request := *message.ProvisionRequest
		result := ProvisionResponse{
			OperationID: request.OperationID, EquipmentID: request.EquipmentID,
			CardID: request.CardID, SIMSessionGeneration: request.SIMSessionGeneration,
			State: ProvisionUnknown, ErrorCode: "agent_operation_limit",
		}
		return writeEnvelope(ctx, socket, envelope{Kind: kindProvisionResponse, RequestID: requestID, ProvisionResult: &result})
	}
	if message.Kind == kindDiscoveryRequest {
		request := *message.DiscoveryRequest
		result := EUICCDiscoveryResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			EID: request.EID, Failure: failure,
		}
		return writeEnvelope(ctx, socket, envelope{Kind: kindDiscoveryResponse, RequestID: requestID, DiscoveryResult: &result})
	}
	if message.Kind == kindDownloadRequest {
		request := *message.DownloadRequest
		result := EUICCDownloadResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			EID: request.EID, Action: request.Action, Failure: failure,
		}
		return writeEnvelope(ctx, socket, envelope{Kind: kindDownloadResponse, RequestID: requestID, DownloadResult: &result})
	}
	if message.Kind == kindEUICCRequest {
		request := *message.EUICCRequest
		result := EUICCProfileResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			EID: request.EID, ICCID: request.ICCID, Action: request.Action, Failure: failure,
		}
		return writeEnvelope(ctx, socket, envelope{Kind: kindEUICCResponse, RequestID: requestID, EUICCResult: &result})
	}
	if message.Kind == kindMediaRequest {
		request := *message.MediaRequest
		result := ModemMediaResponse{
			OperationID: request.OperationID, AttachmentID: request.AttachmentID,
			EquipmentID: request.EquipmentID, CardID: request.CardID, SessionID: request.SessionID,
			Failure: failure,
		}
		return writeEnvelope(ctx, socket, envelope{Kind: kindMediaResponse, RequestID: requestID, MediaResult: &result})
	}
	if message.Kind == kindModemRequest {
		request := *message.ModemRequest
		result := ModemResponse{
			OperationID: request.OperationID, AttachmentID: request.AttachmentID,
			EquipmentID: request.EquipmentID, CardID: request.CardID, Failure: failure,
		}
		return writeEnvelope(ctx, socket, envelope{Kind: kindModemResponse, RequestID: requestID, ModemResult: &result})
	}
	if message.Kind == kindSIMPINRequest {
		request := *message.SIMPINRequest
		result := SIMPINResponse{OperationID: request.OperationID, CardID: request.CardID, ReaderName: request.ReaderName,
			AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID, SIMSessionGeneration: request.SIMSessionGeneration,
			Action: request.Action, State: "unavailable", Failure: failure}
		return writeEnvelope(ctx, socket, envelope{Kind: kindSIMPINResponse, RequestID: requestID, SIMPINResult: &result})
	}
	if message.Kind == kindModemRecoveryRequest {
		request := *message.ModemRecoveryRequest
		result := ModemRecoveryResponse{OperationID: request.OperationID, EquipmentID: request.EquipmentID,
			CardID: request.CardID, AttachmentID: request.AttachmentID, SIMSessionGeneration: request.SIMSessionGeneration,
			Action: request.Action, State: "unavailable", Failure: failure}
		return writeEnvelope(ctx, socket, envelope{Kind: kindModemRecoveryResponse, RequestID: requestID, ModemRecoveryResult: &result})
	}
	request := *message.AKARequest
	result := AKAResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration, Failure: failure,
	}
	return writeEnvelope(ctx, socket, envelope{Kind: kindAKAResponse, RequestID: requestID, AKAResult: &result})
}

func (client Client) execute(ctx context.Context, message envelope) envelope {
	if message.Kind == kindReaderReadbackRequest {
		request := *message.ReaderReadbackRequest
		result := ReaderReadbackResponse{
			OperationID: request.OperationID, ProcessGeneration: request.ProcessGeneration,
			ReaderName: request.ReaderName, CardID: request.CardID,
			SIMSessionGeneration: request.SIMSessionGeneration, State: "unknown",
			ErrorCode: "reader_readback_unavailable",
		}
		if client.ReaderReadback != nil {
			result = client.ReaderReadback.ReadReader(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = ReaderReadbackResponse{
				OperationID: request.OperationID, ProcessGeneration: request.ProcessGeneration,
				ReaderName: request.ReaderName, CardID: request.CardID,
				SIMSessionGeneration: request.SIMSessionGeneration, State: "failed",
				ErrorCode: "invalid_agent_reader_readback_result",
			}
		}
		return envelope{Kind: kindReaderReadbackResponse, ReaderReadbackResult: &result}
	}
	if message.Kind == kindProvisionRequest {
		request := *message.ProvisionRequest
		result := ProvisionResponse{OperationID: request.OperationID, EquipmentID: request.EquipmentID,
			CardID: request.CardID, SIMSessionGeneration: request.SIMSessionGeneration,
			State: ProvisionUnknown, ErrorCode: "provision_unavailable"}
		if request.ReadOnly {
			if reconciler, ok := client.Provision.(ProvisionReconciler); ok {
				result = reconciler.ReconcileProvision(ctx, request)
			} else {
				result.ErrorCode = "provision_reconcile_unavailable"
			}
		} else if client.Provision != nil {
			result = client.Provision.ExecuteProvision(ctx, request)
		}
		return envelope{Kind: kindProvisionResponse, ProvisionResult: &result}
	}
	if message.Kind == kindSIMPINRequest {
		request := *message.SIMPINRequest
		result := SIMPINResponse{OperationID: request.OperationID, CardID: request.CardID, ReaderName: request.ReaderName, AttachmentID: request.AttachmentID, EquipmentID: request.EquipmentID, SIMSessionGeneration: request.SIMSessionGeneration, Action: request.Action, State: "unavailable", Failure: &RemoteError{Kind: "not_ready", Code: "sim_pin_unavailable"}}
		if client.PIN != nil {
			result = client.PIN.ExecuteSIMPIN(ctx, request)
		}
		if result.OperationID == "" {
			result.OperationID = request.OperationID
		}
		if result.CardID == "" {
			result.CardID = request.CardID
		}
		if result.Action == "" {
			result.Action = request.Action
		}
		return envelope{Kind: kindSIMPINResponse, SIMPINResult: &result}
	}
	if message.Kind == kindModemRecoveryRequest {
		request := *message.ModemRecoveryRequest
		result := ModemRecoveryResponse{OperationID: request.OperationID, EquipmentID: request.EquipmentID,
			CardID: request.CardID, AttachmentID: request.AttachmentID, SIMSessionGeneration: request.SIMSessionGeneration,
			Action: request.Action, State: "unavailable",
			Failure: &RemoteError{Kind: "not_ready", Code: "modem_recovery_unavailable"}}
		if client.Recovery != nil {
			result = client.Recovery.ExecuteModemRecovery(ctx, request)
		}
		return envelope{Kind: kindModemRecoveryResponse, ModemRecoveryResult: &result}
	}
	if message.Kind == kindPolicyRequest {
		request := *message.PolicyRequest
		result := ModemPolicyResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
			EquipmentID: request.EquipmentID, CardID: request.CardID,
			SIMSessionGeneration: request.SIMSessionGeneration,
			Failure:              &RemoteError{Kind: "not_ready", Code: "modem_policy_unavailable"}}
		if client.Policies != nil {
			result = client.Policies.ExecuteModemPolicy(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = ModemPolicyResponse{OperationID: request.OperationID, AttachmentID: request.AttachmentID,
				EquipmentID: request.EquipmentID, CardID: request.CardID,
				SIMSessionGeneration: request.SIMSessionGeneration,
				Failure:              &RemoteError{Kind: "failed", Code: "invalid_agent_policy_result"}}
		}
		return envelope{Kind: kindPolicyResponse, PolicyResult: &result}
	}
	if message.Kind == kindRawUSBRequest {
		request := *message.RawUSBRequest
		result := rawUSBResponse(request)
		result.Failure = &RemoteError{Kind: "not_ready", Code: "raw_usb_unavailable"}
		if client.RawUSB != nil {
			result = client.RawUSB.ExecuteRawUSB(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = rawUSBResponse(request)
			result.Failure = &RemoteError{Kind: "failed", Code: "invalid_agent_raw_usb_result"}
		}
		return envelope{Kind: kindRawUSBResponse, RawUSBResult: &result}
	}
	if message.Kind == kindDataRequest {
		request := *message.DataRequest
		result := ModemDataResponse{
			OperationID: request.OperationID, AttachmentID: request.AttachmentID,
			EquipmentID: request.EquipmentID, CardID: request.CardID,
			SIMSessionGeneration: request.SIMSessionGeneration, SessionID: request.SessionID,
			StreamID: request.StreamID,
			Failure:  &RemoteError{Kind: "not_ready", Code: "modem_data_unavailable"},
		}
		if client.Data != nil {
			result = client.Data.ExecuteModemData(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = ModemDataResponse{
				OperationID: request.OperationID, AttachmentID: request.AttachmentID,
				EquipmentID: request.EquipmentID, CardID: request.CardID,
				SIMSessionGeneration: request.SIMSessionGeneration, SessionID: request.SessionID,
				StreamID: request.StreamID,
				Failure:  &RemoteError{Kind: "failed", Code: "invalid_agent_data_result"},
			}
		}
		return envelope{Kind: kindDataResponse, DataResult: &result}
	}
	if message.Kind == kindNotificationRequest {
		request := *message.NotificationRequest
		result := EUICCNotificationResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			EID:     request.EID,
			Failure: &RemoteError{Kind: "not_ready", Code: "euicc_notification_inventory_unavailable"},
		}
		if client.Notifications != nil {
			result = client.Notifications.ExecuteEUICCNotification(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = EUICCNotificationResponse{
				OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
				EID:     request.EID,
				Failure: &RemoteError{Kind: "failed", Code: "invalid_agent_euicc_notification_result"},
			}
		}
		return envelope{Kind: kindNotificationResponse, NotificationResult: &result}
	}
	if message.Kind == kindDiscoveryRequest {
		request := *message.DiscoveryRequest
		result := EUICCDiscoveryResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			EID:     request.EID,
			Failure: &RemoteError{Kind: "not_ready", Code: "euicc_discovery_unavailable"},
		}
		if client.Discovery != nil {
			result = client.Discovery.ExecuteEUICCDiscovery(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = EUICCDiscoveryResponse{
				OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
				EID:     request.EID,
				Failure: &RemoteError{Kind: "failed", Code: "invalid_agent_euicc_discovery_result"},
			}
		}
		return envelope{Kind: kindDiscoveryResponse, DiscoveryResult: &result}
	}
	if message.Kind == kindDownloadRequest {
		request := *message.DownloadRequest
		result := EUICCDownloadResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			EID: request.EID, Action: request.Action,
			Failure: &RemoteError{Kind: "not_ready", Code: "euicc_download_unavailable"},
		}
		if client.Downloads != nil {
			result = client.Downloads.ExecuteEUICCDownload(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = EUICCDownloadResponse{
				OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
				EID: request.EID, Action: request.Action,
				Failure: &RemoteError{Kind: "failed", Code: "invalid_agent_euicc_download_result"},
			}
		}
		return envelope{Kind: kindDownloadResponse, DownloadResult: &result}
	}
	if message.Kind == kindEUICCRequest {
		request := *message.EUICCRequest
		result := EUICCProfileResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			EID: request.EID, ICCID: request.ICCID, Action: request.Action,
			Failure: &RemoteError{Kind: "not_ready", Code: "euicc_profile_management_unavailable"},
		}
		if client.EUICC != nil {
			result = client.EUICC.ExecuteEUICCProfile(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = EUICCProfileResponse{
				OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
				EID: request.EID, ICCID: request.ICCID, Action: request.Action,
				Failure: &RemoteError{Kind: "failed", Code: "invalid_agent_euicc_result"},
			}
		}
		return envelope{Kind: kindEUICCResponse, EUICCResult: &result}
	}
	if message.Kind == kindMediaRequest {
		request := *message.MediaRequest
		result := ModemMediaResponse{
			OperationID: request.OperationID, AttachmentID: request.AttachmentID,
			EquipmentID: request.EquipmentID, CardID: request.CardID, SessionID: request.SessionID,
			Failure: &RemoteError{Kind: "not_ready", Code: "modem_media_unavailable"},
		}
		if client.Media != nil {
			result = client.Media.ExecuteModemMedia(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = ModemMediaResponse{
				OperationID: request.OperationID, AttachmentID: request.AttachmentID,
				EquipmentID: request.EquipmentID, CardID: request.CardID, SessionID: request.SessionID,
				Failure: &RemoteError{Kind: "failed", Code: "invalid_agent_media_result"},
			}
		}
		return envelope{Kind: kindMediaResponse, MediaResult: &result}
	}
	if message.Kind == kindModemRequest {
		request := *message.ModemRequest
		result := ModemResponse{
			OperationID: request.OperationID, AttachmentID: request.AttachmentID,
			EquipmentID: request.EquipmentID, CardID: request.CardID,
			Failure: &RemoteError{Kind: "not_ready", Code: "modem_operations_unavailable"},
		}
		if client.Modems != nil {
			result = client.Modems.ExecuteModem(ctx, request)
		}
		if err := result.ValidateFor(request); err != nil {
			result = ModemResponse{
				OperationID: request.OperationID, AttachmentID: request.AttachmentID,
				EquipmentID: request.EquipmentID, CardID: request.CardID,
				Failure: &RemoteError{Kind: "failed", Code: "invalid_agent_result"},
			}
		}
		return envelope{Kind: kindModemResponse, ModemResult: &result}
	}
	request := *message.AKARequest
	result := client.Authenticator.AuthenticateAKA(ctx, request)
	if result.OperationID == "" {
		result.OperationID = request.OperationID
	}
	if result.SessionGeneration == "" {
		result.SessionGeneration = request.SessionGeneration
	}
	if err := result.ValidateFor(request); err != nil {
		result = AKAResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			Failure: &RemoteError{Kind: "failed", Code: "invalid_agent_result"},
		}
	}
	return envelope{Kind: kindAKAResponse, AKAResult: &result}
}

func rawUSBResponse(request RawUSBRequest) RawUSBResponse {
	return RawUSBResponse{
		OperationID: request.OperationID, Action: request.Action, Role: request.Role,
		SourceAgentID: request.SourceAgentID, SourceProcessGeneration: request.SourceProcessGeneration,
		AttachmentID: request.AttachmentID, SessionGeneration: request.SessionGeneration,
		EquipmentID: request.EquipmentID, CardID: request.CardID,
		USBSessionID: request.USBSessionID, StreamID: request.StreamID,
	}
}

func (client Client) reportHealth(ctx context.Context, socket *websocket.Conn, writes *sync.Mutex,
	policiesEnabled, simAPDUPrepareEnabled, hostHealthEnabled bool) error {
	every := client.HealthEvery
	if every == 0 {
		every = defaultHealthEvery
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	var sequence uint64
	lastRevision := ""
	for {
		topology := NormalizeTopology(client.Health())
		if !hostHealthEnabled {
			topology.Host = nil
		}
		if !policiesEnabled {
			for index := range topology.Modems {
				topology.Modems[index].Policy = nil
			}
		}
		if !simAPDUPrepareEnabled {
			for index := range topology.Modems {
				topology.Modems[index].AT.SIMAPDUOnDemand = false
			}
		}
		revision, err := topology.Revision()
		if err != nil {
			return err
		}
		sequence++
		report := HealthReport{
			SchemaVersion: SchemaVersion, Sequence: sequence, TopologyRevision: revision,
		}
		if revision != lastRevision {
			report.Topology = &topology
		}
		writes.Lock()
		err = writeEnvelope(ctx, socket, envelope{Kind: kindHealth, Health: &report})
		writes.Unlock()
		if err != nil {
			return err
		}
		lastRevision = revision
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (client Client) validate() error {
	if len(client.Token) < minimumTokenBytes || client.Authenticator == nil ||
		client.OperationTimeout <= 0 || client.OperationTimeout > maximumOperationTimeout {
		return errors.New("invalid Agent link client configuration")
	}
	if err := client.Hello.Validate(); err != nil {
		return err
	}
	if client.HealthEvery != 0 && (client.HealthEvery < 10*time.Millisecond || client.HealthEvery > time.Minute) {
		return errors.New("invalid Agent health interval")
	}
	if client.SMSSessionFencing && client.Modems == nil {
		return errors.New("SMS session fencing requires a modem executor")
	}
	if client.HostHealth && client.Health == nil {
		return errors.New("Agent host health requires a health reporter")
	}
	parsed, err := url.Parse(client.URL)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" {
		return errors.New("invalid Agent link URL")
	}
	host := parsed.Hostname()
	if parsed.Scheme == "wss" && host != "" {
		return nil
	}
	if parsed.Scheme != "ws" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("Agent link requires wss, except literal-loopback ws for local testing")
	}
	return nil
}

func cloneHTTPClient(source *http.Client) *http.Client {
	if source == nil {
		source = http.DefaultClient
	}
	clone := *source
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}
