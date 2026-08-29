// Package egressprobe performs an explicit end-to-end UDP check through an
// already-applied country exit. It owns no route, proxy, or recovery state.
package egressprobe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maximumPacketBytes = 4096

type Result struct {
	LatencyMS        int      `json:"latency_ms"`
	Target           string   `json:"target"`
	AttemptedTargets []string `json:"attempted_targets"`
}

type target struct {
	address  netip.Addr
	question []byte
}

type pendingProbe struct {
	target target
	sentAt time.Time
}

var defaultTargets = []target{
	{address: netip.MustParseAddr("1.1.1.1"), question: dnsQuestion("cloudflare.com")},
	{address: netip.MustParseAddr("8.8.8.8"), question: dnsQuestion("google.com")},
}

// Probe verifies the complete SOCKS5 UDP path. The proxy URL must name a
// literal loopback address; callers cannot turn this diagnostic into an SSRF
// primitive by supplying an arbitrary endpoint.
func Probe(ctx context.Context, proxyURL string) (Result, error) {
	return probe(ctx, proxyURL, defaultTargets)
}

func probe(ctx context.Context, proxyURL string, targets []target) (Result, error) {
	var result Result
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || parsed.Scheme != "socks5" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return result, errors.New("country exit has an invalid SOCKS5 endpoint")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return result, errors.New("country exit has an invalid SOCKS5 endpoint")
	}
	address, err := netip.ParseAddr(host)
	if err != nil || !address.IsLoopback() {
		return result, errors.New("country exit SOCKS5 endpoint is not loopback")
	}
	if len(targets) == 0 {
		return result, errors.New("UDP probe has no targets")
	}
	for _, item := range targets {
		result.AttemptedTargets = append(result.AttemptedTargets, item.address.String())
	}

	dialer := net.Dialer{}
	control, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), port))
	if err != nil {
		return result, fmt.Errorf("connect country exit SOCKS5: %w", err)
	}
	defer control.Close()
	stopDeadline := bindDeadline(ctx, control)
	defer stopDeadline()
	if _, err := control.Write([]byte{5, 1, 0}); err != nil {
		return result, fmt.Errorf("negotiate country exit SOCKS5: %w", err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(control, selection); err != nil || selection[0] != 5 || selection[1] != 0 {
		return result, errors.New("country exit SOCKS5 rejected unauthenticated UDP probing")
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return result, fmt.Errorf("request country exit UDP association: %w", err)
	}
	relay, err := readSOCKSReply(control, address)
	if err != nil {
		return result, err
	}
	if !relay.Addr().IsLoopback() {
		return result, errors.New("country exit SOCKS5 returned a non-loopback UDP relay")
	}

	udp, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(relay))
	if err != nil {
		return result, fmt.Errorf("connect country exit UDP relay: %w", err)
	}
	defer udp.Close()
	stopUDPDeadline := bindDeadline(ctx, udp)
	defer stopUDPDeadline()
	pending := make(map[uint16]pendingProbe, len(targets))
	for _, item := range targets {
		identifier, err := randomID(pending)
		if err != nil {
			return result, err
		}
		packet := socksUDPRequest(item.address, 53, dnsQuery(identifier, item.question))
		sentAt := time.Now()
		if _, err := udp.Write(packet); err != nil {
			return result, fmt.Errorf("send country exit UDP probe: %w", err)
		}
		pending[identifier] = pendingProbe{target: item, sentAt: sentAt}
	}

	buffer := make([]byte, maximumPacketBytes)
	failures := make([]string, 0, len(targets))
	for len(pending) > 0 {
		count, err := udp.Read(buffer)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || isTimeout(err) {
				break
			}
			return result, fmt.Errorf("receive country exit UDP probe: %w", err)
		}
		remote, payload, err := parseSOCKSUDP(buffer[:count])
		if err != nil || remote.Port() != 53 || len(payload) < 12 {
			continue
		}
		identifier := binary.BigEndian.Uint16(payload[:2])
		probe, exists := pending[identifier]
		if !exists || remote.Addr() != probe.target.address {
			continue
		}
		delete(pending, identifier)
		if !validDNSAnswer(payload, identifier, probe.target.question) {
			failures = append(failures, probe.target.address.String()+": invalid DNS answer")
			continue
		}
		result.Target = probe.target.address.String()
		result.LatencyMS = max(1, int(time.Since(probe.sentAt).Round(time.Millisecond)/time.Millisecond))
		return result, nil
	}
	for _, probe := range pending {
		failures = append(failures, probe.target.address.String()+": timed out")
	}
	if len(failures) == 0 {
		failures = append(failures, "no valid DNS answer")
	}
	return result, errors.New("UDP probes failed: " + strings.Join(failures, "; "))
}

func bindDeadline(ctx context.Context, connection interface{ SetDeadline(time.Time) error }) func() {
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-done:
		}
	}()
	return func() { close(done) }
}

func readSOCKSReply(reader io.Reader, fallback netip.Addr) (netip.AddrPort, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return netip.AddrPort{}, fmt.Errorf("read country exit UDP association: %w", err)
	}
	if header[0] != 5 || header[1] != 0 || header[2] != 0 {
		return netip.AddrPort{}, fmt.Errorf("country exit SOCKS5 rejected UDP association (code %d)", header[1])
	}
	address, err := readSOCKSAddress(reader, header[3])
	if err != nil {
		return netip.AddrPort{}, err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return netip.AddrPort{}, errors.New("country exit SOCKS5 returned a truncated UDP relay")
	}
	port := binary.BigEndian.Uint16(portBytes)
	if !address.IsValid() || address.IsUnspecified() {
		address = fallback
	}
	if port == 0 {
		return netip.AddrPort{}, errors.New("country exit SOCKS5 returned an invalid UDP relay port")
	}
	return netip.AddrPortFrom(address, port), nil
}

