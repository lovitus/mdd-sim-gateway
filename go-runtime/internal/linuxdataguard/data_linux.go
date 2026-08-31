//go:build linux

package linuxdataguard

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type DataRoute struct {
	Permit                                                        DataPermit
	Interface                                                     string
	Address                                                       netip.Prefix
	Gateway                                                       netip.Addr
	Table                                                         uint32
	linkEnabled, addressInstalled, tableConfigured, ruleInstalled bool
}

func (guard *Guard) ConfigureDataRoute(ctx context.Context, permit DataPermit, interfaceName string,
	address netip.Prefix, gateway netip.Addr) (DataRoute, error) {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" || filepath.Base(interfaceName) != interfaceName || !address.IsValid() || !address.Addr().Is4() ||
		gateway.IsValid() && !gateway.Is4() {
		return DataRoute{}, errors.New("invalid protected cellular data route")
	}
	route := DataRoute{Permit: permit, Interface: interfaceName, Address: address, Gateway: gateway, Table: permit.Mark}
	guard.policyMu.Lock()
	current := guard.permits[permit.Mark]
	if current.Owner == "" || current.Owner != permit.Owner || current.Interface != "" && current.Interface != interfaceName {
		guard.policyMu.Unlock()
		return DataRoute{}, errors.New("cellular data permit is not active")
	}
	current.Interface = interfaceName
	guard.permits[permit.Mark] = current
	if err := guard.installNetfilterLocked(ctx); err != nil {
		current.Interface = ""
		guard.permits[permit.Mark] = current
		guard.policyMu.Unlock()
		return route, errors.Join(err, guard.installNetfilterLocked(ctx))
	}
	guard.policyMu.Unlock()
	permit = current
	route.Permit = permit
	if err := guard.ProtectNetdev(ctx, interfaceName); err != nil {
		return route, err
	}
	if _, err := guard.run(ctx, nil, "ip", "link", "set", "dev", interfaceName, "up"); err != nil {
		return route, fmt.Errorf("enable protected cellular interface: %w", err)
	}
	route.linkEnabled = true
	if _, err := guard.run(ctx, nil, "ip", "address", "replace", address.String(), "dev", interfaceName, "noprefixroute"); err != nil {
		return route, fmt.Errorf("assign protected cellular address: %w", err)
	}
	route.addressInstalled = true
	table := strconv.FormatUint(uint64(route.Table), 10)
	connected := address.Masked().String()
	if _, err := guard.run(ctx, nil, "ip", "route", "replace", "table", table, connected,
		"dev", interfaceName, "src", address.Addr().String()); err != nil {
		return route, fmt.Errorf("install protected cellular connected route: %w", err)
	}
	route.tableConfigured = true
	if gateway.IsValid() {
		if _, err := guard.run(ctx, nil, "ip", "route", "replace", "table", table, gateway.String()+"/32",
			"dev", interfaceName, "scope", "link"); err != nil {
			return route, fmt.Errorf("install protected cellular gateway route: %w", err)
		}
		if _, err := guard.run(ctx, nil, "ip", "route", "replace", "table", table, "default", "via", gateway.String(),
			"dev", interfaceName); err != nil {
			return route, fmt.Errorf("install protected cellular default route: %w", err)
		}
	} else if _, err := guard.run(ctx, nil, "ip", "route", "replace", "table", table, "default", "dev", interfaceName); err != nil {
		return route, fmt.Errorf("install protected point-to-point default route: %w", err)
	}
	mark := strconv.FormatUint(uint64(permit.Mark), 10) + "/4294967295"
	priority := "10000"
	_, _ = guard.run(ctx, nil, "ip", "rule", "del", "priority", priority, "fwmark", mark, "lookup", table)
	if _, err := guard.run(ctx, nil, "ip", "rule", "add", "priority", priority, "fwmark", mark, "lookup", table); err != nil {
		return route, fmt.Errorf("install protected cellular policy rule: %w", err)
	}
	route.ruleInstalled = true
	return route, nil
}

// CloseDataRoute revokes the nftables socket permit before removing routes,
// addresses or the bearer-facing interface. Existing sockets therefore fail
// closed even when a later cleanup step cannot complete.
func (guard *Guard) CloseDataRoute(ctx context.Context, route *DataRoute) error {
	if route == nil {
		return nil
	}
	if err := guard.CloseDataPermit(ctx, route.Permit); err != nil {
		return err
	}
	return guard.CleanupDataRoute(route)
}

func (guard *Guard) CleanupDataRoute(route *DataRoute) error { return guard.rollbackDataRoute(route) }

func (guard *Guard) rollbackDataRoute(route *DataRoute) error {
	if route == nil || route.Interface == "" || route.Table == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	table := strconv.FormatUint(uint64(route.Table), 10)
	mark := strconv.FormatUint(uint64(route.Permit.Mark), 10) + "/4294967295"
	priority := "10000"
	var failures []error
	if route.ruleInstalled {
		if _, err := guard.run(ctx, nil, "ip", "rule", "del", "priority", priority, "fwmark", mark, "lookup", table); err != nil {
			failures = append(failures, err)
		} else {
			route.ruleInstalled = false
		}
	}
	if route.tableConfigured {
		if _, err := guard.run(ctx, nil, "ip", "route", "flush", "table", table); err != nil {
			failures = append(failures, err)
		} else {
			route.tableConfigured = false
		}
	}
	if route.addressInstalled {
		if _, err := guard.run(ctx, nil, "ip", "address", "del", route.Address.String(), "dev", route.Interface); err != nil {
			failures = append(failures, err)
		} else {
			route.addressInstalled = false
		}
	}
	if route.linkEnabled {
		if _, err := guard.run(ctx, nil, "ip", "link", "set", "dev", route.Interface, "down"); err != nil {
			failures = append(failures, err)
		} else {
			route.linkEnabled = false
		}
	}
	return errors.Join(failures...)
}
