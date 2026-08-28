// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"errors"
	"strings"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
)

var ErrVoiceNotReady = errors.New("userspace IMS voice is not ready")

// NewOutboundAgent wires the registered upstream SIP flow into its dialog
// state machine. Media is intentionally not configured here: callers must not
// infer media readiness from successful signalling.
func NewOutboundAgent(registration runtimehost.IMSRegistrationResult) (*voicehost.IMSOutboundAgent, error) {
	if !registration.Registered || registration.VoiceTransport == nil ||
		strings.TrimSpace(registration.Binding.ContactURI) == "" ||
		strings.TrimSpace(registration.Profile.IMPU) == "" {
		return nil, ErrVoiceNotReady
	}
	return &voicehost.IMSOutboundAgent{
		Transport:    registration.VoiceTransport,
		Profile:      registration.Profile,
		Registration: registration.Binding,
		Domain:       registration.Profile.Domain,
		UserAgent:    registration.Profile.UserAgent,
	}, nil
}
