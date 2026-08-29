package agentat

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type SIMPINState string

const (
	SIMPINUnknown     SIMPINState = "unknown"
	SIMPINNotRequired SIMPINState = "not_required"
	SIMPINRequired    SIMPINState = "pin_required"
	SIMPINPUKRequired SIMPINState = "puk_required"
	SIMPINOtherLock   SIMPINState = "other_lock"
)

type SIMPINStatus struct {
	State             SIMPINState
	CardID            string
	AttemptsRemaining *uint32
}

type SIMPINResult struct {
	Attempted bool
	Status    SIMPINStatus
}

var (
	cpinPattern  = regexp.MustCompile(`(?m)^\s*\+CPIN:\s*([^\r\n]+)\s*$`)
	qccidPattern = regexp.MustCompile(`(?m)^\s*\+QCCID:\s*([0-9]{18,22})\s*$`)
	qpincPattern = regexp.MustCompile(`(?m)^\s*\+QPINC:\s*"SC"\s*,\s*([0-9]+)\s*,\s*([0-9]+)\s*$`)
	iccidPattern = regexp.MustCompile(`^[0-9]{18,22}$`)
)

func (owner *Owner) SIMPINStatus(ctx context.Context) (SIMPINStatus, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.simPINStatusLocked(ctx, false)
}

func (owner *Owner) SIMPINStatusFull(ctx context.Context) (SIMPINStatus, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.simPINStatusLocked(ctx, true)
}

func (owner *Owner) simPINStatusLocked(ctx context.Context, includeReadyCounter bool) (SIMPINStatus, error) {
	response, err := owner.exchangeLocked(ctx, "AT+CPIN?", 3*time.Second)
	if err != nil {
		return SIMPINStatus{}, fmt.Errorf("read SIM PIN state: %w", err)
	}
	match := cpinPattern.FindSubmatch(response)
	if len(match) != 2 {
		return SIMPINStatus{}, errors.New("SIM PIN state response was not recognized")
	}
	status := SIMPINStatus{State: parseSIMPINState(string(match[1]))}
	if status.State == SIMPINUnknown {
		return SIMPINStatus{}, errors.New("SIM PIN state response was empty")
	}
	identity, err := owner.exchangeLocked(ctx, "AT+QCCID", 3*time.Second)
	if err != nil {
		return SIMPINStatus{}, fmt.Errorf("read locked SIM identity: %w", err)
	}
	identityMatch := qccidPattern.FindSubmatch(identity)
	if len(identityMatch) != 2 {
		return SIMPINStatus{}, errors.New("SIM ICCID response was not recognized")
	}
	status.CardID = string(identityMatch[1])
	if status.State != SIMPINRequired && status.State != SIMPINPUKRequired &&
		!(includeReadyCounter && status.State == SIMPINNotRequired) {
		return status, nil
	}
	retries, err := owner.exchangeLocked(ctx, "AT+QPINC?", 3*time.Second)
	if err != nil {
		return status, fmt.Errorf("read SIM PIN retry counter: %w", err)
	}
	retryMatch := qpincPattern.FindSubmatch(retries)
	if len(retryMatch) != 3 {
		return status, errors.New("SIM PIN retry counter response was not recognized")
	}
	value, err := strconv.ParseUint(string(retryMatch[1]), 10, 32)
	if err != nil || value > 255 {
		return status, errors.New("SIM PIN retry counter was invalid")
	}
	remaining := uint32(value)
	status.AttemptsRemaining = &remaining
	return status, nil
}

func parseSIMPINState(value string) SIMPINState {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "READY":
		return SIMPINNotRequired
	case "SIM PIN":
		return SIMPINRequired
	case "SIM PUK":
		return SIMPINPUKRequired
	case "":
		return SIMPINUnknown
	default:
		return SIMPINOtherLock
	}
}

func (owner *Owner) EnterSIMPIN(ctx context.Context, cardID, pin string) (SIMPINResult, error) {
	if !iccidPattern.MatchString(cardID) || len(pin) < 4 || len(pin) > 8 {
		return SIMPINResult{}, errors.New("invalid SIM PIN request")
	}
	for _, character := range pin {
		if character < '0' || character > '9' {
			return SIMPINResult{}, errors.New("invalid SIM PIN request")
		}
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	before, err := owner.simPINStatusLocked(ctx, false)
	if err != nil {
		return SIMPINResult{Status: before}, err
	}
	if before.CardID != cardID {
		return SIMPINResult{Status: before}, errors.New("SIM identity changed before PIN entry")
	}
	if before.State != SIMPINRequired {
		return SIMPINResult{Status: before}, errors.New("SIM does not require PIN1")
	}
	if before.AttemptsRemaining == nil || *before.AttemptsRemaining < 2 {
		return SIMPINResult{Status: before}, errors.New("SIM PIN retry counter is too low or unavailable")
	}
	result := SIMPINResult{Attempted: true, Status: before}
	if _, err := owner.exchangeLocked(ctx, `AT+CPIN="`+pin+`"`, 5*time.Second); err != nil {
		if after, statusErr := owner.simPINStatusLocked(ctx, false); statusErr == nil {
			result.Status = after
		}
		return result, fmt.Errorf("enter SIM PIN: %w", err)
	}
	after, err := owner.simPINStatusLocked(ctx, false)
	result.Status = after
	if err != nil {
		return result, fmt.Errorf("verify SIM PIN result: %w", err)
	}
	if after.CardID != cardID || after.State != SIMPINNotRequired {
		return result, errors.New("SIM PIN entry was not confirmed")
	}
	return result, nil
}
