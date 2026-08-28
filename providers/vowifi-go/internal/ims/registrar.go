// SPDX-License-Identifier: AGPL-3.0-only

// Package ims binds the upstream IMS registrar to one SWu userspace stack.
// It deliberately rejects alternate transports and resolvers because their
// network provenance cannot be proven by this provider.
package ims

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/boa-z/vowifi-go/runtimehost"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

var (
	ErrInvalidConfig       = errors.New("invalid userspace IMS registrar config")
	ErrUntrustedNetworking = errors.New("userspace IMS registrar rejects custom networking")
)

// Registrar owns a copy of the upstream registrar configuration and forces
// its SIP and DNS dials through one in-memory SWu stack.
type Registrar struct {
	stack *usernet.Stack
	base  runtimehost.WireIMSRegistrar
}

func NewRegistrar(stack *usernet.Stack, base runtimehost.WireIMSRegistrar) (*Registrar, error) {
	if stack == nil {
		return nil, fmt.Errorf("%w: stack is nil", ErrInvalidConfig)
	}
	if strings.TrimSpace(base.LocalAddr) != "" || base.DialContext != nil || base.Resolver != nil ||
		base.Transport != nil || base.TransportFactory != nil ||
		base.VoiceTransport != nil || base.VoiceFactory != nil ||
		base.SMSTransport != nil || base.SMSFactory != nil ||
		base.USSDTransport != nil || base.USSDFactory != nil ||
		base.SecurityPlanInstaller != nil || base.SecurityAssociationInstaller != nil ||
		base.DialContextLocal != nil {
		return nil, ErrUntrustedNetworking
	}
	base.DialContext = stack.DialContext
	base.DialContextLocal = stack.DialContextLocal
	base.SecurityAssociationInstaller = userspaceSecurityInstaller{stack: stack}
	return &Registrar{stack: stack, base: base}, nil
}

func (registrar *Registrar) RegisterIMS(ctx context.Context, config runtimehost.IMSRegistrationConfig) (runtimehost.IMSRegistrationResult, error) {
	if registrar == nil || registrar.stack == nil {
		return runtimehost.IMSRegistrationResult{}, ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return runtimehost.IMSRegistrationResult{}, err
	}
	return registrar.base.RegisterIMS(ctx, config)
}

var _ runtimehost.IMSRegistrar = (*Registrar)(nil)
