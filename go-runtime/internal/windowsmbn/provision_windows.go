//go:build windows && (amd64 || arm64)

package windowsmbn

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agenthost"
)

// ProvisionHardware exposes the existing exclusive Windows AT owner. MBN
// profile operations remain separate durable policy operations.
func (prober *Prober) ProvisionHardware() agenthost.ProvisionHardware {
	return agentat.NewProvisionHardware(prober.at)
}
