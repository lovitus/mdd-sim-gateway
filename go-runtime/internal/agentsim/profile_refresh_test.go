package agentsim

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/damonto/euicc-go/apdu"
	"github.com/damonto/euicc-go/bertlv"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func TestPCSCProfileStateRequestsDoNotRequireCATRefresh(t *testing.T) {
	for _, action := range []agentlink.EUICCProfileAction{agentlink.EUICCProfileEnable, agentlink.EUICCProfileDisable} {
		t.Run(string(action), func(t *testing.T) {
			card := euiccCard(t, emptyProfileResponse())
			base := card.handler
			calls := 0
			tag := byte(0x31)
			if action == agentlink.EUICCProfileDisable {
				tag = 0x32
			}
			card.handler = func(command []byte) ([]byte, error) {
				if len(command) > 5 && command[1] == 0xE2 && bytes.HasPrefix(command[5:], []byte{0xBF, tag}) {
					calls++
					var request bertlv.TLV
					if err := request.UnmarshalBinary(command[5 : 5+int(command[4])]); err != nil {
						t.Fatal(err)
					}
					refresh := request.First(bertlv.ContextSpecific.Primitive(1))
					if refresh == nil || !bytes.Equal(refresh.Value, []byte{0}) {
						t.Fatal("PC/SC mutation requested modem CAT REFRESH")
					}
					return []byte{0xBF, tag, 0x03, 0x80, 0x01, 0x00, 0x90, 0x00}, nil
				}
				return base(command)
			}
			if err := mutateEUICCProfile(context.Background(), card, nil, "8944000000000000001", action, ""); err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestProfileUncertaintyPreservesStatusWithoutAPDUData(t *testing.T) {
	err := fmt.Errorf("private card detail: %w", &apdu.StatusError{Status: 0x6a80})
	if got := uncertainProfileCode(err); got != "euicc_profile_apdu_6a80" {
		t.Fatal(got)
	}
}
