package agentat

import (
	"context"
	"errors"
	"time"
)

// IdentifyProfiledPort supplies the equipment identity for the original Linux
// serial-only path, where no ModemManager identity exists. The caller must first
// match a configured modem USB interface and establish its physical ownership.
func IdentifyProfiledPort(ctx context.Context, candidate Candidate, open Opener) (equipment string, err error) {
	if !candidate.USB || candidate.PhysicalID == "" || candidate.Name == "" || open == nil {
		return "", errors.New("profiled USB AT interface required")
	}
	port, err := open(candidate)
	if err != nil {
		return "", err
	}
	owner := &Owner{port: port, candidate: candidate}
	defer func() {
		if closeErr := owner.Close(); closeErr != nil {
			equipment = ""
			err = errors.Join(err, closeErr)
		}
	}()
	if _, err := owner.Exchange(ctx, "AT", 2*time.Second); err != nil {
		return "", err
	}
	response, err := owner.Exchange(ctx, "AT+CGSN", 3*time.Second)
	if err != nil {
		return "", err
	}
	for _, value := range numericIdentity.FindAllString(string(response), -1) {
		if !equipmentIDPattern.MatchString(value) {
			continue
		}
		if equipment != "" && equipment != value {
			return "", errors.New("ambiguous modem equipment identity")
		}
		equipment = value
	}
	if equipment == "" {
		return "", errors.New("modem equipment identity unavailable")
	}
	return equipment, nil
}
