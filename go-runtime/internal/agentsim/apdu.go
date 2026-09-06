package agentsim

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const (
	usimAIDPrefix = "A0000000871002"
	isimAIDPrefix = "A0000000871004"
)

var (
	errIdentityUnavailable    = errors.New("card has no active ICCID")
	errApplicationUnavailable = errors.New("SIM application is unavailable")
)

type apduResponse struct {
	body []byte
	sw1  byte
	sw2  byte
}

type apduStatusError struct {
	operation string
	sw1       byte
	sw2       byte
}

func (failure *apduStatusError) Error() string {
	return fmt.Sprintf("%s failed with status %02X%02X", failure.operation, failure.sw1, failure.sw2)
}

func isAPDUStatus(err error) bool {
	var status *apduStatusError
	return errors.As(err, &status)
}

func readICCID(ctx context.Context, card Card) (string, error) {
	master, err := exchange(ctx, card, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F, 0x00, 0x00})
	if err != nil {
		return "", err
	}
	if !master.success() {
		return "", &apduStatusError{"select MF", master.sw1, master.sw2}
	}
	selected, err := exchange(ctx, card, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x2F, 0xE2, 0x00})
	if err != nil {
		return "", err
	}
	if !selected.success() {
		if selected.sw1 == 0x6A && selected.sw2 == 0x82 || selected.sw1 == 0x94 && selected.sw2 == 0x04 {
			return "", errIdentityUnavailable
		}
		return "", &apduStatusError{"select EF_ICCID", selected.sw1, selected.sw2}
	}
	read, err := exchange(ctx, card, []byte{0x00, 0xB0, 0x00, 0x00, 0x0A})
	if err != nil {
		return "", err
	}
	if !read.success() {
		return "", &apduStatusError{"read EF_ICCID", read.sw1, read.sw2}
	}
	if len(read.body) < 10 {
		return "", errors.New("read EF_ICCID returned a short body")
	}
	var identity strings.Builder
	for _, value := range read.body[:10] {
		identity.WriteByte("0123456789ABCDEF"[value&0x0F])
		identity.WriteByte("0123456789ABCDEF"[value>>4])
	}
	cardID := strings.TrimRight(identity.String(), "F")
	for _, character := range cardID {
		if character < '0' || character > '9' {
			return "", errors.New("EF_ICCID is not numeric BCD")
		}
	}
	if cardID == "" {
		return "", errors.New("EF_ICCID is empty")
	}
	return cardID, nil
}

func selectApplication(ctx context.Context, card Card, application agentlink.AKAApplication) error {
	prefix := usimAIDPrefix
	if application == agentlink.AKAApplicationISIM {
		prefix = isimAIDPrefix
	}
	aids, directoryErr := readDirectoryAIDs(ctx, card)
	if directoryErr != nil && !isAPDUStatus(directoryErr) {
		return directoryErr
	}
	for _, aid := range appendMatchingAIDs(aids, prefix) {
		decoded, err := hex.DecodeString(aid)
		if err != nil || len(decoded) == 0 || len(decoded) > 16 {
			continue
		}
		command := append([]byte{0x00, 0xA4, 0x04, 0x04, byte(len(decoded))}, decoded...)
		response, err := exchange(ctx, card, command)
		if err != nil {
			return err
		}
		if response.success() {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", errApplicationUnavailable, application)
}

func appendMatchingAIDs(aids []string, prefix string) []string {
	var matches []string
	for _, aid := range aids {
		if strings.HasPrefix(aid, prefix) {
			matches = append(matches, aid)
		}
	}
	// Partial DF-name selection is the standards-based fallback when EF.DIR
	// cannot be read or omits a full matching AID.
	return append(matches, prefix)
}

func readDirectoryAIDs(ctx context.Context, card Card) ([]string, error) {
	master, err := exchange(ctx, card, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x3F, 0x00, 0x00})
	if err != nil {
		return nil, err
	}
	if !master.success() {
		return nil, &apduStatusError{"select MF", master.sw1, master.sw2}
	}
	selected, err := exchange(ctx, card, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x2F, 0x00, 0x00})
	if err != nil {
		return nil, err
	}
	if !selected.success() {
		return nil, &apduStatusError{"select EF_DIR", selected.sw1, selected.sw2}
	}
	var aids []string
	for record := 1; record <= 16; record++ {
		response, err := exchange(ctx, card, []byte{0x00, 0xB2, byte(record), 0x04, 0x00})
		if err != nil || !response.success() {
			break
		}
		for _, aid := range findTLVValues(response.body, 0x4F) {
			if len(aid) > 0 && len(aid) <= 16 {
				aids = append(aids, strings.ToUpper(hex.EncodeToString(aid)))
			}
		}
	}
	return aids, nil
}

