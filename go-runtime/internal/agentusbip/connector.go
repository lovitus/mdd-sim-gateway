package agentusbip

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

type Connector struct {
	url         string
	serverToken string
	httpClient  *http.Client
}

func NewConnector(serverURL, serverToken string, httpClient *http.Client) (*Connector, error) {
	parsed, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || parsed.Host == "" || httpClient == nil || len(serverToken) < 32 {
		return nil, errors.New("invalid Agent USB/IP connector configuration")
	}
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else if parsed.Scheme == "http" {
		parsed.Scheme = "ws"
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return nil, errors.New("Agent USB/IP connector requires HTTP(S) or WS(S)")
	}
	parsed.Path, parsed.RawQuery, parsed.Fragment = "/v1/agent/usbip/ws", "", ""
	return &Connector{url: parsed.String(), serverToken: serverToken, httpClient: httpClient}, nil
}

func (connector *Connector) Connect(ctx context.Context, identity EndpointIdentity) (net.Conn, error) {
	normalizeEndpoint(&identity)
	if err := validateEndpoint(identity); err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+connector.serverToken)
	headers.Set("X-MDD-Agent-ID", identity.AgentID)
	headers.Set("X-MDD-Agent-Generation", identity.ProcessGeneration)
	headers.Set("X-MDD-USBIP-Role", string(identity.Role))
	headers.Set("X-MDD-USBIP-Source-Agent", identity.SourceAgentID)
	headers.Set("X-MDD-USBIP-Source-Generation", identity.SourceProcessGeneration)
	headers.Set("X-MDD-USBIP-Attachment", identity.AttachmentID)
	headers.Set("X-MDD-USBIP-Card-Generation", identity.SessionGeneration)
	headers.Set("X-MDD-USBIP-Equipment", identity.EquipmentID)
	headers.Set("X-MDD-USBIP-Card", identity.CardID)
	headers.Set("X-MDD-USBIP-Session", identity.USBSessionID)
	headers.Set("X-MDD-USBIP-Stream", identity.StreamID)
	headers.Set("X-MDD-USBIP-Token", identity.StreamToken)
	socket, response, err := websocket.Dial(ctx, connector.url, &websocket.DialOptions{
		HTTPClient: connector.httpClient, HTTPHeader: headers, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if response != nil {
			return nil, errors.New("Core rejected Agent USB/IP endpoint")
		}
		return nil, err
	}
	socket.SetReadLimit(maximumMessage)
	messageType, payload, err := socket.Read(ctx)
	var acknowledgement struct {
		Type     string `json:"type"`
		Version  int    `json:"version"`
		StreamID string `json:"stream_id"`
		Role     Role   `json:"role"`
	}
	if err == nil && messageType == websocket.MessageText {
		err = json.Unmarshal(payload, &acknowledgement)
	}
	if err != nil || messageType != websocket.MessageText || acknowledgement.Type != "agent.usbip.ready" ||
		acknowledgement.Version != 1 || acknowledgement.StreamID != identity.StreamID || acknowledgement.Role != identity.Role {
		_ = socket.Close(websocket.StatusPolicyViolation, "invalid USB/IP acknowledgement")
		return nil, errors.New("Core returned an invalid Agent USB/IP acknowledgement")
	}
	return websocket.NetConn(context.Background(), socket, websocket.MessageBinary), nil
}
