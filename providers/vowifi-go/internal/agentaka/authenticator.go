// SPDX-License-Identifier: AGPL-3.0-only

// Package agentaka adapts Core's high-level Agent AKA broker to vowifi-go's
// SIM authenticator. It cannot issue arbitrary APDUs or select a different
// live card: every request carries the configured Agent and card generations.
package agentaka

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	swusim "github.com/boa-z/vowifi-go/engine/sim"
	"github.com/boa-z/vowifi-go/runtimehost/simauth"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const (
	defaultTimeout = 15 * time.Second
	maximumTimeout = 60 * time.Second
)

var ErrInvalidConfig = errors.New("invalid Agent AKA configuration")

type Broker interface {
	AuthenticateAKA(context.Context, string, string, agentlink.AKARequest) (agentlink.AKAResponse, error)
}

type Config struct {
	AgentID           string
	ProcessGeneration string
	SessionGeneration string
	CardID            string
	Timeout           time.Duration
}

type Authenticator struct {
	broker Broker
	config Config
}

func New(broker Broker, config Config) (*Authenticator, error) {
	config.AgentID = strings.TrimSpace(config.AgentID)
	config.ProcessGeneration = strings.TrimSpace(config.ProcessGeneration)
	config.SessionGeneration = strings.TrimSpace(config.SessionGeneration)
	config.CardID = strings.TrimSpace(config.CardID)
	if config.Timeout <= 0 {
		config.Timeout = defaultTimeout
	}
	if broker == nil || config.AgentID == "" || config.ProcessGeneration == "" ||
		config.SessionGeneration == "" || config.CardID == "" || config.Timeout > maximumTimeout {
		return nil, ErrInvalidConfig
	}
	probe := agentlink.AKARequest{
		OperationID:       "config-probe",
		SessionGeneration: config.SessionGeneration,
		CardID:            config.CardID,
		Application:       agentlink.AKAApplicationUSIM,
		RAND:              make([]byte, 16),
		AUTN:              make([]byte, 16),
	}
	if err := (agentlink.BrokerRequest{AgentID: config.AgentID, ProcessGeneration: config.ProcessGeneration, AKA: probe}).Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return &Authenticator{broker: broker, config: config}, nil
}

func (auth *Authenticator) AuthenticateAKA(request swusim.AKAAuthRequest) (swusim.AKAResult, error) {
	if auth == nil || auth.broker == nil {
		return swusim.AKAResult{}, ErrInvalidConfig
	}
	if err := request.Validate(); err != nil {
		return swusim.AKAResult{}, err
	}
	operationID, err := newOperationID()
	if err != nil {
		return swusim.AKAResult{}, err
	}
	application := agentlink.AKAApplication(request.Application)
	ctx, cancel := context.WithTimeout(context.Background(), auth.config.Timeout)
	wireRequest := agentlink.AKARequest{
		OperationID:       operationID,
		SessionGeneration: auth.config.SessionGeneration,
		CardID:            auth.config.CardID,
		Application:       application,
		RAND:              append([]byte(nil), request.RAND...),
		AUTN:              append([]byte(nil), request.AUTN...),
	}
	response, brokerErr := auth.broker.AuthenticateAKA(ctx, auth.config.AgentID, auth.config.ProcessGeneration, wireRequest)
	cancel()
	if response.OperationID != "" {
		if err := response.ValidateFor(wireRequest); err != nil {
			return swusim.AKAResult{}, fmt.Errorf("validate Agent AKA response: %w", err)
		}
	}
	if brokerErr != nil {
		return swusim.AKAResult{}, fmt.Errorf("Agent AKA broker: %w", brokerErr)
	}
	result, err := simauth.ParseUSIMAuthResponse(response.Body, response.SW1, response.SW2)
	if err != nil {
		return result, fmt.Errorf("parse Agent AKA response: %w", err)
	}
	return result, nil
}

func newOperationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate AKA operation ID: %w", err)
	}
	return "aka-" + hex.EncodeToString(value[:]), nil
}

var _ swusim.AKAAuthenticator = (*Authenticator)(nil)