func verifyPIN(ctx context.Context, card Card, pin string, mayAttempt bool) (bool, error) {
	if len(pin) < 4 || len(pin) > 8 {
		return false, errors.New("PIN length must be 4 to 8 digits")
	}
	status, attempts, err := readPINStatus(ctx, card)
	if err != nil {
		return false, err
	}
	if status == "verified" {
		return true, nil
	}
	if !mayAttempt {
		return false, errors.New("PIN was already attempted for this card")
	}
	// Preserve at least two remaining attempts. With no PUK available, using
	// either of the final two attempts would turn a single bad configuration
	// into an unsafe recovery situation.
	if status == "blocked" || attempts == nil || *attempts <= 2 {
		return false, errors.New("PIN retry counter is too low")
	}
	body, err := pinBlock(pin)
	if err != nil {
		return false, err
	}
	command := append([]byte{0x00, 0x20, 0x00, 0x01, 0x08}, body...)
	response, err := exchange(ctx, card, command)
	if err != nil || !response.success() {
		return true, errors.New("VERIFY PIN failed")
	}
	return true, nil
}

func readPINStatus(ctx context.Context, card Card) (string, *uint32, error) {
	response, err := exchange(ctx, card, []byte{0x00, 0x20, 0x00, 0x01, 0x00})
	if err != nil {
		return "", nil, err
	}
	if response.success() {
		return "verified", nil, nil
	}
	if response.sw1 == 0x63 && response.sw2&0xF0 == 0xC0 {
		remaining := uint32(response.sw2 & 0x0F)
		if remaining == 0 {
			return "blocked", &remaining, nil
		}
		// Empty-data VERIFY reports the retry counter; it does not report
		// whether PIN1 is currently verified in the active security context.
		return "retry_counter", &remaining, nil
	}
	if response.sw1 == 0x69 && response.sw2 == 0x83 {
		remaining := uint32(0)
		return "blocked", &remaining, nil
	}
	return "", nil, &apduStatusError{"read PIN status", response.sw1, response.sw2}
}

func changePIN(ctx context.Context, card Card, oldPIN, newPIN string) error {
	if _, err := verifyPIN(ctx, card, oldPIN, true); err != nil {
		return err
	}
	oldBlock, err := pinBlock(oldPIN)
	if err != nil {
		return err
	}
	newBlock, err := pinBlock(newPIN)
	if err != nil {
		return err
	}
	response, err := exchange(ctx, card, append([]byte{0x00, 0x24, 0x00, 0x01, 0x10}, append(oldBlock, newBlock...)...))
	if err != nil {
		return err
	}
	if !response.success() {
		return errors.New("CHANGE PIN failed")
	}
	return nil
}

func setPINEnabled(ctx context.Context, card Card, pin string, enabled bool) error {
	if _, err := verifyPIN(ctx, card, pin, true); err != nil {
		return err
	}
	block, err := pinBlock(pin)
	if err != nil {
		return err
	}
	ins := byte(0x26)
	if enabled {
		ins = 0x28
	}
	response, err := exchange(ctx, card, append([]byte{0x00, ins, 0x00, 0x01, 0x08}, block...))
	if err != nil {
		return err
	}
	if !response.success() {
		return errors.New("PIN enable state change failed")
	}
	return nil
}

