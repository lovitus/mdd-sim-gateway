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
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// Dynamic routes retain kernel indices and the exact objects they created.
// A late DHCP worker must never configure or down a replacement with the same name.
type dynamicRoute struct {
	iface   net.Interface
	address *rtnetlink.AddressMessage
	routes  []*realmRoute
	rule    *rtnetlink.RuleMessage
	realm   uint32
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
	// A point query preserves both transport errors and the kernel's ENODEV.
	// Link.Get masks errors; Link.List cannot expose a DONE-only DUMP_INTR.
	links, err := conn.Execute(&rtnetlink.LinkMessage{Index: uint32(iface.Index)}, unix.RTM_GETLINK, netlink.Request)
	if err != nil {
		return err
	}
	if len(links) != 1 {
		return errors.New("ambiguous DHCP interface reply")
	}
	link, ok := links[0].(*rtnetlink.LinkMessage)
	if !ok || link.Index != uint32(iface.Index) || link.Type != unix.ARPHRD_ETHER || link.Attributes == nil {
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
	route := DataRoute{Permit: permit, Interface: iface.Name, Table: permit.Mark, dynamic: &dynamicRoute{iface: iface, realm: permit.Mark}}
	if iface.Index <= 0 || len(iface.HardwareAddr) != 6 {
		return route, errors.New("DHCP requires an Ethernet interface")
	}
	guard.policyMu.Lock()
	defer guard.policyMu.Unlock()
	current := guard.permits[permit.Mark]
	if current.Owner != permit.Owner || current.Owner == "" || current.Interface != "" {
		return route, errors.New("DHCP permit is not in bootstrap state")
	}
	for mark, other := range guard.permits {
		if mark != permit.Mark && other.requiresRouteRealm && other.routeRealm&0xffff == permit.Mark&0xffff {
			return route, errors.New("DHCP route realm is reserved by another owner")
		}
	}
	conn, err := routeNetlink(ctx)
	if err != nil {
		return route, err
	}
	defer conn.Close()
	if err := checkDynamicLink(conn, iface); err != nil {
		return route, err
	}
	addresses, err := realmDump(ctx, unix.RTM_NEWADDR)
	if err != nil {
		return route, err
	}
	for _, message := range addresses {
		var address rtnetlink.AddressMessage
		if err := address.UnmarshalBinary(message.Data); err != nil {
			return route, err
		}
		if address.Index == uint32(iface.Index) && address.Family == unix.AF_INET {
			return route, errors.New("DHCP interface already has a foreign IPv4 address")
		}
	}
	if err := verifyRealmInventory(ctx, &route); err != nil {
		return route, err
	}
	if err := conn.Link.Set(&rtnetlink.LinkMessage{Index: uint32(iface.Index), Flags: unix.IFF_UP, Change: unix.IFF_UP}); err != nil {
		return route, err
	}
	route.linkEnabled = true
	current.requiresRouteRealm, current.routeRealm = true, permit.Mark
	guard.permits[permit.Mark], route.Permit = current, current
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
		entry := &realmRoute{realm: state.realm, RouteMessage: rtnetlink.RouteMessage{Family: unix.AF_INET, DstLength: uint8(prefix.Bits()), Protocol: unix.RTPROT_STATIC, Scope: scope, Type: unix.RTN_UNICAST, Attributes: rtnetlink.RouteAttributes{Table: route.Table, OutIface: uint32(state.iface.Index), Dst: net.IP(prefix.Masked().Addr().AsSlice()), Src: net.IP(address.Addr().AsSlice())}}}
		if via.IsValid() {
			entry.Attributes.Gateway = net.IP(via.AsSlice())
		}
		if _, err := conn.Execute(entry, unix.RTM_NEWROUTE, netlink.Request|netlink.Acknowledge|netlink.Create|netlink.Excl); err != nil {
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
	if current.Owner == "" || current.Owner != route.Permit.Owner || current.Interface != "" ||
		!current.requiresRouteRealm || current.routeRealm != state.realm {
		return errors.New("DHCP permit changed before publication")
	}
	if err := verifyRealmInventory(ctx, route); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
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
	var retained []*realmRoute
	for _, entry := range state.routes {
		// Include the original realm in the delete, so a late replacement
		// with the same prefix/interface but another owner cannot be removed.
		if _, err := conn.Execute(entry, unix.RTM_DELROUTE, netlink.Request|netlink.Acknowledge); !absent(err) {
			failures = append(failures, err)
			retained = append(retained, entry)
		}
	}
	state.routes = retained
	// All device mutations use the captured index, never a lookup by old name.
	if err := checkDynamicLink(conn, state.iface); err != nil {
		// Only the kernel's point-query ENODEV proves absence. A failed
		// transport or malformed reply is not proof of disappearance.
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
