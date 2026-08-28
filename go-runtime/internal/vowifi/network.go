// Package vowifi defines the provider-neutral boundary for an entirely
// userspace VoWiFi data path. It contains no host interface, route, TUN, or
// network-namespace operation.
package vowifi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
)

type Packet struct {
	Payload []byte
}

type NetworkConfig struct {
	Addresses []netip.Prefix
	DNS       []netip.Addr
	MTU       int
}

// OuterPacketDialer creates only the physical ePDG transport. A provider may
// implement it with a direct socket or a selected country egress proxy.
type OuterPacketDialer interface {
	DialPacket(ctx context.Context, endpoint netip.AddrPort) (net.PacketConn, error)
}

// SIMAuthenticator keeps AKA material behind the Agent boundary. The VoWiFi
// runtime receives responses, never a reusable SIM secret.
type SIMAuthenticator interface {
	Authenticate(ctx context.Context, challenge []byte) ([]byte, error)
}

// PacketSession is the decrypted inner-IP boundary of one SWu session.
type PacketSession interface {
	ReadPacket(ctx context.Context) (Packet, error)
	WritePacket(ctx context.Context, packet Packet) error
	NetworkConfig() NetworkConfig
	Close() error
}

type EPDGConnector interface {
	Connect(ctx context.Context, outer OuterPacketDialer, sim SIMAuthenticator) (PacketSession, error)
}

// UserNetwork exposes ordinary Go connection APIs backed by an in-process IP
// stack. IMS code cannot ask it to change a host route or create an interface.
type UserNetwork interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error)
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	Close() error
}

type NetworkFactory interface {
	Open(ctx context.Context, packets PacketSession) (UserNetwork, error)
}

type IMSRegistration interface {
	Close() error
}

type IMSRegistrar interface {
	Register(ctx context.Context, network UserNetwork) (IMSRegistration, error)
}

type Dependencies struct {
	EPDG    EPDGConnector
	Outer   OuterPacketDialer
	SIM     SIMAuthenticator
	Network NetworkFactory
	IMS     IMSRegistrar
}

type Stage string

const (
	StageEPDG      Stage = "epdg"
	StageUserStack Stage = "userspace_stack"
	StageIMS       Stage = "ims"
)

type StageError struct {
	Stage Stage
	Err   error
}

func (e *StageError) Error() string { return fmt.Sprintf("%s: %v", e.Stage, e.Err) }
func (e *StageError) Unwrap() error { return e.Err }

type Session struct {
	once         sync.Once
	registration IMSRegistration
	network      UserNetwork
	packets      PacketSession
	closeErr     error
}

// Open performs one bounded attempt. Retry/backoff belongs to the recovery
// policy and never restarts this process.
func Open(ctx context.Context, dependencies Dependencies) (*Session, error) {
	if dependencies.EPDG == nil || dependencies.Outer == nil || dependencies.SIM == nil ||
		dependencies.Network == nil || dependencies.IMS == nil {
		return nil, &StageError{Stage: StageEPDG, Err: errors.New("incomplete dependencies")}
	}
	packets, err := dependencies.EPDG.Connect(ctx, dependencies.Outer, dependencies.SIM)
	if err != nil {
		return nil, &StageError{Stage: StageEPDG, Err: err}
	}
	if packets == nil {
		return nil, &StageError{Stage: StageEPDG, Err: errors.New("connector returned no packet session")}
	}
	network, err := dependencies.Network.Open(ctx, packets)
	if err != nil {
		return nil, &StageError{Stage: StageUserStack, Err: errors.Join(err, packets.Close())}
	}
	if network == nil {
		return nil, &StageError{Stage: StageUserStack,
			Err: errors.Join(errors.New("factory returned no userspace network"), packets.Close())}
	}
	registration, err := dependencies.IMS.Register(ctx, network)
	if err != nil {
		return nil, &StageError{Stage: StageIMS, Err: errors.Join(err, network.Close(), packets.Close())}
	}
	if registration == nil {
		return nil, &StageError{Stage: StageIMS,
			Err: errors.Join(errors.New("registrar returned no IMS session"), network.Close(), packets.Close())}
	}
	return &Session{registration: registration, network: network, packets: packets}, nil
}

func (s *Session) Network() UserNetwork { return s.network }

func (s *Session) Close() error {
	s.once.Do(func() {
		s.closeErr = errors.Join(s.registration.Close(), s.network.Close(), s.packets.Close())
	})
	return s.closeErr
}