func pinBlock(pin string) ([]byte, error) {
	if len(pin) < 4 || len(pin) > 8 {
		return nil, errors.New("PIN length must be 4 to 8 digits")
	}
	body := bytes.Repeat([]byte{0xFF}, 8)
	for index, character := range pin {
		if character < '0' || character > '9' {
			return nil, errors.New("PIN must contain only digits")
		}
		body[index] = byte(character)
	}
	return body, nil
}

func authenticate(ctx context.Context, card Card, rand16, autn16 []byte) (apduResponse, error) {
	if len(rand16) != 16 || len(autn16) != 16 {
		return apduResponse{}, errors.New("invalid AKA challenge length")
	}
	command := []byte{0x00, 0x88, 0x00, 0x81, 0x22, 0x10}
	command = append(command, rand16...)
	command = append(command, 0x10)
	command = append(command, autn16...)
	return exchange(ctx, card, command)
}

func exchange(ctx context.Context, card Card, command []byte) (apduResponse, error) {
	if err := ctx.Err(); err != nil {
		return apduResponse{}, err
	}
	response, err := transmit(card, command)
	if err != nil {
		return apduResponse{}, err
	}
	if response.sw1 == 0x6C {
		corrected := append([]byte(nil), command...)
		if len(corrected) < 5 {
			return response, nil
		}
		lc := int(corrected[4])
		switch len(corrected) {
		case 5:
			corrected[4] = response.sw2
		case 5 + lc:
			corrected = append(corrected, response.sw2)
		case 6 + lc:
			corrected[len(corrected)-1] = response.sw2
		default:
			return response, nil
		}
		response, err = transmit(card, corrected)
		if err != nil {
			return apduResponse{}, err
		}
	}
	combined := append([]byte(nil), response.body...)
	for count := 0; (response.sw1 == 0x61 || response.sw1 == 0x9F) && count < 4; count++ {
		if err := ctx.Err(); err != nil {
			return apduResponse{}, err
		}
		response, err = transmit(card, []byte{0x00, 0xC0, 0x00, 0x00, response.sw2})
		if err != nil {
			return apduResponse{}, err
		}
		combined = append(combined, response.body...)
	}
	response.body = combined
	return response, nil
}

func transmit(card Card, command []byte) (apduResponse, error) {
	raw, err := card.Transmit(append([]byte(nil), command...))
	if err != nil {
		return apduResponse{}, err
	}
	if len(raw) < 2 {
		return apduResponse{}, errors.New("APDU response is shorter than SW1/SW2")
	}
	return apduResponse{
		body: append([]byte(nil), raw[:len(raw)-2]...), sw1: raw[len(raw)-2], sw2: raw[len(raw)-1],
	}, nil
}

func (response apduResponse) success() bool {
	return response.sw1 == 0x90 && response.sw2 == 0x00
}

func findTLVValues(data []byte, wanted byte) [][]byte {
	var values [][]byte
	for offset := 0; offset+2 <= len(data); {
		tag := data[offset]
		offset++
		length := int(data[offset])
		offset++
		if length&0x80 != 0 {
			count := length & 0x7F
			if count == 0 || count > 2 || offset+count > len(data) {
				break
			}
			length = 0
			for index := 0; index < count; index++ {
				length = length<<8 | int(data[offset+index])
			}
			offset += count
		}
		if length < 0 || offset+length > len(data) {
			break
		}
		value := data[offset : offset+length]
		if tag == wanted {
			values = append(values, append([]byte(nil), value...))
		}
		if tag&0x20 != 0 || tag == 0x61 || tag == 0x62 {
			values = append(values, findTLVValues(value, wanted)...)
		}
		offset += length
	}
	return values
}
