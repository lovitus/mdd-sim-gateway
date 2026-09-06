package agentlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/coder/websocket"
)

const (
	agentFeaturesHeader        = "X-MDD-Agent-Features"
	agentCapabilitiesHeader    = "X-MDD-Agent-Capabilities"
	modemEventsFeature         = "modem-events-v1"
	modemPolicyFeature         = "modem-policy-v1"
	modemSIMAPDUPrepareFeature = "modem-sim-apdu-prepare-v1"
	modemDataRenewFeature      = "modem-data-renew-v1"
	modemSMSSessionFeature     = "modem-sms-session-v1"
	modemRecoveryFeature       = "modem-recovery-v1"
	simPINFeature              = "sim-pin-v1"
	readerReadbackFeature      = "reader-readback-v1"
	agentHostHealthFeature     = "agent-host-health-v1"
)

func featureEnabled(header, feature string) bool {
	for _, value := range strings.Split(header, ",") {
		if strings.TrimSpace(value) == feature {
			return true
		}
	}
	return false
}

const (
	kindHello                  = "hello"
	kindHelloAck               = "hello_ack"
	kindAKARequest             = "aka_request"
	kindAKAResponse            = "aka_response"
	kindModemRequest           = "modem_request"
	kindModemResponse          = "modem_response"
	kindSIMPINRequest          = "sim_pin_request"
	kindSIMPINResponse         = "sim_pin_response"
	kindModemRecoveryRequest   = "modem_recovery_request"
	kindModemRecoveryResponse  = "modem_recovery_response"
	kindMediaRequest           = "modem_media_request"
	kindMediaResponse          = "modem_media_response"
	kindDataRequest            = "modem_data_request"
	kindDataResponse           = "modem_data_response"
	kindPolicyRequest          = "modem_policy_request"
	kindPolicyResponse         = "modem_policy_response"
	kindRawUSBRequest          = "raw_usb_request"
	kindRawUSBResponse         = "raw_usb_response"
	kindEUICCRequest           = "euicc_profile_request"
	kindEUICCResponse          = "euicc_profile_response"
	kindDownloadRequest        = "euicc_download_request"
	kindDownloadResponse       = "euicc_download_response"
	kindDiscoveryRequest       = "euicc_discovery_request"
	kindDiscoveryResponse      = "euicc_discovery_response"
	kindNotificationRequest    = "euicc_notification_request"
	kindNotificationResponse   = "euicc_notification_response"
	kindProvisionRequest       = "provision_request"
	kindProvisionResponse      = "provision_response"
	kindReaderReadbackRequest  = "reader_readback_request"
	kindReaderReadbackResponse = "reader_readback_response"
	kindHealth                 = "health"
	kindModemEvent             = "modem_event"
	kindModemEventAck          = "modem_event_ack"
)

type envelope struct {
	Kind                  string                     `json:"kind"`
	RequestID             string                     `json:"request_id,omitempty"`
	Hello                 *Hello                     `json:"hello,omitempty"`
	AKARequest            *AKARequest                `json:"aka_request,omitempty"`
	AKAResult             *AKAResponse               `json:"aka_response,omitempty"`
	ModemRequest          *ModemRequest              `json:"modem_request,omitempty"`
	ModemResult           *ModemResponse             `json:"modem_response,omitempty"`
	SIMPINRequest         *SIMPINRequest             `json:"sim_pin_request,omitempty"`
	SIMPINResult          *SIMPINResponse            `json:"sim_pin_response,omitempty"`
	ModemRecoveryRequest  *ModemRecoveryRequest      `json:"modem_recovery_request,omitempty"`
	ModemRecoveryResult   *ModemRecoveryResponse     `json:"modem_recovery_response,omitempty"`
	MediaRequest          *ModemMediaRequest         `json:"modem_media_request,omitempty"`
	MediaResult           *ModemMediaResponse        `json:"modem_media_response,omitempty"`
	DataRequest           *ModemDataRequest          `json:"modem_data_request,omitempty"`
	DataResult            *ModemDataResponse         `json:"modem_data_response,omitempty"`
	PolicyRequest         *ModemPolicyRequest        `json:"modem_policy_request,omitempty"`
	PolicyResult          *ModemPolicyResponse       `json:"modem_policy_response,omitempty"`
	RawUSBRequest         *RawUSBRequest             `json:"raw_usb_request,omitempty"`
	RawUSBResult          *RawUSBResponse            `json:"raw_usb_response,omitempty"`
	EUICCRequest          *EUICCProfileRequest       `json:"euicc_profile_request,omitempty"`
	EUICCResult           *EUICCProfileResponse      `json:"euicc_profile_response,omitempty"`
	DownloadRequest       *EUICCDownloadRequest      `json:"euicc_download_request,omitempty"`
	DownloadResult        *EUICCDownloadResponse     `json:"euicc_download_response,omitempty"`
	DiscoveryRequest      *EUICCDiscoveryRequest     `json:"euicc_discovery_request,omitempty"`
	DiscoveryResult       *EUICCDiscoveryResponse    `json:"euicc_discovery_response,omitempty"`
	NotificationRequest   *EUICCNotificationRequest  `json:"euicc_notification_request,omitempty"`
	NotificationResult    *EUICCNotificationResponse `json:"euicc_notification_response,omitempty"`
	ProvisionRequest      *ProvisionRequest          `json:"provision_request,omitempty"`
	ProvisionResult       *ProvisionResponse         `json:"provision_response,omitempty"`
	ReaderReadbackRequest *ReaderReadbackRequest     `json:"reader_readback_request,omitempty"`
	ReaderReadbackResult  *ReaderReadbackResponse    `json:"reader_readback_response,omitempty"`
	Health                *HealthReport              `json:"health,omitempty"`
	ModemEvent            *ModemEvent                `json:"modem_event,omitempty"`
	ModemEventAck         *ModemEventAck             `json:"modem_event_ack,omitempty"`
}

