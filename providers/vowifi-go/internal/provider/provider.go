// SPDX-License-Identifier: AGPL-3.0-only

// Package provider adapts the pinned vowifi-go SWu implementation to one
// userspace packet session. It contains no Core state, process supervisor,
// host routing, TUN, call admission, or recovery policy.
package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	upstreamswu "github.com/boa-z/vowifi-go/engine/swu"
)

var (
	ErrInvalidConfig        = errors.New("invalid MDD vowifi-go provider config")
	ErrTunnelNotReady       = errors.New("upstream SWu tunnel is not ready")
	ErrUserspaceRequired    = errors.New("upstream SWu session is not userspace")
	ErrPacketSessionNeeded  = errors.New("upstream SWu session has no packet interface")
	ErrProviderSessionClose = errors.New("MDD vowifi-go provider session is closed")
)

const maxRejectedSessionClose = 30 * time.Second

type Config struct {
	LineID       string
	DeviceID     string
	TraceID      string
	IMSI         string
	MCC          string
	MNC          string
	EPDGAddress  string
	CloseTimeout time.Duration
}

type Info struct {
	LineID        string
	EPDGAddress   string
	LocalInnerIP  string
	RemoteInnerIP string
	DNSServers    []string
	PCSCFServers  []string
}

type Provider struct {
	manager upstreamswu.TunnelManager
}

// NewUpstream constructs the real pinned IKE/SWu manager. The returned
// Provider still performs no network operation until Open is called.
func NewUpstream(config upstreamswu.IKEPacketTunnelManagerConfig) (*Provider, error) {
	if config.SIM == nil {
		return nil, fmt.Errorf("%w: SIM AKA provider is nil", ErrInvalidConfig)
	}
	return NewWithManager(upstreamswu.NewIKEPacketTunnelManager(config))
}

// NewWithManager is the narrow test/adapter seam. Production passes the
// upstream IKE manager returned by NewUpstream.
func NewWithManager(manager upstreamswu.TunnelManager) (*Provider, error) {
	if manager == nil {
		return nil, fmt.Errorf("%w: tunnel manager is nil", ErrInvalidConfig)
	}
	return &Provider{manager: manager}, nil
}

func (provider *Provider) Open(ctx context.Context, config Config) (*Session, error) {
	if provider == nil || provider.manager == nil {
		return nil, fmt.Errorf("%w: provider is nil", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	config = normalizeConfig(config)
	if config.LineID == "" || config.DeviceID == "" ||
		(config.EPDGAddress == "" && (config.MCC == "" || config.MNC == "")) || config.IMSI == "" ||
		config.CloseTimeout > maxRejectedSessionClose {
		return nil, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base, err := provider.manager.EstablishTunnel(ctx, upstreamswu.TunnelConfig{
		DeviceID: config.DeviceID, TraceID: config.TraceID,
		Mode:        upstreamswu.DataplaneModeUserspace,
		EPDGAddress: config.EPDGAddress, IMSI: config.IMSI,
		MCC: config.MCC, MNC: config.MNC,
	})
	if err != nil {
		return nil, fmt.Errorf("establish upstream SWu tunnel: %w", err)
	}
	if base == nil {
		return nil, fmt.Errorf("%w: upstream returned nil", ErrTunnelNotReady)
	}
	result := base.Result()
	if ctx.Err() != nil {
		return nil, errors.Join(ctx.Err(), closeRejected(base, config.CloseTimeout))
	}
	if !result.IsReady() {
		return nil, errors.Join(ErrTunnelNotReady, closeRejected(base, config.CloseTimeout))
	}
	if result.Mode != upstreamswu.DataplaneModeUserspace {
		return nil, errors.Join(ErrUserspaceRequired, closeRejected(base, config.CloseTimeout))
	}
	packets, ok := base.(upstreamswu.PacketTunnelReadSession)
	if !ok {
		return nil, errors.Join(ErrPacketSessionNeeded, closeRejected(base, config.CloseTimeout))
	}
	return &Session{
		base: packets,
		info: Info{
			LineID: config.LineID, EPDGAddress: result.EPDGAddress,
			LocalInnerIP: result.LocalInnerIP, RemoteInnerIP: result.RemoteInnerIP,
			DNSServers: append([]string(nil), result.DNSServers...), PCSCFServers: append([]string(nil), result.PCSCFServers...),
		},
	}, nil
}

type Session struct {
	base    upstreamswu.PacketTunnelReadSession
	info    Info
	closed  atomic.Bool
	closeMu sync.Mutex
}

func (session *Session) Info() Info {
	if session == nil {
		return Info{}
	}
	info := session.info
	info.DNSServers = append([]string(nil), info.DNSServers...)
	info.PCSCFServers = append([]string(nil), info.PCSCFServers...)
	return info
}

func (session *Session) Send(ctx context.Context, packet []byte) error {
	if session == nil || session.base == nil || session.closed.Load() {
		return ErrProviderSessionClose
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return session.base.SendInnerPacket(ctx, append([]byte(nil), packet...))
}

func (session *Session) Receive(ctx context.Context) ([]byte, error) {
	if session == nil || session.base == nil || session.closed.Load() {
		return nil, ErrProviderSessionClose
	}
	if ctx == nil {
		ctx = context.Background()
	}
	packet, err := session.base.ReadInnerPacket(ctx)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), packet.Payload...), nil
}

func (session *Session) Close(ctx context.Context) error {
	if session == nil || session.base == nil || session.closed.Load() {
		return nil
	}
	session.closeMu.Lock()
	defer session.closeMu.Unlock()
	if session.closed.Load() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := session.base.Close(ctx); err != nil {
		return err
	}
	session.closed.Store(true)
	return nil
}

func normalizeConfig(config Config) Config {
	config.LineID = strings.TrimSpace(config.LineID)
	config.DeviceID = strings.TrimSpace(config.DeviceID)
	config.TraceID = strings.TrimSpace(config.TraceID)
	config.IMSI = strings.TrimSpace(config.IMSI)
	config.MCC = strings.TrimSpace(config.MCC)
	config.MNC = strings.TrimSpace(config.MNC)
	config.EPDGAddress = strings.TrimSpace(config.EPDGAddress)
	if config.CloseTimeout <= 0 {
		config.CloseTimeout = 5 * time.Second
	}
	return config
}

func closeRejected(session upstreamswu.TunnelSession, timeout time.Duration) error {
	closeContext, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return session.Close(closeContext)
}
