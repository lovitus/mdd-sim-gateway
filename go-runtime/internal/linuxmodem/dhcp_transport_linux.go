//go:build linux

package linuxmodem

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
	"github.com/mdlayher/packet"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// The packet socket bypasses inet nftables by design. It is exclusively used
// for DHCP, bound by captured ifindex, filtered before bind, and never exported.
type dhcpTransport struct {
	net.PacketConn
	udp       net.PacketConn
	hardware  net.HardwareAddr
	mu        sync.Mutex
	requestAt map[dhcpv4.TransactionID]time.Time
}

func openDHCPTransport(ctx context.Context, iface net.Interface, source net.IP, mark uint32) (*dhcpTransport, error) {
	filter, err := bpf.Assemble([]bpf.Instruction{
		bpf.LoadAbsolute{Off: 9, Size: 1}, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 17, SkipFalse: 7},
		bpf.LoadAbsolute{Off: 6, Size: 2}, bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x3fff, SkipTrue: 5},
		bpf.LoadMemShift{Off: 0}, bpf.LoadIndirect{Off: 0, Size: 2}, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 67, SkipFalse: 2},
		bpf.LoadIndirect{Off: 2, Size: 2}, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 68, SkipTrue: 1},
		bpf.RetConstant{Val: 0}, bpf.RetConstant{Val: 2048},
	})
	if err != nil {
		return nil, err
	}
	raw, err := packet.Listen(&iface, packet.Datagram, unix.ETH_P_IP, &packet.Config{Filter: filter})
	if err != nil {
		return nil, err
	}
	transport := &dhcpTransport{PacketConn: nclient4.NewBroadcastUDPConn(raw, &net.UDPAddr{Port: 68}), hardware: append(net.HardwareAddr(nil), iface.HardwareAddr...), requestAt: make(map[dhcpv4.TransactionID]time.Time)}
	if source != nil {
		control := dhcpSocketControl(iface.Index, mark)
		config := net.ListenConfig{Control: func(network, address string, fd syscall.RawConn) error {
			if err := control(network, address, fd); err != nil {
				return err
			}
			var socketErr error
			if err := fd.Control(func(value uintptr) { socketErr = unix.SetsockoptInt(int(value), unix.SOL_SOCKET, unix.SO_BROADCAST, 1) }); err != nil {
				return err
			}
			return socketErr
		}}
		transport.udp, err = config.ListenPacket(ctx, "udp4", net.JoinHostPort(source.String(), "68"))
		if err != nil {
			raw.Close()
			return nil, err
		}
		current, err := net.InterfaceByIndex(iface.Index)
		if err != nil || current.Name != iface.Name || !bytes.Equal(current.HardwareAddr, iface.HardwareAddr) {
			transport.Close()
			return nil, errors.New("DHCP interface changed during socket binding")
		}
	}
	return transport, nil
}

// Bind by the frozen kernel index, not a name that can belong to a new device
// between the identity check and connect. Unsupported kernels fail closed.
func dhcpSocketControl(index int, mark uint32) func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var socketErr error
		err := raw.Control(func(fd uintptr) {
			if socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BINDTOIFINDEX, index); socketErr == nil {
				socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark))
			}
		})
		return errors.Join(err, socketErr)
	}
}

func (transport *dhcpTransport) ReadFrom(buffer []byte) (int, net.Addr, error) {
	for {
		n, from, err := transport.PacketConn.ReadFrom(buffer)
		if err != nil {
			return 0, nil, err
		}
		peer, ok := from.(*net.UDPAddr)
		if !ok || peer.Port != 67 {
			continue
		}
		message, err := dhcpv4.FromBytes(buffer[:n])
		if err != nil || message.OpCode != dhcpv4.OpcodeBootReply || !bytes.Equal(message.ClientHWAddr, transport.hardware) {
			continue
		}
		return n, from, nil
	}
}

func (transport *dhcpTransport) WriteTo(buffer []byte, to net.Addr) (int, error) {
	peer, ok := to.(*net.UDPAddr)
	message, err := dhcpv4.FromBytes(buffer)
	if !ok || peer.Port != 67 || peer.IP.To4() == nil || err != nil || message.OpCode != dhcpv4.OpcodeBootRequest || !bytes.Equal(message.ClientHWAddr, transport.hardware) || (message.MessageType() != dhcpv4.MessageTypeDiscover && message.MessageType() != dhcpv4.MessageTypeRequest) {
		return 0, errors.New("invalid internal DHCP transmission")
	}
	if message.MessageType() == dhcpv4.MessageTypeRequest {
		transport.mu.Lock()
		if _, exists := transport.requestAt[message.TransactionID]; !exists {
			transport.requestAt[message.TransactionID] = time.Now()
		}
		transport.mu.Unlock()
	}
	if transport.udp != nil {
		return transport.udp.WriteTo(buffer, to)
	}
	if !peer.IP.Equal(net.IPv4bcast) {
		return 0, errors.New("bootstrap DHCP must use broadcast")
	}
	return transport.PacketConn.WriteTo(buffer, to)
}

func (transport *dhcpTransport) sentAt(id dhcpv4.TransactionID) time.Time {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.requestAt[id]
}

func (transport *dhcpTransport) Close() error {
	var err error
	if transport.udp != nil {
		err = transport.udp.Close()
	}
	return errors.Join(err, transport.PacketConn.Close())
}

func (transport *dhcpTransport) SetWriteDeadline(deadline time.Time) error {
	if transport.udp != nil {
		return transport.udp.SetWriteDeadline(deadline)
	}
	return transport.PacketConn.SetWriteDeadline(deadline)
}

func (transport *dhcpTransport) SetDeadline(deadline time.Time) error {
	return errors.Join(transport.SetReadDeadline(deadline), transport.SetWriteDeadline(deadline))
}