func (message envelope) emptyModemEvent() bool {
	return message.ModemEvent == nil && message.ModemEventAck == nil
}

func (message envelope) emptyPolicy() bool {
	return message.PolicyRequest == nil && message.PolicyResult == nil
}

func (message envelope) emptyLegacy() bool {
	return message.RequestID == "" && message.Hello == nil && message.AKARequest == nil && message.AKAResult == nil &&
		message.ModemRequest == nil && message.ModemResult == nil && message.MediaRequest == nil && message.MediaResult == nil &&
		message.SIMPINRequest == nil && message.SIMPINResult == nil && message.emptyEUICC() && message.emptyDownload() && message.emptyDiscovery() && message.emptyNotification() &&
		message.emptyData() && message.emptyPolicy() && message.emptyRawUSB() && message.Health == nil
}

func (message envelope) emptySIMPIN() bool {
	return message.SIMPINRequest == nil && message.SIMPINResult == nil
}

func (message envelope) emptyModemRecovery() bool {
	return message.ModemRecoveryRequest == nil && message.ModemRecoveryResult == nil
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

func (message envelope) emptyNotification() bool {
	return message.NotificationRequest == nil && message.NotificationResult == nil
}

func (message envelope) emptyData() bool {
	return message.DataRequest == nil && message.DataResult == nil
}

func (message envelope) emptyRawUSB() bool {
	return message.RawUSBRequest == nil && message.RawUSBResult == nil
}

func (message envelope) emptyReaderReadback() bool {
	return message.ReaderReadbackRequest == nil && message.ReaderReadbackResult == nil
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
	if message.Kind != kindProvisionRequest && message.Kind != kindProvisionResponse &&
		(message.ProvisionRequest != nil || message.ProvisionResult != nil) {
		return errors.New("unexpected provision fields")
	}
	if message.Kind != kindReaderReadbackRequest && message.Kind != kindReaderReadbackResponse &&
		!message.emptyReaderReadback() {
		return errors.New("unexpected reader readback fields")
	}
	if message.Kind != kindSIMPINRequest && message.Kind != kindSIMPINResponse && !message.emptySIMPIN() {
		return errors.New("unexpected SIM PIN fields")
	}
	if message.Kind != kindModemRecoveryRequest && message.Kind != kindModemRecoveryResponse && !message.emptyModemRecovery() {
		return errors.New("unexpected modem recovery fields")
	}
	if message.Kind != kindModemEvent && message.Kind != kindModemEventAck && !message.emptyModemEvent() {
		return errors.New("unexpected modem event fields")
	}
	if message.Kind != kindPolicyRequest && message.Kind != kindPolicyResponse && !message.emptyPolicy() {
		return errors.New("unexpected modem policy fields")
	}
	switch message.Kind {
	case kindReaderReadbackRequest:
		if !validIdentifier(message.RequestID) || message.ReaderReadbackRequest == nil ||
			message.ReaderReadbackResult != nil || message.Hello != nil || message.AKARequest != nil ||
			message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil ||
			message.MediaRequest != nil || message.MediaResult != nil || message.SIMPINRequest != nil ||
			message.SIMPINResult != nil || message.PolicyRequest != nil || message.PolicyResult != nil ||
			!message.emptyData() || !message.emptyRawUSB() || !message.emptyEUICC() ||
			!message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() ||
			message.ProvisionRequest != nil || message.ProvisionResult != nil || message.Health != nil {
			return errors.New("invalid reader readback request envelope")
		}
		return message.ReaderReadbackRequest.Validate()
	case kindReaderReadbackResponse:
		if !validIdentifier(message.RequestID) || message.ReaderReadbackRequest != nil ||
			message.ReaderReadbackResult == nil || message.Hello != nil || message.AKARequest != nil ||
			message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil ||
			message.MediaRequest != nil || message.MediaResult != nil || message.SIMPINRequest != nil ||
			message.SIMPINResult != nil || message.PolicyRequest != nil || message.PolicyResult != nil ||
			!message.emptyData() || !message.emptyRawUSB() || !message.emptyEUICC() ||
			!message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() ||
			message.ProvisionRequest != nil || message.ProvisionResult != nil || message.Health != nil {
			return errors.New("invalid reader readback response envelope")
		}
		return nil
	case kindSIMPINRequest:
		if !validIdentifier(message.RequestID) || message.SIMPINRequest == nil || message.SIMPINResult != nil || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || message.DataRequest != nil || message.DataResult != nil || message.PolicyRequest != nil || message.PolicyResult != nil || message.RawUSBRequest != nil || message.RawUSBResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || message.Health != nil {
			return errors.New("invalid SIM PIN request envelope")
		}
		return message.SIMPINRequest.Validate()
	case kindSIMPINResponse:
		if !validIdentifier(message.RequestID) || message.SIMPINRequest != nil || message.SIMPINResult == nil || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || message.DataRequest != nil || message.DataResult != nil || message.PolicyRequest != nil || message.PolicyResult != nil || message.RawUSBRequest != nil || message.RawUSBResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || message.Health != nil {
			return errors.New("invalid SIM PIN response envelope")
		}
		return message.SIMPINResult.Validate()
	case kindModemRecoveryRequest:
		if !validIdentifier(message.RequestID) || message.ModemRecoveryRequest == nil || message.ModemRecoveryResult != nil ||
			message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil ||
			message.ModemRequest != nil || message.ModemResult != nil || message.SIMPINRequest != nil || message.SIMPINResult != nil ||
			message.MediaRequest != nil || message.MediaResult != nil || !message.emptyData() || !message.emptyPolicy() ||
			!message.emptyRawUSB() || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() ||
			!message.emptyNotification() || !message.emptyReaderReadback() || message.ProvisionRequest != nil ||
			message.ProvisionResult != nil || !message.emptyModemEvent() || message.Health != nil {
			return errors.New("invalid modem recovery request envelope")
		}
		return message.ModemRecoveryRequest.Validate()
	case kindModemRecoveryResponse:
		if !validIdentifier(message.RequestID) || message.ModemRecoveryRequest != nil || message.ModemRecoveryResult == nil ||
			message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil ||
			message.ModemRequest != nil || message.ModemResult != nil || message.SIMPINRequest != nil || message.SIMPINResult != nil ||
			message.MediaRequest != nil || message.MediaResult != nil || !message.emptyData() || !message.emptyPolicy() ||
			!message.emptyRawUSB() || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() ||
			!message.emptyNotification() || !message.emptyReaderReadback() || message.ProvisionRequest != nil ||
			message.ProvisionResult != nil || !message.emptyModemEvent() || message.Health != nil {
			return errors.New("invalid modem recovery response envelope")
		}
		return nil
	case kindHello:
		if message.RequestID != "" || message.Hello == nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid Agent hello envelope")
		}
		return message.Hello.Validate()
	case kindHelloAck:
		if message.RequestID != "" || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid Agent hello acknowledgement envelope")
		}
		return nil
	case kindAKARequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest == nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid AKA request envelope")
		}
		return message.AKARequest.Validate()
	case kindAKAResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult == nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid AKA response envelope")
		}
		return nil
	case kindModemRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest == nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid modem request envelope")
		}
		return message.ModemRequest.Validate()
	case kindModemResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult == nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid modem response envelope")
		}
		return nil
	case kindMediaRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest == nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid modem media request envelope")
		}
		return message.MediaRequest.Validate()
	case kindMediaResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult == nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid modem media response envelope")
		}
		return nil
	case kindDataRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || message.DataRequest == nil || message.DataResult != nil || !message.emptyRawUSB() || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || message.Health != nil {
			return errors.New("invalid modem data request envelope")
		}
		return message.DataRequest.Validate()
	case kindDataResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || message.DataRequest != nil || message.DataResult == nil || !message.emptyRawUSB() || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || message.Health != nil {
			return errors.New("invalid modem data response envelope")
		}
		return nil
	case kindPolicyRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil ||
			message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil ||
			!message.emptyData() || message.PolicyRequest == nil || message.PolicyResult != nil || !message.emptyRawUSB() ||
			!message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || message.Health != nil {
			return errors.New("invalid modem policy request envelope")
		}
		return message.PolicyRequest.Validate()
	case kindPolicyResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil ||
			message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil ||
			!message.emptyData() || message.PolicyRequest != nil || message.PolicyResult == nil || !message.emptyRawUSB() ||
			!message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || message.Health != nil {
			return errors.New("invalid modem policy response envelope")
		}
		return nil
	case kindRawUSBRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyData() || message.RawUSBRequest == nil || message.RawUSBResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || message.Health != nil {
			return errors.New("invalid raw USB request envelope")
		}
		return message.RawUSBRequest.Validate()
	case kindRawUSBResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyData() || message.RawUSBRequest != nil || message.RawUSBResult == nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || message.Health != nil {
			return errors.New("invalid raw USB response envelope")
		}
		return nil
	case kindEUICCRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || message.EUICCRequest == nil || message.EUICCResult != nil || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid eUICC profile request envelope")
		}
		return message.EUICCRequest.Validate()
	case kindEUICCResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || message.EUICCRequest != nil || message.EUICCResult == nil || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid eUICC profile response envelope")
		}
		return nil
	case kindDownloadRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || message.DownloadRequest == nil || message.DownloadResult != nil || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid eUICC download request envelope")
		}
		return message.DownloadRequest.Validate()
	case kindDownloadResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || message.DownloadRequest != nil || message.DownloadResult == nil || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid eUICC download response envelope")
		}
		return nil
	case kindDiscoveryRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || message.DiscoveryRequest == nil || message.DiscoveryResult != nil || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid eUICC discovery request envelope")
		}
		return message.DiscoveryRequest.Validate()
	case kindDiscoveryResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || message.DiscoveryRequest != nil || message.DiscoveryResult == nil || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid eUICC discovery response envelope")
		}
		return nil
	case kindNotificationRequest:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyData() || !message.emptyRawUSB() || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.NotificationRequest == nil || message.NotificationResult != nil || message.Health != nil {
			return errors.New("invalid eUICC notification request envelope")
		}
		return message.NotificationRequest.Validate()
	case kindNotificationResponse:
		if !validIdentifier(message.RequestID) || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyData() || !message.emptyRawUSB() || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || message.NotificationRequest != nil || message.NotificationResult == nil || message.Health != nil {
			return errors.New("invalid eUICC notification response envelope")
		}
		return nil
	case kindProvisionRequest:
		if !validIdentifier(message.RequestID) || message.ProvisionRequest == nil ||
			message.ProvisionResult != nil || message.Hello != nil || message.AKARequest != nil ||
			message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil ||
			message.MediaRequest != nil || message.MediaResult != nil || message.SIMPINRequest != nil ||
			message.SIMPINResult != nil || !message.emptyEUICC() || !message.emptyDownload() ||
			!message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() ||
			!message.emptyPolicy() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid provision request envelope")
		}
		return message.ProvisionRequest.Validate()
	case kindProvisionResponse:
		if !validIdentifier(message.RequestID) || message.ProvisionRequest != nil ||
			message.ProvisionResult == nil || message.Hello != nil || message.AKARequest != nil ||
			message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil ||
			message.MediaRequest != nil || message.MediaResult != nil || message.SIMPINRequest != nil ||
			message.SIMPINResult != nil || !message.emptyEUICC() || !message.emptyDownload() ||
			!message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() ||
			!message.emptyPolicy() || !message.emptyRawUSB() || message.Health != nil {
			return errors.New("invalid provision response envelope")
		}
		return message.ProvisionResult.Validate()
	case kindHealth:
		if message.RequestID != "" || message.Hello != nil || message.AKARequest != nil || message.AKAResult != nil || message.ModemRequest != nil || message.ModemResult != nil || message.MediaRequest != nil || message.MediaResult != nil || !message.emptyEUICC() || !message.emptyDownload() || !message.emptyDiscovery() || !message.emptyNotification() || !message.emptyData() || !message.emptyRawUSB() || message.Health == nil {
			return errors.New("invalid Agent health envelope")
		}
		return message.Health.Validate()
	case kindModemEvent:
		if !message.emptyLegacy() || message.ModemEvent == nil || message.ModemEventAck != nil {
			return errors.New("invalid modem event envelope")
		}
		return message.ModemEvent.Validate()
	case kindModemEventAck:
		if !message.emptyLegacy() || message.ModemEvent != nil || message.ModemEventAck == nil {
			return errors.New("invalid modem event acknowledgement envelope")
		}
		return message.ModemEventAck.Validate()
	default:
		return errors.New("unknown Agent link message kind")
	}
}
