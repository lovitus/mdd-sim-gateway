package agentlink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Client struct {
	URL              string
	Token            string
	Hello            Hello
	HTTPClient       *http.Client
	Authenticator    Authenticator
	Modems           ModemExecutor
	Media            ModemMediaExecutor
	EUICC            EUICCProfileExecutor
	Downloads        EUICCDownloadExecutor
	Discovery        EUICCDiscoveryExecutor
	Notifications    EUICCNotificationExecutor
	OperationTimeout time.Duration
	Connected        func()
	Health           func() TopologySnapshot
	HealthEvery      time.Duration
}

const maximumConcurrentRequests = 16

const maximumOperationTimeout = 3 * time.Minute

const smsSubmitOperationTimeout = 130 * time.Second

const euiccDiscoveryOperationTimeout = 120 * time.Second

const euiccNotificationOperationTimeout = 60 * time.Second

const euiccNotificationMutationTimeout = 120 * time.Second

const defaultHealthEvery = 10 * time.Second

func (client Client) Run(ctx context.Context) error {
	if err := client.validate(); err != nil {
		return err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+client.Token)
	headers.Set("X-MDD-Agent-ID", client.Hello.AgentID)
	httpClient := cloneHTTPClient(client.HTTPClient)
	socket, _, err := websocket.Dial(ctx, client.URL, &websocket.DialOptions{
		HTTPClient: httpClient, HTTPHeader: headers,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return fmt.Errorf("connect Agent WSS: %w", err)
	}
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
	if client.Health != nil {
		reportDone = make(chan error, 1)
		go func() {
			err := client.reportHealth(reportContext, socket, &writes)
			if err != nil && reportContext.Err() == nil {
				socket.CloseNow()
			}
			reportDone <- err
		}()
	}
	defer func() {
		stopReports()
		if reportDone != nil {
			<-reportDone
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
		if err := message.validate(); err != nil || message.Kind != kindAKARequest && message.Kind != kindModemRequest && message.Kind != kindMediaRequest && message.Kind != kindEUICCRequest && message.Kind != kindDownloadRequest && message.Kind != kindDiscoveryRequest && message.Kind != kindNotificationRequest {
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

func (client Client) timeoutFor(message envelope) time.Duration {
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
	return client.OperationTimeout
}

func (client Client) writeOverload(ctx context.Context, socket *websocket.Conn, requestID string, message envelope) error {
	failure := &RemoteError{Kind: "conflict", Code: "agent_operation_limit", Retryable: true}
	if message.Kind == kindNotificationRequest {
		request := *message.NotificationRequest
		result := EUICCNotificationResponse{
			OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
			EID: request.EID, Failure: failure,
		}
		return writeEnvelope(ctx, socket, envelope{Kind: kindNotificationResponse, RequestID: requestID, NotificationResult: &result})
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
	request := *message.AKARequest
	result := AKAResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration, Failure: failure,
	}
	return writeEnvelope(ctx, socket, envelope{Kind: kindAKAResponse, RequestID: requestID, AKAResult: &result})
}

func (client Client) execute(ctx context.Context, message envelope) envelope {
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

func (client Client) reportHealth(ctx context.Context, socket *websocket.Conn, writes *sync.Mutex) error {
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
