// SPDX-License-Identifier: AGPL-3.0-only

package ims

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/boa-z/vowifi-go/runtimehost/voiceclient"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/imssec"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/usernet"
)

type userspaceSecurityInstaller struct {
	stack *usernet.Stack
}

func (installer userspaceSecurityInstaller) InstallSecurityPlanRequest(ctx context.Context, request voiceclient.IMSSecurityAssociationInstallRequest) error {
	if installer.stack == nil {
		return ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan := request.Plan
	if !strings.EqualFold(strings.TrimSpace(plan.Protocol), voiceclient.DefaultSecurityProtocol) {
		return fmt.Errorf("%w: unsupported security protocol %q", ErrInvalidConfig, plan.Protocol)
	}
	mode := strings.ToLower(strings.TrimSpace(plan.Mode))
	if mode != "trans" && mode != "transport" {
		return fmt.Errorf("%w: unsupported security mode %q", ErrInvalidConfig, plan.Mode)
	}
	local, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(request.LocalEndpoint.Address), "[]"))
	if err != nil {
		return fmt.Errorf("%w: invalid local security address", ErrInvalidConfig)
	}
	remote, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(request.RemoteEndpoint.Address), "[]"))
	if err != nil {
		return fmt.Errorf("%w: invalid remote security address", ErrInvalidConfig)
	}
	if plan.PortClient <= 0 || plan.PortClient > 65535 || plan.PortServer <= 0 || plan.PortServer > 65535 {
		return fmt.Errorf("%w: invalid protected port", ErrInvalidConfig)
	}
	confidentialityKey := []byte(nil)
	if strings.EqualFold(strings.TrimSpace(plan.EncryptionAlgorithm), voiceclient.SecurityEncryptionAlgorithmAES) {
		confidentialityKey = request.AKA.CK
	}
	protector, err := imssec.New(imssec.Config{
		LocalAddress: local, RemoteAddress: remote,
		LocalPort: uint16(plan.PortClient), RemotePort: uint16(plan.PortServer),
		SPIClient: plan.SPIClient, SPIServer: plan.SPIServer,
		Authentication: plan.Algorithm, Encryption: plan.EncryptionAlgorithm,
		IntegrityKey: request.AKA.IK, ConfidentialityKey: confidentialityKey,
	})
	if err != nil {
		return err
	}
	return installer.stack.SetPacketProtector(protector)
}

var _ voiceclient.SecurityPlanRequestInstaller = userspaceSecurityInstaller{}
