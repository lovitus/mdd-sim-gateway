package agentat

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	usimAID = "A0000000871002"
	isimAID = "A0000000871004"
)

var (
	cchoResponse = regexp.MustCompile(`(?im)^\s*\+CCHO:\s*(\d+)\s*$`)
	cglaResponse = regexp.MustCompile(`(?im)^\s*\+CGLA:\s*(\d+)\s*,\s*(?:"([0-9a-fA-F]*)"|([0-9a-fA-F]+))\s*$`)
)

type SIMAKAResult struct {
	Body []byte
	SW1  byte
	SW2  byte
}

// AuthenticateAKA owns the AT port for the complete logical-channel
// transaction. Only SELECT-by-AID, one fixed 3GPP AUTHENTICATE APDU and bounded
// status recovery are possible; callers cannot supply raw AT or APDU.
func (owner *Owner) AuthenticateAKA(ctx context.Context, application string, rand16, autn16 []byte) (result SIMAKAResult, err error) {
	if !owner.capabilities.SIMAPDU || len(rand16) != 16 || len(autn16) != 16 {
		return SIMAKAResult{}, errors.New("modem SIM AKA is unavailable")
	}
	aid := usimAID
	switch strings.ToLower(strings.TrimSpace(application)) {
	case "usim":
	case "isim":
		aid = isimAID
	default:
		return SIMAKAResult{}, errors.New("unsupported modem SIM AKA application")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.port == nil {
		return SIMAKAResult{}, errors.New("AT control port is closed")
	}
	open, err := owner.exchangeLocked(ctx, `AT+CCHO="`+aid+`"`, 3*time.Second)
	if err != nil {
		return SIMAKAResult{}, fmt.Errorf("open SIM logical channel: %w", err)
	}
	match := cchoResponse.FindSubmatch(open)
	if len(match) != 2 {
		return SIMAKAResult{}, errors.New("modem omitted logical channel identity")
	}
	channel, parseErr := strconv.Atoi(string(match[1]))
	if parseErr != nil || channel < 1 || channel > 19 {
		return SIMAKAResult{}, errors.New("modem returned an invalid logical channel")
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, closeErr := owner.exchangeLocked(closeContext, fmt.Sprintf("AT+CCHC=%d", channel), 2*time.Second)
		cancel()
		if closeErr != nil {
			result = SIMAKAResult{}
			err = errors.Join(err, fmt.Errorf("close SIM logical channel: %w", closeErr))
		}
	}()

	auth := make([]byte, 0, 40)
	auth = append(auth, 0x00, 0x88, 0x00, 0x81, 0x22, 0x10)
	auth = append(auth, rand16...)
	auth = append(auth, 0x10)
	auth = append(auth, autn16...)
	return owner.transmitAPDULocked(ctx, channel, auth)
}

func (owner *Owner) transmitAPDULocked(ctx context.Context, channel int, command []byte) (SIMAKAResult, error) {
	response, err := owner.transmitAPDUOnceLocked(ctx, channel, command)
	if err != nil {
		return SIMAKAResult{}, err
	}
	if response.SW1 == 0x6C {
		corrected := append([]byte(nil), command...)
		corrected = append(corrected, response.SW2)
		response, err = owner.transmitAPDUOnceLocked(ctx, channel, corrected)
		if err != nil {
			return SIMAKAResult{}, err
		}
	}
	body := append([]byte(nil), response.Body...)
	for count := 0; (response.SW1 == 0x61 || response.SW1 == 0x9F) && count < 4; count++ {
		response, err = owner.transmitAPDUOnceLocked(ctx, channel, []byte{0x00, 0xC0, 0x00, 0x00, response.SW2})
		if err != nil {
			return SIMAKAResult{}, err
		}
		body = append(body, response.Body...)
	}
	response.Body = body
	return response, nil
}

func (owner *Owner) transmitAPDUOnceLocked(ctx context.Context, channel int, command []byte) (SIMAKAResult, error) {
	if channel < 1 || channel > 19 || len(command) < 4 || len(command) > 255 {
		return SIMAKAResult{}, errors.New("invalid typed SIM APDU operation")
	}
	wire := strings.ToUpper(hex.EncodeToString(command))
	output, err := owner.exchangeLocked(ctx, fmt.Sprintf(`AT+CGLA=%d,%d,"%s"`, channel, len(wire), wire), 3*time.Second)
	if err != nil {
		return SIMAKAResult{}, err
	}
	match := cglaResponse.FindSubmatch(output)
	if len(match) != 4 {
		return SIMAKAResult{}, errors.New("modem omitted SIM APDU response")
	}
	declared, parseErr := strconv.Atoi(string(match[1]))
	hexValue := string(match[2])
	if hexValue == "" {
		hexValue = string(match[3])
	}
	if parseErr != nil || declared != len(hexValue) || len(hexValue) < 4 || len(hexValue)%2 != 0 {
		return SIMAKAResult{}, errors.New("modem returned an invalid SIM APDU length")
	}
	raw, decodeErr := hex.DecodeString(hexValue)
	if decodeErr != nil {
		return SIMAKAResult{}, errors.New("modem returned invalid SIM APDU hex")
	}
	return SIMAKAResult{Body: append([]byte(nil), raw[:len(raw)-2]...), SW1: raw[len(raw)-2], SW2: raw[len(raw)-1]}, nil
}
