//go:build linux

package linuxdataguard

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/jsimonetti/rtnetlink"
	"golang.org/x/sys/unix"
)

// Dynamic routes retain kernel indices and the exact objects they created.
// A late DHCP worker must never configure or down a replacement with the same name.
type dynamicRoute struct {
	iface   net.Interface
	address *rtnetlink.AddressMessage
	routes  []*rtnetlink.RouteMessage
	rule    *rtnetlink.RuleMessage
}

func routeNetlink(ctx context.Context) (*rtnetlink.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := rtnetlink.Dial(nil)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(3 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func checkDynamicLink(conn *rtnetlink.Conn, iface net.Interface) error {
	// Link.Get in the pinned rtnetlink version masks transport errors when no
	// message is returned. A successful complete dump proves index absence.
	links, err := conn.Link.List()
	if err != nil {
		return err
	}
	var link rtnetlink.LinkMessage
	found := false
	for _, candidate := range links {
		if candidate.Index == uint32(iface.Index) {
			link, found = candidate, true
			break
		}
	}
	if !found {
		return unix.ENODEV
	}
	if link.Type != unix.ARPHRD_ETHER || link.Attributes == nil {
		return errors.New("DHCP interface identity unavailable")
	}
	a := link.Attributes
	if a.Name != iface.Name || !bytes.Equal(a.Address, iface.HardwareAddr) || a.NetDevGroup == nil || *a.NetDevGroup != InterfaceGroup {
		return errors.New("DHCP interface identity or isolation changed")
	}
	return nil
}

// PrepareDynamicRoute enables only an already quarantined exact interface.
// The permit remains unpublished: AF_PACKET DHCP is not ordinary host traffic.
func (guard *Guard) PrepareDynamicRoute(ctx context.Context, permit DataPermit, iface net.Interface) (DataRoute, error) {
	route := DataRoute{Permit: permit, Interface: iface.Name, Table: permit.Mark, dynamic: &dynamicRoute{iface: iface}}
	if iface.Index <= 0 || len(iface.HardwareAddr) != 6 {
		return route, errors.New("DHCP requires an Ethernet interface")
	}
	guard.policyMu.Lock()
	defer guard.policyMu.Unlock()
	current := guard.permits[permit.Mark]
	if current.Owner != permit.Owner || current.Owner == "" || current.Interface != "" {
		return route, errors.New("DHCP permit is not in bootstrap state")
	}
	conn, err := routeNetlink(ctx)
	if err != nil {
		return route, err
	}
	defer conn.Close()
	if err := checkDynamicLink(conn, iface); err != nil {
		return route, err
	}
	addresses, err := conn.Address.List()
	if err != nil {
		return route, err
	}
	for _, address := range addresses {
		if address.Index == uint32(iface.Index) && address.Family == unix.AF_INET {
			return route, errors.New("DHCP interface already has a foreign IPv4 address")
		}
	}
	routes, err := conn.Route.List()
	if err != nil {
		return route, err
	}
	for _, other := range routes {
		if other.Attributes.Table == permit.Mark {
			return route, errors.New("DHCP route table is already owned")
		}
	}
	rules, err := conn.Rule.List()
	if err != nil {
		return route, err
	}
	for _, rule := range rules {
		if rule.Attributes != nil && rule.Attributes.Table != nil && *rule.Attributes.Table == permit.Mark {
			return route, errors.New("DHCP route rule is already owned")
		}
	}
	if err := conn.Link.Set(&rtnetlink.LinkMessage{Index: uint32(iface.Index), Flags: unix.IFF_UP, Change: unix.IFF_UP}); err != nil {
		return route, err
	}
	route.linkEnabled = true
	return route, nil
}

// ConfigureDynamicRoute publishes last. Exclusive additions refuse foreign
// addresses/routes instead of replacing them; partial progress remains owned.
func (guard *Guard) ConfigureDynamicRoute(ctx context.Context, route *DataRoute, address netip.Prefix, gateway netip.Addr) error {
	if route == nil || route.dynamic == nil || !address.IsValid() || !address.Addr().Is4() || gateway.IsValid() && !gateway.Is4() {
		return errors.New("invalid DHCP route")
	}
	conn, err := routeNetlink(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	state := route.dynamic
	if err := checkDynamicLink(conn, state.iface); err != nil {
		return err
	}
	route.Address, route.Gateway = address, gateway
	addr := &rtnetlink.AddressMessage{Family: unix.AF_INET, PrefixLength: uint8(address.Bits()), Index: uint32(state.iface.Index), Attributes: &rtnetlink.AddressAttributes{Local: net.IP(address.Addr().AsSlice()), Address: net.IP(address.Addr().AsSlice()), Flags: unix.IFA_F_NOPREFIXROUTE}}
	if err := conn.Address.New(addr); err != nil {
		return err
	}
	state.address = addr
	add := func(prefix netip.Prefix, via netip.Addr, scope uint8) error {
		entry := &rtnetlink.RouteMessage{Family: unix.AF_INET, DstLength: uint8(prefix.Bits()), Protocol: unix.RTPROT_STATIC, Scope: scope, Type: unix.RTN_UNICAST, Attributes: rtnetlink.RouteAttributes{Table: route.Table, OutIface: uint32(state.iface.Index), Dst: net.IP(prefix.Masked().Addr().AsSlice()), Src: net.IP(address.Addr().AsSlice())}}
		if via.IsValid() {
			entry.Attributes.Gateway = net.IP(via.AsSlice())
		}
		if err := conn.Route.Add(entry); err != nil {
			return err
		}
		state.routes = append(state.routes, entry)
		return nil
	}
	if err := add(address.Masked(), netip.Addr{}, unix.RT_SCOPE_LINK); err != nil {
		return err
	}
	if gateway.IsValid() && !address.Contains(gateway) {
		if err := add(netip.PrefixFrom(gateway, 32), netip.Addr{}, unix.RT_SCOPE_LINK); err != nil {
			return err
		}
	}
	if err := add(netip.MustParsePrefix("0.0.0.0/0"), gateway, unix.RT_SCOPE_UNIVERSE); err != nil {
		return err
	}
	priority, mask := uint32(10000), ^uint32(0)
	rule := &rtnetlink.RuleMessage{Family: unix.AF_INET, Action: unix.FR_ACT_TO_TBL, Attributes: &rtnetlink.RuleAttributes{Table: &route.Table, FwMark: &route.Permit.Mark, FwMask: &mask, Priority: &priority}}
	if err := conn.Rule.Add(rule); err != nil {
		return err
	}
	state.rule = rule
	if err := checkDynamicLink(conn, state.iface); err != nil {
		return err
	}
	guard.policyMu.Lock()
	defer guard.policyMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current := guard.permits[route.Permit.Mark]
	if current.Owner == "" || current.Owner != route.Permit.Owner || current.Interface != "" {
		return errors.New("DHCP permit changed before publication")
	}
	current.Interface = route.Interface
	guard.permits[current.Mark] = current
	if err := errors.Join(guard.installNetfilterLocked(ctx), ctx.Err()); err != nil {
		current.Interface = ""
		guard.permits[current.Mark] = current
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return errors.Join(err, guard.installNetfilterLocked(cleanup))
	}
	route.Permit = current
	return nil
}

func (guard *Guard) cleanupDynamicRoute(route *DataRoute) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := routeNetlink(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return cleanupDynamicRouteOn(conn, route)
}

// Keep partially completed kernel cleanup on the owning route until every
// remaining object is removed or a successful inventory proves it vanished.
func cleanupDynamicRouteOn(conn *rtnetlink.Conn, route *DataRoute) error {
	state := route.dynamic
	var failures []error
	absent := func(err error) bool {
		return err == nil || errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EADDRNOTAVAIL)
	}
	if state.rule != nil {
		if err := conn.Rule.Delete(state.rule); !absent(err) {
			failures = append(failures, err)
		} else {
			state.rule = nil
		}
	}
	var retained []*rtnetlink.RouteMessage
	for _, entry := range state.routes {
		if err := conn.Route.Delete(entry); !absent(err) {
			failures = append(failures, err)
			retained = append(retained, entry)
		}
	}
	state.routes = retained
	// All device mutations use the captured index, never a lookup by old name.
	if err := checkDynamicLink(conn, state.iface); err != nil {
		// Only a successful complete dump produces ENODEV here. A failed
		// enumeration is not proof of disappearance.
		if !errors.Is(err, unix.ENODEV) {
			return errors.Join(append(failures, err)...)
		}
		state.address = nil
		route.linkEnabled = false
		return errors.Join(failures...)
	}
	if state.address != nil {
		if err := conn.Address.Delete(state.address); !absent(err) {
			failures = append(failures, err)
		} else {
			state.address = nil
		}
	}
	if route.linkEnabled {
		if err := conn.Link.Set(&rtnetlink.LinkMessage{Index: uint32(state.iface.Index), Change: unix.IFF_UP}); err != nil {
			failures = append(failures, err)
		} else {
			route.linkEnabled = false
		}
	}
	return errors.Join(failures...)
}
