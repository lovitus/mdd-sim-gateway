// Package mediaproxy transparently relays one authorized browser media
// WebSocket through Core to a same-host provider. It preserves WebSocket
// message type and boundaries and never parses or persists PCM/control data.
package mediaproxy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	defaultMessageLimit = 64 << 10
	minimumTokenBytes   = 32
)

type Target struct {
	URL   string
	Token string
}

type Authorizer interface {
	AuthorizeMedia(context.Context, *http.Request) (Target, error)
}

type AuthorizerFunc func(context.Context, *http.Request) (Target, error)

func (function AuthorizerFunc) AuthorizeMedia(ctx context.Context, request *http.Request) (Target, error) {
	return function(ctx, request)
}

type AuthorizationError struct {
	Status int
	Code   string
}

func (failure *AuthorizationError) Error() string {
	return fmt.Sprintf("media authorization failed: HTTP %d (%s)", failure.Status, failure.Code)
}

type Handler struct {
	authorizer   Authorizer
	httpClient   *http.Client
	dialTimeout  time.Duration
	messageLimit int64
}

func NewHandler(authorizer Authorizer, client *http.Client, dialTimeout time.Duration, messageLimit int64) (*Handler, error) {
	if authorizer == nil || dialTimeout <= 0 || dialTimeout > 30*time.Second {
		return nil, errors.New("invalid media proxy configuration")
	}
	if messageLimit == 0 {
		messageLimit = defaultMessageLimit
	}
	if messageLimit < 320 || messageLimit > 1<<20 {
		return nil, errors.New("media proxy message limit must be between 320 bytes and 1 MiB")
	}
	if client == nil {
		client = &http.Client{}
	} else {
		clone := *client
		client = &clone
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Handler{authorizer: authorizer, httpClient: client, dialTimeout: dialTimeout, messageLimit: messageLimit}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(request) {
		http.Error(response, "same_origin_required", http.StatusForbidden)
		return
	}
	target, err := handler.authorizer.AuthorizeMedia(request.Context(), request)
	if err != nil {
		var authorization *AuthorizationError
		if errors.As(err, &authorization) && authorization.Status >= 400 && authorization.Status <= 499 {
			http.Error(response, authorization.Code, authorization.Status)
			return
		}
		http.Error(response, "media_authorization_failed", http.StatusForbidden)
		return
	}
	if err := target.Validate(); err != nil {
		http.Error(response, "media_target_invalid", http.StatusBadGateway)
		return
	}
	headers := http.Header{"Authorization": []string{"Bearer " + target.Token}}
	dialContext, cancelDial := context.WithTimeout(request.Context(), handler.dialTimeout)
	provider, providerResponse, err := websocket.Dial(dialContext, target.URL, &websocket.DialOptions{
		HTTPClient: handler.httpClient, HTTPHeader: headers, CompressionMode: websocket.CompressionDisabled,
	})
	cancelDial()
	if err != nil {
		status := http.StatusBadGateway
		if providerResponse != nil && providerResponse.StatusCode == http.StatusConflict {
			status = http.StatusConflict
		}
		http.Error(response, "media_provider_unavailable", status)
		return
	}
	provider.SetReadLimit(handler.messageLimit)
	browser, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		_ = provider.Close(websocket.StatusPolicyViolation, "browser handshake rejected")
		return
	}
	browser.SetReadLimit(handler.messageLimit)
	handler.bridge(browser, provider)
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host != "" && strings.EqualFold(request.Host, parsed.Host)
}

func (target Target) Validate() error {
	if len(target.Token) < minimumTokenBytes {
		return errors.New("media provider token is invalid")
	}
	parsed, err := url.Parse(target.URL)
	if err != nil || parsed.Scheme != "ws" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Port() == "" || !strings.HasPrefix(parsed.EscapedPath(), "/v1/media/") {
		return errors.New("media provider target must be an exact loopback ws URL")
	}
	address := net.ParseIP(strings.Trim(parsed.Hostname(), "[]"))
	if address == nil || !address.IsLoopback() {
		return errors.New("media provider target must use a literal loopback address")
	}
	return nil
}

func (handler *Handler) bridge(browser, provider *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer browser.CloseNow()
	defer provider.CloseNow()
	errorsChannel := make(chan relayResult, 2)
	go relayMessages(ctx, browser, provider, errorsChannel)
	go relayMessages(ctx, provider, browser, errorsChannel)
	first := <-errorsChannel
	code := websocket.CloseStatus(first.err)
	if code == -1 {
		code = websocket.StatusGoingAway
	}
	closeDone := make(chan struct{})
	go func() {
		_ = first.destination.Close(code, closeReason(code))
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
	}
	cancel()
	browser.CloseNow()
	provider.CloseNow()
	<-errorsChannel
}

type relayResult struct {
	destination *websocket.Conn
	err         error
}

func relayMessages(ctx context.Context, source, destination *websocket.Conn, result chan<- relayResult) {
	for {
		messageType, payload, err := source.Read(ctx)
		if err == nil && messageType != websocket.MessageText && messageType != websocket.MessageBinary {
			err = errors.New("unsupported WebSocket media message type")
		}
		if err == nil {
			err = destination.Write(ctx, messageType, payload)
		}
		if err != nil {
			result <- relayResult{destination: destination, err: err}
			return
		}
	}
}

func closeReason(code websocket.StatusCode) string {
	if code == websocket.StatusNormalClosure || code == websocket.StatusGoingAway {
		return "media session ended"
	}
	return "media relay failed"
}

// AuthorizedToken compares a presented bearer without exposing either token.
// Provider-side handlers use it after separately enforcing literal loopback.
func AuthorizedToken(header, expected string) bool {
	const prefix = "Bearer "
	if len(expected) < minimumTokenBytes || !strings.HasPrefix(header, prefix) {
		return false
	}
	want := sha256.Sum256([]byte(expected))
	got := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}
