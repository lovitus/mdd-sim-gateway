package agentlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/coder/websocket"
)

const (
	kindHello             = "hello"
	kindHelloAck          = "hello_ack"
	kindAKARequest        = "aka_request"
	kindAKAResponse       = "aka_response"
	kindModemRequest      = "modem_request"
	kindModemResponse     = "modem_response"
	kindMediaRequest      = "modem_media_request"
	kindMediaResponse     = "modem_media_response"
	kindEUICCRequest      = "euicc_profile_request"
	kindEUICCResponse     = "euicc_profile_response"
	kindDownloadRequest   = "euicc_download_request"
	kindDownloadResponse  = "euicc_download_response"
	kindDiscoveryRequest  = "euicc_discovery_request"
	kindDiscoveryResponse = "euicc_discovery_response"
	kindHealth            = "health"
)

type envelope struct {
	Kind             string                  `json:"kind"`
	RequestID        string                  `json:"request_id,omitempty"`
	Hello            *Hello                  `json:"hello,omitempty"`
	AKARequest       *AKARequest             `json:"aka_request,omitempty"`
	AKAResult        *AKAResponse            `json:"aka_response,omitempty"`
	ModemRequest     *ModemRequest           `json:"modem_request,omitempty"`
	ModemResult      *ModemResponse          `json:"modem_response,omitempty"`
	MediaRequest     *ModemMediaRequest      `json:"modem_media_request,omitempty"`
	MediaResult      *ModemMediaResponse     `json:"modem_media_response,omitempty"`
	EUICCRequest     *EUICCProfileRequest    `json:"euicc_profile_request,omitempty"`
	EUICCResult      *EUICCProfileResponse   `json:"euicc_profile_response,omitempty"`
	DownloadRequest  *EUICCDownloadRequest   `json:"euicc_download_request,omitempty"`
	DownloadResult   *EUICCDownloadResponse  `json:"euicc_download_response,omitempty"`
	DiscoveryRequest *EUICCDiscoveryRequest  `json:"euicc_discovery_request,omitempty"`
	DiscoveryResult  *EUICCDiscoveryResponse `json:"euicc_discovery_response,omitempty"`
	Health           *HealthReport           `json:"health,omitempty"`
}

func (message envelope) emptyEUICC() bool {
	return message.EUICCRequest == nil && message.EUICCResult == nil
}

func (message envelope) emptyDownload() bool {
	return message.DownloadRequest == nil && message.DownloadResult == nil
}

func (message envelope) emptyDiscovery() bool {
	return message.DiscoveryRequest == nil && message.DiscoveryResult == nil
}

func readEnvelope(ctx context.Context, socket *websocket.Conn) (envelope, error) {
	messageType, payload, err := socket.Read(ctx)
	if err != nil {
		return envelope{}, err
	}
	if messageType != websocket.MessageText {
		return envelope{}, errors.New("Agent link message is not JSON text")
	}
	return decodeEnvelope(payload)
}

func decodeEnvelope(payload []byte) (envelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var message envelope
	if err := decoder.Decode(&message); err != nil {
		return envelope{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return envelope{}, errors.New("Agent link message has trailing JSON")
	}
	return message, nil
}

func writeEnvelope(ctx context.Context, socket *websocket.Conn, message envelope) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return socket.Write(ctx, websocket.MessageText, payload)
}

func (message envelope) validate() error {
	switch message.Kind {
	case kindHello:
		if message.RequestID != "" || message.Hello == nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid Agent hello envelope")
		}
		return message.Hello.Validate()
	case kindHelloAck:
		if message.RequestID != "" || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid Agent hello acknowledgement envelope")
		}
		return nil
	case kindAKARequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest == nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid AKA request envelope")
		}
		return message.AKARequest.Validate()
	case kindAKAResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult == nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid AKA response envelope")
		}
		return nil
	case kindModemRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest == nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid modem request envelope")
		}
		return message.ModemRequest.Validate()
	case kindModemResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult == nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid modem response envelope")
		}
		return nil
	case kindMediaRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest == nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid modem media request envelope")
		}
		return message.MediaRequest.Validate()
	case kindMediaResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult == nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid modem media response envelope")
		}
		return nil
	case kindEUICCRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || message.EUICCRequest == nil || message.EUICCResult != nil || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid eUICC profile request envelope")
		}
		return message.EUICCRequest.Validate()
	case kindEUICCResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || message.EUICCRequest != nil || message.EUICCResult == nil || !message.emptyDownload() || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid eUICC profile response envelope")
		}
		return nil
	case kindDownloadRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || message.DownloadRequest == nil || message.DownloadResult != nil || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid eUICC download request envelope")
		}
		return message.DownloadRequest.Validate()
	case kindDownloadResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || message.DownloadRequest != nil || message.DownloadResult == nil || !message.emptyDiscovery() || message.Health != nil {
			return errors.New("invalid eUICC download response envelope")
		}
		return nil
	case kindDiscoveryRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || message.DiscoveryRequest == nil || message.DiscoveryResult != nil || message.Health != nil {
			return errors.New("invalid eUICC discovery request envelope")
		}
		return message.DiscoveryRequest.Validate()
	case kindDiscoveryResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || message.DiscoveryRequest != nil || message.DiscoveryResult == nil || message.Health != nil {
			return errors.New("invalid eUICC discovery response envelope")
		}
		return nil
	case kindHealth:
		if message.RequestID != "" || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.Health == nil {
			return errors.New("invalid Agent health envelope")
		}
		return message.Health.Validate()
	default:
		return errors.New("unknown Agent link message kind")
	}
}
