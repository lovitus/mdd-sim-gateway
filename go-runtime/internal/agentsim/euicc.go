package agentsim

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/damonto/euicc-go/lpa"
	sgp22 "github.com/damonto/euicc-go/v2"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

var euiccInitialize = []byte{0x80, 0xAA, 0x00, 0x00, 0x0A, 0xA9, 0x08, 0x81, 0x00, 0x82, 0x01, 0x01, 0x83, 0x01, 0x07}

// inspectEUICC performs only read-only ES10 operations on the Card already
// owned by this attachment session. A partial EID remains useful when profile
// enumeration fails, while ProfilesAvailable prevents treating that failure
// as an empty eUICC.
func inspectEUICC(ctx context.Context, card Card) (fact *agentlink.EUICCFact, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("eUICC library panic: %v", recovered)
		}
	}()
	channel := &euiccCardChannel{ctx: ctx, card: card}
	client, err := lpa.New(&lpa.Options{
		Channel: channel,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	eidBytes, err := client.EID()
	if err != nil {
		return nil, err
	}
	eid := hex.EncodeToString(eidBytes)
	if len(eid) != 32 || !numeric(eid) {
		return nil, errors.New("eUICC returned an invalid EID")
	}
	fact = &agentlink.EUICCFact{EID: eid, Profiles: []agentlink.EUICCProfileFact{}}
	profiles, err := client.ListProfile(nil, nil)
	if err != nil {
		return fact, err
	}
	for _, profile := range profiles {
		if profile == nil {
			return fact, errors.New("eUICC returned an empty profile record")
		}
		iccid := profile.ICCID.String()
		if iccid == "" || !numeric(iccid) {
			return fact, errors.New("eUICC returned an invalid profile ICCID")
		}
		state := agentlink.EUICCProfileState(profile.ProfileState.String())
		if state != agentlink.EUICCProfileEnabled && state != agentlink.EUICCProfileDisabled {
			return fact, errors.New("eUICC returned an invalid profile state")
		}
		if !validProfileText(profile.ProfileNickname) || !validProfileText(profile.ServiceProviderName) ||
			!validProfileText(profile.ProfileName) {
			return fact, errors.New("eUICC returned invalid profile display metadata")
		}
		fact.Profiles = append(fact.Profiles, agentlink.EUICCProfileFact{
			ICCID: iccid, State: state, Nickname: profile.ProfileNickname,
			ServiceProviderName: profile.ServiceProviderName, ProfileName: profile.ProfileName,
		})
	}
	sort.Slice(fact.Profiles, func(left, right int) bool { return fact.Profiles[left].ICCID < fact.Profiles[right].ICCID })
	for index := 1; index < len(fact.Profiles); index++ {
		if fact.Profiles[index-1].ICCID == fact.Profiles[index].ICCID {
			return fact, errors.New("eUICC returned duplicate profile ICCIDs")
		}
	}
	fact.ProfilesAvailable = true
	fact.ProfileManagement = true
	return fact, nil
}

func mutateEUICCProfile(ctx context.Context, card Card, iccid string,
	action agentlink.EUICCProfileAction) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("eUICC library panic: %v", recovered)
		}
	}()
	identifier, err := sgp22.NewICCID(iccid)
	if err != nil {
		return err
	}
	client, err := lpa.New(&lpa.Options{
		Channel: &euiccCardChannel{ctx: ctx, card: card},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	switch action {
	case agentlink.EUICCProfileEnable:
		return client.EnableProfile(identifier, true)
	case agentlink.EUICCProfileDisable:
		return client.DisableProfile(identifier, true)
	default:
		return errors.New("unsupported eUICC profile action")
	}
}

func validProfileText(value string) bool { return len(value) <= 256 && utf8.ValidString(value) }

func classifyEUICCProfileError(err error) *agentlink.RemoteError {
	if errors.Is(err, sgp22.ErrCatBusy) {
		return &agentlink.RemoteError{Kind: "not_ready", Code: "euicc_cat_busy", Retryable: true}
	}
	switch strings.TrimSpace(err.Error()) {
	case "iccid or aid not found":
		return &agentlink.RemoteError{Kind: "rejected", Code: "euicc_profile_not_found"}
	case "profile not in disabled state", "profile not in enabled state":
		return &agentlink.RemoteError{Kind: "conflict", Code: "euicc_profile_state_changed"}
	case "disallowed by policy":
		return &agentlink.RemoteError{Kind: "rejected", Code: "euicc_profile_policy_rejected"}
	case "wrong profile re-enabling":
		return &agentlink.RemoteError{Kind: "rejected", Code: "euicc_profile_reenable_rejected"}
	default:
		return nil
	}
}

func numeric(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}

// euiccCardChannel adapts the current session's Card to euicc-go without
// opening a second PC/SC connection or taking ownership of the physical card.
type euiccCardChannel struct {
	ctx     context.Context
	card    Card
	channel byte
}

func (channel *euiccCardChannel) Connect() error {
	response, err := channel.Transmit(euiccInitialize)
	if err != nil {
		return err
	}
	if !statusOKOrMore(response) {
		return fmt.Errorf("initialize eUICC: %X", response)
	}
	return nil
}

func (channel *euiccCardChannel) Disconnect() error { return nil }

func (channel *euiccCardChannel) OpenLogicalChannel(aid []byte) (byte, error) {
	response, err := channel.Transmit([]byte{0x00, 0x70, 0x00, 0x00, 0x01})
	if err != nil {
		return 0, err
	}
	if len(response) < 3 || !statusOK(response) || response[0] == 0 || response[0] > 19 {
		return 0, fmt.Errorf("open eUICC logical channel: %X", response)
	}
	logical := response[0]
	cla := logical
	if logical >= 4 {
		cla = 0x40 | logical - 4
	}
	command := append([]byte{cla, 0xA4, 0x04, 0x00, byte(len(aid))}, aid...)
	selected, err := channel.Transmit(command)
	if err != nil {
		return 0, err
	}
	if !statusOKOrMore(selected) {
		_ = channel.closeLogicalChannel(logical)
		return 0, fmt.Errorf("select eUICC application: %X", selected)
	}
	channel.channel = logical
	return logical, nil
}

func (channel *euiccCardChannel) Transmit(command []byte) ([]byte, error) {
	if err := channel.ctx.Err(); err != nil {
		return nil, err
	}
	return channel.card.Transmit(append([]byte(nil), command...))
}

func (channel *euiccCardChannel) CloseLogicalChannel(logical byte) error {
	if logical == 0 || logical != channel.channel {
		return errors.New("invalid eUICC logical channel")
	}
	err := channel.closeLogicalChannel(logical)
	if err == nil {
		channel.channel = 0
	}
	return err
}

func (channel *euiccCardChannel) closeLogicalChannel(logical byte) error {
	response, err := channel.Transmit([]byte{0x00, 0x70, 0x80, logical, 0x00})
	if err != nil {
		return err
	}
	if !statusOK(response) {
		return fmt.Errorf("close eUICC logical channel: %X", response)
	}
	return nil
}

func statusOK(response []byte) bool {
	return len(response) >= 2 && response[len(response)-2] == 0x90 && response[len(response)-1] == 0x00
}

func statusOKOrMore(response []byte) bool {
	return statusOK(response) || len(response) >= 2 && response[len(response)-2] == 0x61
}
