package agentat

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ReadHomePLMN binds the EF_AD result to an exact ICCID and IMSI while retaining
// the existing Manager owner. It is an explicit read, not an inventory probe.
func (manager *Manager) ReadHomePLMN(ctx context.Context, equipmentID, cardID, imsi string) (string, string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if cardID == "" || len(imsi) < 5 || len(imsi) > 18 || !provisionDigits.MatchString(imsi) {
		return "", "", errors.New("SIM identity required")
	}
	owned, err := manager.anyOwner(equipmentID)
	if err != nil {
		return "", "", err
	}
	call, err := owned.owner.CallStatus(ctx)
	if err != nil || !call.Authoritative || call.State != "idle" || call.VoiceCalls != 0 || call.IncomingCalls != 0 {
		return "", "", errors.New("SIM identity read requires idle modem")
	}
	before, err := owned.owner.SIMPINStatus(ctx)
	if err != nil || before.CardID != cardID || before.State != SIMPINNotRequired {
		return "", "", errors.New("SIM identity or PIN state changed")
	}
	actual, err := owned.owner.Exchange(ctx, "AT+CIMI", 3*time.Second)
	if err != nil || firstDigits(actual, len(imsi)) != imsi {
		return "", "", errors.New("SIM IMSI changed")
	}
	length, err := owned.owner.ReadMNCLength(ctx)
	if err != nil {
		return "", "", err
	}
	after, err := owned.owner.SIMPINStatus(ctx)
	if err != nil || after.CardID != cardID || after.State != SIMPINNotRequired {
		return "", "", errors.New("SIM changed during identity read")
	}
	if len(imsi) < 3+length {
		return "", "", errors.New("SIM IMSI shorter than home PLMN")
	}
	return imsi[:3], imsi[3 : 3+length], nil
}

// ReadMNCLength ports agentsim/reader_identity.go's EF_AD read onto the
// existing modem USIM channel. It never verifies a PIN or writes a SIM file.
func (owner *Owner) ReadMNCLength(ctx context.Context) (length int, err error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.capabilities.SIMAPDU || owner.port == nil {
		return 0, errors.New("modem SIM identity APDU unavailable")
	}
	open, err := owner.exchangeLocked(ctx, `AT+CCHO="`+usimAID+`"`, 3*time.Second)
	if err != nil {
		return 0, err
	}
	match := cchoResponse.FindSubmatch(open)
	if len(match) != 2 {
		return 0, errors.New("modem omitted logical channel identity")
	}
	channel, err := strconv.Atoi(string(match[1]))
	if err != nil || channel < 1 || channel > 19 {
		return 0, errors.New("invalid SIM logical channel")
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, closeErr := owner.exchangeLocked(closeContext, fmt.Sprintf("AT+CCHC=%d", channel), 2*time.Second); closeErr != nil {
			length = 0
			err = errors.Join(err, closeErr)
		}
	}()
	selected, err := owner.transmitAPDULocked(ctx, channel, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x6F, 0xAD, 0x00})
	if err != nil {
		return 0, err
	}
	if selected.SW1 != 0x90 || selected.SW2 != 0 {
		return 0, errors.New("SIM EF_AD selection unavailable")
	}
	read, err := owner.transmitAPDULocked(ctx, channel, []byte{0x00, 0xB0, 0x00, 0x00, 0x04})
	if err != nil {
		return 0, err
	}
	if read.SW1 != 0x90 || read.SW2 != 0 || len(read.Body) < 4 {
		return 0, errors.New("SIM EF_AD read unavailable")
	}
	// Same decoder as agentsim, sourced from vowifi-go 1e9c6e6a MNCLengthFromAD.
	length = int(read.Body[3] & 0x0F)
	if length != 2 && length != 3 {
		return 0, errors.New("SIM MNC length unavailable")
	}
	return length, nil
}
