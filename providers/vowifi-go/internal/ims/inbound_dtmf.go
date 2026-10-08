// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/boa-z/vowifi-go/runtimehost/voicehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/media"
)

// Only advertise the peer's offered 8 kHz event mapping. A re-INVITE may omit
// events temporarily, but cannot repurpose the mapping of an established call.
func inboundDTMF(body []byte, audioPayload int, established *voicehost.SDPCodec) (voicehost.SDPCodec, uint16, error) {
	description, err := voicehost.ParseSDPMediaDescription(body)
	if err != nil {
		return voicehost.SDPCodec{}, 0, err
	}
	found := false
	for _, codec := range description.Codecs {
		if codec.Payload == audioPayload || codec.Payload < 96 || codec.Payload > 127 ||
			codec.ClockRate != media.SampleRate || codec.Channels > 1 ||
			!strings.EqualFold(codec.EncodingName, voicehost.SDPCodecTelephoneEvent) {
			continue
		}
		if !slices.Contains(description.Info.Payloads, codec.Payload) {
			continue
		}
		events := dtmfCodecEvents(codec)
		if events == 0 {
			continue
		}
		found = true
		selected := voicehost.NewSDPTelephoneEventCodec(codec.Payload, media.SampleRate)
		var names []string
		for event := 0; event < 16; event++ {
			if events&(1<<event) != 0 {
				names = append(names, strconv.Itoa(event))
			}
		}
		selected.FMTP = strings.Join(names, ",")
		if established == nil || selected == *established {
			return selected, events, nil
		}
	}
	if found && established != nil {
		return voicehost.SDPCodec{}, 0, fmt.Errorf("%w: telephone-event mapping changed", ErrMediaNegotiation)
	}
	return voicehost.SDPCodec{}, 0, nil
}
