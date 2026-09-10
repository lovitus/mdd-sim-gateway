package agentsim

import (
	"bytes"
	"context"
	"github.com/damonto/euicc-go/bertlv"
	sgp22 "github.com/damonto/euicc-go/v2"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
	"testing"
)

func TestStandardDeleteUsesExactBF33WithoutRefresh(t *testing.T) {
	card := euiccCard(t, emptyProfileResponse())
	base := card.handler
	calls := 0
	card.handler = func(command []byte) ([]byte, error) {
		if len(command) > 5 && command[1] == 0xe2 && bytes.HasPrefix(command[5:], []byte{0xbf, 0x33}) {
			calls++
			var request bertlv.TLV
			if err := request.UnmarshalBinary(command[5 : 5+int(command[4])]); err != nil {
				t.Fatal(err)
			}
			if len(request.Children) != 1 || !request.Children[0].Tag.Equal(sgp22.TagICCID) {
				t.Fatal("invalid standard delete encoding")
			}
			if sgp22.ICCID(request.Children[0].Value).String() != "8944000000000000001" {
				t.Fatal("wrong profile deletion")
			}
			return []byte{0xbf, 0x33, 3, 0x80, 1, 0, 0x90, 0}, nil
		}
		return base(command)
	}
	if err := mutateEUICCProfile(context.Background(), card, nil, "8944000000000000001", agentlink.EUICCProfileDelete, ""); err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
}

func TestStandardDeleteCannotDeleteEnabledProfile(t *testing.T) {
	for _, state := range []sgp22.ProfileState{sgp22.ProfileEnabled, sgp22.ProfileDisabled} {
		t.Run(state.String(), func(t *testing.T) {
			card := euiccCard(t, profileResponse(profileTLV(t, "8944000000000000001", state, "[MDD-DELETED] test")))
			manager, err := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			writes := 0
			manager.mutateProfile = func(_ context.Context, _ Card, _ []byte, _ string, action agentlink.EUICCProfileAction, _ string) error {
				if action != agentlink.EUICCProfileDelete {
					t.Fatal("wrong action")
				}
				writes++
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insert"})
			}()
			defer func() { cancel(); <-done }()
			waitForSession(t, manager, "insert")
			result := manager.ExecuteEUICCProfile(ctx, agentlink.EUICCProfileRequest{OperationID: "delete-test", SessionGeneration: "insert", EID: testEID, ICCID: "8944000000000000001", Action: agentlink.EUICCProfileDelete, ExpectedState: agentlink.EUICCProfileDisabled})
			if state == sgp22.ProfileEnabled {
				if writes != 0 || result.Failure == nil {
					t.Fatal("enabled profile was deleted")
				}
			} else if writes != 1 || result.Outcome != agentlink.EUICCProfileRefreshPending {
				t.Fatalf("delete not dispatched: %+v", result)
			}
		})
	}
}