func readSOCKSAddress(reader io.Reader, atyp byte) (netip.Addr, error) {
	size := 0
	switch atyp {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(reader, length); err != nil || length[0] == 0 {
			return netip.Addr{}, errors.New("country exit SOCKS5 returned an invalid UDP relay")
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, name); err != nil {
			return netip.Addr{}, errors.New("country exit SOCKS5 returned a truncated UDP relay")
		}
		address, err := netip.ParseAddr(string(name))
		if err != nil {
			return netip.Addr{}, errors.New("country exit SOCKS5 returned a non-literal UDP relay")
		}
		return address, nil
	default:
		return netip.Addr{}, errors.New("country exit SOCKS5 returned an invalid UDP relay address type")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return netip.Addr{}, errors.New("country exit SOCKS5 returned a truncated UDP relay")
	}
	address, ok := netip.AddrFromSlice(payload)
	if !ok {
		return netip.Addr{}, errors.New("country exit SOCKS5 returned an invalid UDP relay")
	}
	return address.Unmap(), nil
}

func socksUDPRequest(address netip.Addr, port uint16, payload []byte) []byte {
	address = address.Unmap()
	packet := []byte{0, 0, 0}
	if address.Is4() {
		packet = append(packet, 1)
	} else {
		packet = append(packet, 4)
	}
	packet = append(packet, address.AsSlice()...)
	packet = binary.BigEndian.AppendUint16(packet, port)
	return append(packet, payload...)
}

func parseSOCKSUDP(packet []byte) (netip.AddrPort, []byte, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return netip.AddrPort{}, nil, errors.New("invalid SOCKS5 UDP packet")
	}
	offset := 4
	var address netip.Addr
	switch packet[3] {
	case 1:
		if len(packet) < offset+4+2 {
			return netip.AddrPort{}, nil, errors.New("truncated SOCKS5 UDP packet")
		}
		address, _ = netip.AddrFromSlice(packet[offset : offset+4])
		offset += 4
	case 4:
		if len(packet) < offset+16+2 {
			return netip.AddrPort{}, nil, errors.New("truncated SOCKS5 UDP packet")
		}
		address, _ = netip.AddrFromSlice(packet[offset : offset+16])
		offset += 16
	case 3:
		if len(packet) < offset+1 {
			return netip.AddrPort{}, nil, errors.New("truncated SOCKS5 UDP packet")
		}
		length := int(packet[offset])
		offset++
		if len(packet) < offset+length+2 {
			return netip.AddrPort{}, nil, errors.New("truncated SOCKS5 UDP packet")
		}
		parsed, err := netip.ParseAddr(string(packet[offset : offset+length]))
		if err != nil {
			return netip.AddrPort{}, nil, errors.New("non-literal SOCKS5 UDP response")
		}
		address = parsed
		offset += length
	default:
		return netip.AddrPort{}, nil, errors.New("invalid SOCKS5 UDP address type")
	}
	port := binary.BigEndian.Uint16(packet[offset : offset+2])
	offset += 2
	return netip.AddrPortFrom(address.Unmap(), port), packet[offset:], nil
}

func dnsQuestion(name string) []byte {
	result := make([]byte, 0, len(name)+6)
	for _, label := range strings.Split(name, ".") {
		result = append(result, byte(len(label)))
		result = append(result, label...)
	}
	result = append(result, 0, 0, 1, 0, 1)
	return result
}

func dnsQuery(identifier uint16, question []byte) []byte {
	query := make([]byte, 12, 12+len(question))
	binary.BigEndian.PutUint16(query, identifier)
	query[2] = 1
	query[5] = 1
	return append(query, question...)
}

func validDNSAnswer(payload []byte, identifier uint16, question []byte) bool {
	if len(payload) < 12+len(question) || binary.BigEndian.Uint16(payload[:2]) != identifier ||
		payload[2]&0x80 == 0 || payload[3]&0x0f != 0 || binary.BigEndian.Uint16(payload[4:6]) != 1 ||
		binary.BigEndian.Uint16(payload[6:8]) == 0 || string(payload[12:12+len(question)]) != string(question) {
		return false
	}
	offset, ok := skipDNSName(payload, 12+len(question))
	if !ok || len(payload) < offset+10 {
		return false
	}
	resourceLength := int(binary.BigEndian.Uint16(payload[offset+8 : offset+10]))
	return resourceLength > 0 && len(payload) >= offset+10+resourceLength
}

func skipDNSName(payload []byte, offset int) (int, bool) {
	for offset < len(payload) {
		length := int(payload[offset])
		offset++
		if length == 0 {
			return offset, true
		}
		if length&0xc0 == 0xc0 {
			return offset + 1, offset < len(payload)
		}
		if length > 63 || offset+length > len(payload) {
			return 0, false
		}
		offset += length
	}
	return 0, false
}

func randomID(pending map[uint16]pendingProbe) (uint16, error) {
	for range 8 {
		var payload [2]byte
		if _, err := rand.Read(payload[:]); err != nil {
			return 0, errors.New("generate UDP probe identifier")
		}
		identifier := binary.BigEndian.Uint16(payload[:])
		if _, exists := pending[identifier]; !exists {
			return identifier, nil
		}
	}
	return 0, errors.New("generate unique UDP probe identifier")
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
