//go:build linux

package linuxdataguard

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/jsimonetti/rtnetlink"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// Linux UAPI include/uapi/linux/rtnetlink.h; x/sys does not export RTA_NH_ID.
const routeNexthopID = 30

// rtnetlink v1.3.5 (and current upstream) omits RTA_FLOW. Keep its route
// codec/transport and supplement only this attribute; RTA_MARK is unrelated.
// Embedding preserves rtnetlink.Message, including its private marker method.
type realmRoute struct {
	rtnetlink.RouteMessage
	realm     uint32
	hopRealms []uint32
	unknown   bool
}

func (route *realmRoute) MarshalBinary() ([]byte, error) {
	payload, err := route.RouteMessage.MarshalBinary()
	if err != nil {
		return nil, err
	}
	if route.realm == 0 {
		return nil, errors.New("DHCP owned route requires a nonzero realm")
	}
	encoder := netlink.NewAttributeEncoder()
	encoder.Uint32(unix.RTA_FLOW, route.realm)
	attribute, err := encoder.Encode()
	return append(payload, attribute...), err
}

// iproute2 checks DUMP_INTR before discarding DONE (lib/libnetlink.c).
// The pinned Go transport discards DONE without exposing that flag. Use the
// existing system tool for complete read-only inventories, retaining its raw
// numeric objects, and the pinned codecs for decoding. No restore is executed.
// iproute2 can exit zero after warning about interruption: ANY stderr fails.
func realmDump(ctx context.Context, kind uint16) ([]netlink.Message, error) {
	var arguments []string
	var magic uint32
	switch kind {
	case unix.RTM_NEWROUTE:
		arguments, magic = []string{"-4", "route", "save", "table", "all"}, 0x45311224
	case unix.RTM_NEWRULE:
		arguments, magic = []string{"-4", "rule", "save"}, 0x71706986
	case unix.RTM_NEWADDR:
		arguments, magic = []string{"-4", "address", "save"}, 0x47361222
	default:
		return nil, errors.New("unsupported DHCP inventory")
	}
	path, err := exec.LookPath("ip")
	if err != nil && os.Getenv("PATH") == "" {
		for _, directory := range []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
			if path, err = exec.LookPath(filepath.Join(directory, "ip")); err == nil {
				break
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("DHCP inventory ip: %w", err)
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(bounded, path, arguments...)
	command.WaitDelay = time.Second
	stdout, stderr := &inventoryOutput{limit: 4 << 20}, &inventoryOutput{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	err = command.Run()
	if failure := errors.Join(err, bounded.Err()); failure != nil {
		return nil, fmt.Errorf("DHCP inventory %s: %w", arguments[1], failure)
	}
	if stderr.overflow || stderr.buffer.Len() != 0 || stdout.overflow {
		return nil, errors.New("DHCP inventory interrupted, warned or exceeded output limit")
	}
	return decodeInventory(stdout.buffer.Bytes(), magic, kind)
}

// Keep draining until process completion without retaining an unbounded dump.
// Overflow is independent of framing: a valid prefix must never pass as complete.
type inventoryOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (output *inventoryOutput) Write(data []byte) (int, error) {
	length := len(data)
	remaining := output.limit - output.buffer.Len()
	if length > remaining {
		output.overflow = true
		data = data[:remaining]
	}
	_, _ = output.buffer.Write(data)
	return length, nil
}

func decodeInventory(data []byte, magic uint32, kind uint16) ([]netlink.Message, error) {
	if len(data) < 4 || binary.NativeEndian.Uint32(data[:4]) != magic {
		return nil, errors.New("invalid DHCP inventory header")
	}
	data = data[4:]
	var messages []netlink.Message
	for len(data) != 0 {
		if len(data) < unix.NLMSG_HDRLEN {
			return nil, errors.New("truncated DHCP inventory message")
		}
		length := uint64(binary.NativeEndian.Uint32(data[:4]))
		if length < unix.NLMSG_HDRLEN || length > uint64(len(data)) || length%4 != 0 {
			return nil, errors.New("invalid DHCP inventory message length")
		}
		var message netlink.Message
		if err := message.UnmarshalBinary(data[:length]); err != nil {
			return nil, err
		}
		if uint16(message.Header.Type) != kind || message.Header.Flags&netlink.DumpInterrupted != 0 || len(message.Data) == 0 || message.Data[0] != unix.AF_INET {
			return nil, errors.New("unexpected or interrupted DHCP inventory message")
		}
		messages = append(messages, message)
		data = data[length:]
	}
	return messages, nil
}

func routeRealms(payload []byte) (uint32, []uint32, error) {
	decoder, err := netlink.NewAttributeDecoder(payload)
	if err != nil {
		return 0, nil, err
	}
	var realm uint32
	var hops []uint32
	seen := make(map[uint16]bool)
	for decoder.Next() {
		if seen[decoder.Type()] && decoder.Type() != unix.RTA_UNSPEC {
			return 0, nil, errors.New("duplicate route attribute")
		}
		seen[decoder.Type()] = true
		switch decoder.Type() {
		case unix.RTA_FLOW:
			if decoder.TypeFlags() != 0 {
				return 0, nil, errors.New("ambiguous route realm")
			}
			realm = decoder.Uint32()
		case unix.RTA_MULTIPATH:
			if decoder.TypeFlags() != 0 {
				return 0, nil, errors.New("ambiguous route nexthops")
			}
			data := decoder.Bytes()
			for len(data) != 0 {
				// rtnexthop is an eight-byte header followed by ordinary route
				// attributes. Do not lose nested RTA_FLOW in the upstream codec.
				if len(data) < 8 {
					return 0, nil, errors.New("truncated route nexthop")
				}
				length := int(binary.NativeEndian.Uint16(data[:2]))
				aligned := (length + 3) &^ 3
				if length < 8 || aligned > len(data) {
					return 0, nil, errors.New("invalid route nexthop length")
				}
				// Nested multipath is not a valid nexthop attribute. Check it
				// before parsing, so malformed dumps cannot recurse indefinitely.
				attrs, err := netlink.UnmarshalAttributes(data[8:length])
				if err != nil {
					return 0, nil, err
				}
				for _, attribute := range attrs {
					if attribute.Type&0x3fff == unix.RTA_MULTIPATH {
						return 0, nil, errors.New("nested route multipath")
					}
				}
				hop, _, err := routeRealms(data[8:length])
				if err != nil {
					return 0, nil, err
				}
				hops = append(hops, hop)
				data = data[aligned:]
			}
		case routeNexthopID:
			// This pinned route API does not inventory separate nexthop
			// objects. Refuse unsupported ownership evidence, never assume 0.
			return 0, nil, errors.New("DHCP realm inventory cannot resolve nexthop object")
		}
	}
	return realm, hops, decoder.Err()
}

func readRealmRoutes(ctx context.Context) ([]realmRoute, error) {
	raw, err := realmDump(ctx, unix.RTM_NEWROUTE)
	if err != nil {
		return nil, err
	}
	var routes []realmRoute
	for _, message := range raw {
		if message.Header.Type != unix.RTM_NEWROUTE {
			return nil, errors.New("unexpected DHCP route inventory message")
		}
		var route realmRoute
		if err := route.RouteMessage.UnmarshalBinary(message.Data); err != nil {
			return nil, err
		}
		if route.Family != unix.AF_INET {
			return nil, errors.New("unexpected DHCP route inventory family")
		}
		route.realm, route.hopRealms, err = routeRealms(message.Data[unix.SizeofRtMsg:])
		if err != nil {
			return nil, err
		}
		attrs, err := netlink.UnmarshalAttributes(message.Data[unix.SizeofRtMsg:])
		if err != nil {
			return nil, err
		}
		for _, attr := range attrs {
			// The route codec intentionally ignores unknown attributes. They
			// can belong to a foreign route, but cannot prove our exact object.
			switch attr.Type {
			case unix.RTA_UNSPEC, unix.RTA_DST, unix.RTA_PREFSRC, unix.RTA_GATEWAY, unix.RTA_OIF, unix.RTA_TABLE, unix.RTA_FLOW:
			default:
				route.unknown = true
			}
		}
		routes = append(routes, route)
	}
	return routes, nil
}

func sameRealmRoute(left, right *realmRoute) bool {
	if left.unknown || right.unknown || left.realm != right.realm || len(left.hopRealms) != 0 || len(right.hopRealms) != 0 {
		return false
	}
	a, b := left.RouteMessage, right.RouteMessage
	// The kernel may encode the table in the header, and omit default Dst.
	if a.Attributes.Table == 0 {
		a.Attributes.Table = uint32(a.Table)
	}
	if b.Attributes.Table == 0 {
		b.Attributes.Table = uint32(b.Table)
	}
	a.Table, b.Table = 0, 0
	if a.DstLength == 0 && (len(a.Attributes.Dst) == 0 || a.Attributes.Dst.Equal(net.IPv4zero)) {
		a.Attributes.Dst = nil
	}
	if b.DstLength == 0 && (len(b.Attributes.Dst) == 0 || b.Attributes.Dst.Equal(net.IPv4zero)) {
		b.Attributes.Dst = nil
	}
	return reflect.DeepEqual(a, b)
}

// Reserve a nonzero low half: Linux can combine the two halves of route/rule
// realms. Comparing only complete foreign 32-bit values would miss collisions.
// Old partially cleaned objects remain foreign even after their permit closes.
func verifyRealmInventory(ctx context.Context, route *DataRoute) error {
	state := route.dynamic
	if state == nil || state.realm&0xffff == 0 {
		return errors.New("DHCP route realm has no reservable low half")
	}
	routes, err := readRealmRoutes(ctx)
	if err != nil {
		return err
	}
	found := make([]bool, len(state.routes))
	for index := range routes {
		other := &routes[index]
		owned := false
		for i, expected := range state.routes {
			if sameRealmRoute(other, expected) {
				if found[i] {
					return errors.New("duplicate DHCP owned route")
				}
				found[i], owned = true, true
				break
			}
		}
		if owned {
			continue
		}
		if other.Attributes.Table == route.Table || uint32(other.Table) == route.Table {
			return errors.New("DHCP table contains a foreign route")
		}
		for _, realm := range append([]uint32{other.realm}, other.hopRealms...) {
			if realm&0xffff == state.realm&0xffff {
				return errors.New("DHCP route realm is already owned")
			}
		}
	}
	for _, present := range found {
		if !present {
			return errors.New("DHCP owned route readback mismatch")
		}
	}
	raw, err := realmDump(ctx, unix.RTM_NEWRULE)
	if err != nil {
		return err
	}
	ruleFound := false
	for _, message := range raw {
		if message.Header.Type != unix.RTM_NEWRULE {
			return errors.New("unexpected DHCP rule inventory message")
		}
		var rule rtnetlink.RuleMessage
		if len(message.Data) < unix.SizeofRtMsg {
			return errors.New("truncated DHCP rule inventory")
		}
		attrs, err := netlink.UnmarshalAttributes(message.Data[unix.SizeofRtMsg:])
		if err != nil {
			return err
		}
		seen := make(map[uint16]bool)
		for _, attr := range attrs {
			if attr.Type&0xc000 != 0 || seen[attr.Type] && attr.Type != unix.FRA_PAD && attr.Type != unix.FRA_UNSPEC {
				return errors.New("ambiguous DHCP rule attribute")
			}
			seen[attr.Type] = true
		}
		if err := rule.UnmarshalBinary(message.Data); err != nil {
			return err
		}
		if rule.Family != unix.AF_INET || rule.Attributes == nil {
			return errors.New("invalid DHCP rule inventory")
		}
		// FRA_FLOW is already supported by the pinned rule codec. DstRealm
		// is the low half, including when the high half is supplied elsewhere.
		if low := rule.Attributes.DstRealm; low != nil && uint32(*low) == state.realm&0xffff {
			return errors.New("DHCP route realm conflicts with a policy rule")
		}
		if rule.Attributes.Table != nil && *rule.Attributes.Table == route.Table {
			if state.rule == nil || ruleFound {
				return errors.New("DHCP route rule is already owned")
			}
			// Linux reports its default rule protocol even when the add did
			// not specify one. Every selector, action and realm remains exact.
			if state.rule.Attributes.Protocol == nil && rule.Attributes.Protocol != nil && *rule.Attributes.Protocol == unix.RTPROT_UNSPEC {
				rule.Attributes.Protocol = nil
			}
			if state.rule.Attributes.SuppressPrefixLen == nil && rule.Attributes.SuppressPrefixLen != nil && *rule.Attributes.SuppressPrefixLen == ^uint32(0) {
				rule.Attributes.SuppressPrefixLen = nil
			}
			// FRA_TABLE already matched the exact private table above. The
			// kernel uses RT_TABLE_COMPAT in the eight-bit header for it.
			if rule.Table == unix.RT_TABLE_COMPAT {
				rule.Table = state.rule.Table
			}
			if !reflect.DeepEqual(&rule, state.rule) {
				return errors.New("DHCP owned rule readback mismatch")
			}
			ruleFound = true
		}
	}
	if state.rule != nil && !ruleFound {
		return errors.New("DHCP owned rule disappeared")
	}
	return nil
}
