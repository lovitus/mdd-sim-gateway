// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/messaging"
	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/emiago/sipgo/sip"
)

func isSMSReport(request voiceclient.SIPIncomingRequest, response voicehost.IMSInboundWireResponse) bool {
	if !strings.EqualFold(request.Method, "MESSAGE") || len(response.Body) == 0 {
		return false
	}
	for name, value := range response.Headers {
		if strings.EqualFold(name, "Content-Type") {
			kind, _, _ := strings.Cut(value, ";")
			return strings.EqualFold(strings.TrimSpace(kind), messaging.IMS3GPPSMSContentType) || strings.EqualFold(strings.TrimSpace(kind), "message/cpim")
		}
	}
	return false
}

func (inbound *inboundMessaging) buildSMSReport(request voiceclient.SIPIncomingRequest, response voicehost.IMSInboundWireResponse) (*voiceclient.SIPRequestMessage, error) {
	if inbound.server.CarrierTransport == nil {
		return nil, errors.New("SMS report transport unavailable")
	}
	// P-Asserted-Identity has the same name-address/list grammar as Contact.
	// Reuse sipgo's parser, including quoted commas and repeated header lines.
	parser := sip.HeadersParser{"p-asserted-identity": sip.DefaultHeadersParser()["contact"]}
	var gateway, callID string
	for name, values := range request.Headers {
		if strings.EqualFold(name, "Call-ID") && len(values) == 1 {
			callID = strings.TrimSpace(values[0])
		}
		if !strings.EqualFold(name, "P-Asserted-Identity") {
			continue
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return nil, errors.New("invalid SMS gateway identity")
			}
			headers, err := parser.ParseHeader(nil, []byte("P-Asserted-Identity: "+value))
			if err != nil {
				return nil, errors.New("invalid SMS gateway identity")
			}
			for _, header := range headers {
				address, ok := header.(*sip.ContactHeader)
				if !ok || (!strings.EqualFold(address.Address.Scheme, "sip") && !strings.EqualFold(address.Address.Scheme, "sips")) {
					continue
				}
				uri := address.Address.String()
				if address.Address.Host == "" || (gateway != "" && gateway != uri) {
					return nil, errors.New("ambiguous SMS gateway identity")
				}
				gateway = uri
			}
		}
	}
	if gateway == "" || callID == "" || strings.ContainsAny(callID, "\r\n") {
		return nil, errors.New("SMS report missing asserted gateway or transaction identity")
	}
	report, err := voiceclient.BuildMessageRequest(voiceclient.DialogRequestConfig{
		Profile: inbound.server.Profile, Registration: inbound.server.Registration,
		ContactURI: inbound.server.ContactURI, LocalTag: inbound.server.LocalTag,
		RemoteURI: gateway, CallID: "sms-report-" + rand.Text(), CSeq: 1,
		UserAgent: inbound.server.UserAgent,
	}, response.Headers["Content-Type"], response.Body)
	if err != nil {
		return nil, err
	}
	report.Headers["In-Reply-To"] = callID
	return &report, nil
}

func incomingSMSIdentity(incoming *messaging.IncomingSMS) (string, error) {
	// Content alone is not identity. Preserve SCTS and protocol/UDH metadata,
	// but exclude transient transport identifiers and delivery-state flags.
	identity, err := json.Marshal(struct {
		Sender, Recipient, Content, Timestamp string
		ProtocolID, DataCodingScheme          byte
		UserDataHeader                        bool
		Header                                []byte
	}{incoming.Sender, incoming.Recipient, incoming.Content, incoming.Timestamp.UTC().Format(time.RFC3339Nano),
		incoming.ProtocolID, incoming.DataCodingScheme, incoming.UserDataHeader, incoming.UserDataHeaderInfo.Raw})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ims-sms-v1:%x", sha256.Sum256(identity)), nil
}
