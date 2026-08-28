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
	OperationTimeout time.Duration
	Connected        func()
}

const maximumConcurrentRequests = 16

const maximumOperationTimeout = time.Minute

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
		if err := message.validate(); err != nil || message.Kind != kindAKARequest {
			_ = socket.Close(websocket.StatusPolicyViolation, "invalid request")
			return errors.New("Core sent an invalid Agent request")
		}
		requestID := message.RequestID
		request := *message.AKARequest
		select {
		case slots <- struct{}{}:
		default:
			result := AKAResponse{
				OperationID: request.OperationID, SessionGeneration: request.SessionGeneration,
				Failure: &RemoteError{Kind: "conflict", Code: "agent_operation_limit", Retryable: true},
			}
			writes.Lock()
			err := writeEnvelope(ctx, socket, envelope{
				Kind: kindAKAResponse, RequestID: requestID, AKAResult: &result,
			})
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
			operationContext, cancel := context.WithTimeout(ctx, client.OperationTimeout)
			result := client.Authenticator.AuthenticateAKA(operationContext, request)
			cancel()
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
			writes.Lock()
			defer writes.Unlock()
			if err := writeEnvelope(ctx, socket, envelope{
				Kind: kindAKAResponse, RequestID: requestID, AKAResult: &result,
			}); err != nil {
				socket.CloseNow()
			}
		}()
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
