//go:build linux

package linuxdataguard

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergedConfigRequiresExactStrictGSMDeviceSpec(t *testing.T) {
	valid := []byte("# merged\n[keyfile]\nunmanaged-devices=interface-name:wwan0;type:gsm\n")
	if !mergedConfigHasStrictGSMGuard(valid) {
		t.Fatal("exact type:gsm device spec was not found")
	}
	for name, payload := range map[string][]byte{
		"wrong section": []byte("[device]\nunmanaged-devices=type:gsm\n"),
		"substring":     []byte("[keyfile]\nunmanaged-devices=except:type:gsm\n"),
		"missing":       []byte("[keyfile]\nunmanaged-devices=interface-name:wwan0\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if mergedConfigHasStrictGSMGuard(payload) {
				t.Fatal("invalid merged config was accepted")
			}
		})
	}
}

func TestNFTContractRequiresBothExactDevgroupDropHooks(t *testing.T) {
	valid := []byte(`{
  "nftables": [
    {"metainfo":{"json_schema_version":1}},
    {"table":{"family":"inet","name":"mdd_cellular_guard","handle":1}},
    {"chain":{"family":"inet","table":"mdd_cellular_guard","name":"output","handle":2,"type":"filter","hook":"output","prio":0,"policy":"accept"}},
    {"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"output","handle":3,"expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":5063748}},{"counter":{"packets":0,"bytes":0}},{"drop":null}]}},
    {"chain":{"family":"inet","table":"mdd_cellular_guard","name":"input","handle":4,"type":"filter","hook":"input","prio":0,"policy":"accept"}},
    {"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"input","handle":5,"expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":5063748}},{"match":{"op":"in","left":{"ct":{"key":"state"}},"right":{"set":["established","related"]}}},{"counter":{"packets":0,"bytes":0}},{"accept":null}]}},
    {"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"input","handle":6,"expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":5063748}},{"counter":{"packets":0,"bytes":0}},{"drop":null}]}},
    {"chain":{"family":"inet","table":"mdd_cellular_guard","name":"forward","handle":7,"type":"filter","hook":"forward","prio":0,"policy":"accept"}},
    {"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"forward","handle":8,"expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":5063748}},{"counter":{"packets":0,"bytes":0}},{"drop":null}]}},
    {"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"forward","handle":9,"expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":5063748}},{"counter":{"packets":0,"bytes":0}},{"drop":null}]}}
  ]
}`)
	if err := verifyNFTContract(valid, nil); err != nil {
		t.Fatalf("valid nft contract: %v", err)
	}
	for name, replace := range map[string][2]string{
		"wrong group":   {"5063748", "5063749"},
		"wrong verdict": {`{"drop":null}`, `{"accept":null}`},
		"wrong hook":    {`"hook":"forward"`, `"hook":"input"`},
	} {
		t.Run(name, func(t *testing.T) {
			corrupt := []byte(stringReplaceOnce(string(valid), replace[0], replace[1]))
			if err := verifyNFTContract(corrupt, nil); err == nil {
				t.Fatal("corrupt nft contract was accepted")
			}
		})
	}
}

func TestNFTContractAcceptsOnlyDeclaredSocketMarkPermits(t *testing.T) {
	const mark = uint32(1291911169)
	payload := []byte(`{"nftables":[
{"table":{"family":"inet","name":"mdd_cellular_guard"}},
{"chain":{"family":"inet","table":"mdd_cellular_guard","name":"output","type":"filter","hook":"output","prio":0,"policy":"accept"}},
{"chain":{"family":"inet","table":"mdd_cellular_guard","name":"input","type":"filter","hook":"input","prio":0,"policy":"accept"}},
{"chain":{"family":"inet","table":"mdd_cellular_guard","name":"forward","type":"filter","hook":"forward","prio":0,"policy":"accept"}},
{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"output","expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":5063748}},{"match":{"op":"==","left":{"meta":{"key":"oifname"}},"right":"wwan0"}},{"match":{"op":"==","left":{"socket":{"key":"cgroupv2","level":2}},"right":"system.slice/mdd-agent.service"}},{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":1291911169}},{"counter":{}},{"accept":null}]}},
{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"output","expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":5063748}},{"counter":{}},{"drop":null}]}},
{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"input","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":5063748}},{"match":{"op":"in","left":{"ct":{"key":"state"}},"right":{"set":["established","related"]}}},{"counter":{}},{"accept":null}]}},
{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"input","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":5063748}},{"counter":{}},{"drop":null}]}},
{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"forward","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":5063748}},{"counter":{}},{"drop":null}]}},
{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"forward","expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":5063748}},{"counter":{}},{"drop":null}]}}
]}`)
	if err := verifyNFTContract(payload, map[uint32]string{mark: "wwan0"}); err != nil {
		t.Fatalf("exact declared socket permit rejected: %v", err)
	}
	if err := verifyNFTContract(payload, nil); err == nil {
		t.Fatal("undeclared socket permit was accepted")
	}
}

func TestExistingVHCIParentRequiresExactDurableIdentity(t *testing.T) {
	guard := newGuard("/sys", "/etc", "/var/lib/mdd-agent", func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
		return nil, nil
	})
	physicalID := "/sys/devices/platform/vhci_hcd.0/usb1/1-1"
	parent := usbParent{physicalID: physicalID, vendorID: 0x2c7c, productID: 0x0125, serial: "serial-a"}
	marker := importMarker{SchemaVersion: 1, PhysicalID: physicalID, VendorID: parent.vendorID,
		ProductID: parent.productID, Serial: parent.serial}
	if err := guard.verifyTrackedParents(map[string]usbParent{physicalID: parent}, map[string]importMarker{physicalID: marker}); err != nil {
		t.Fatalf("exact marker rejected: %v", err)
	}
	if err := guard.verifyTrackedParents(map[string]usbParent{physicalID: parent}, nil); err == nil {
		t.Fatal("untracked existing VHCI parent was accepted")
	}
	marker.Serial = "serial-b"
	if err := guard.verifyTrackedParents(map[string]usbParent{physicalID: parent}, map[string]importMarker{physicalID: marker}); err == nil {
		t.Fatal("mismatched durable identity was accepted")
	}
}

func TestLeadingUdevVersion(t *testing.T) {
	for input, expected := range map[string]uint64{"219": 219, "255.4-1ubuntu8": 255, "260-rc2": 260} {
		actual, err := leadingUint(input)
		if err != nil || actual != expected {
			t.Fatalf("leadingUint(%q)=%d, %v", input, actual, err)
		}
	}
	if _, err := leadingUint("systemd"); err == nil {
		t.Fatal("version without a leading integer was accepted")
	}
}

func TestDataRouteRevokesPermitBeforeNetworkCleanup(t *testing.T) {
	type invocation struct{ name, arguments, input string }
	var calls []invocation
	var guard *Guard
	run := func(_ context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
		calls = append(calls, invocation{name: name, arguments: strings.Join(arguments, " "), input: string(input)})
		if name == "ip" && strings.Join(arguments, " ") == "-j link show dev wwan0" {
			return []byte(fmt.Sprintf(`[{"ifname":"wwan0","group":%d}]`, InterfaceGroup)), nil
		}
		if name == "nft" && len(arguments) > 0 && arguments[0] == "--numeric" {
			permit := ""
			for mark, current := range guard.permits {
				if current.Interface != "" {
					permit = fmt.Sprintf(`{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"output","expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":%d}},{"match":{"op":"==","left":{"meta":{"key":"oifname"}},"right":"%s"}},{"match":{"op":"==","left":{"socket":{"key":"cgroupv2","level":2}},"right":"system.slice/mdd-agent.service"}},{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":%d}},{"counter":{}},{"accept":null}]}},`, InterfaceGroup, current.Interface, mark)
				}
			}
			return []byte(fmt.Sprintf(`{"nftables":[{"table":{"family":"inet","name":"mdd_cellular_guard"}},{"chain":{"family":"inet","table":"mdd_cellular_guard","name":"output","type":"filter","hook":"output","prio":0,"policy":"accept"}},{"chain":{"family":"inet","table":"mdd_cellular_guard","name":"input","type":"filter","hook":"input","prio":0,"policy":"accept"}},{"chain":{"family":"inet","table":"mdd_cellular_guard","name":"forward","type":"filter","hook":"forward","prio":0,"policy":"accept"}},%s{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"output","expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":%d}},{"counter":{}},{"drop":null}]}},{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"input","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":%d}},{"match":{"op":"in","left":{"ct":{"key":"state"}},"right":{"set":["established","related"]}}},{"counter":{}},{"accept":null}]}},{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"input","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":%d}},{"counter":{}},{"drop":null}]}},{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"forward","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":%d}},{"counter":{}},{"drop":null}]}},{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"forward","expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":%d}},{"counter":{}},{"drop":null}]}}]}`, permit, InterfaceGroup, InterfaceGroup, InterfaceGroup, InterfaceGroup, InterfaceGroup)), nil
		}
		return nil, nil
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys", "class", "net", "wwan0"), 0o755); err != nil {
		t.Fatal(err)
	}
	guard = newGuard(filepath.Join(root, "sys"), filepath.Join(root, "etc"), filepath.Join(root, "state"), run)
	permit, err := guard.OpenDataPermit(context.Background(), "equipment\x00card")
	if err != nil {
		t.Fatal(err)
	}
	route, err := guard.ConfigureDataRoute(context.Background(), permit, "wwan0",
		netip.MustParsePrefix("10.1.2.1/30"), netip.MustParseAddr("10.1.2.2"))
	if err != nil {
		t.Fatal(err)
	}
	beforeClose := len(calls)
	if err := guard.CloseDataRoute(context.Background(), &route); err != nil {
		t.Fatal(err)
	}
	permitRemoval, networkCleanup := -1, -1
	for index, call := range calls[beforeClose:] {
		if call.name == "nft" && call.arguments == "-f -" && !strings.Contains(call.input, "socket cgroupv2") {
			permitRemoval = index
		}
		if call.name == "ip" && strings.HasPrefix(call.arguments, "rule del ") {
			networkCleanup = index
			break
		}
	}
	if permitRemoval < 0 || networkCleanup < 0 || permitRemoval >= networkCleanup {
		t.Fatalf("permit removal=%d network cleanup=%d calls=%+v", permitRemoval, networkCleanup, calls[beforeClose:])
	}
	for _, call := range calls {
		if call.name == "nft" && call.arguments == "-f -" && strings.Contains(call.input, "flush table") {
			t.Fatal("nft policy update used non-replacing flush-table semantics")
		}
	}
}

func TestFailedPermitRevocationRetainsExactOwner(t *testing.T) {
	failInstall := false
	var guard *Guard
	run := func(_ context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
		if name == "nft" && strings.Join(arguments, " ") == "-f -" && failInstall {
			return nil, errors.New("injected nft failure")
		}
		if name == "nft" && len(arguments) > 0 && arguments[0] == "--numeric" {
			return nftJSONForGuard(guard), nil
		}
		return nil, nil
	}
	guard = newGuard("/sys", "/etc", "/var/lib/mdd-agent", run)
	permit, err := guard.OpenDataPermit(context.Background(), "equipment\x00card")
	if err != nil {
		t.Fatal(err)
	}
	permit.Interface = "wwan0"
	guard.permits[permit.Mark] = permit
	failInstall = true
	if err := guard.CloseDataPermit(context.Background(), permit); err == nil {
		t.Fatal("injected permit revocation failure was hidden")
	}
	if current := guard.permits[permit.Mark]; current != permit {
		t.Fatalf("failed revocation lost owner: current=%+v want=%+v", current, permit)
	}
}

func TestRouteFailureKeepsIdentityForPermitRevocationRetry(t *testing.T) {
	var guard *Guard
	permitRemovalFailures := 1
	absentDeleteCalls := 0
	run := func(_ context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
		joined := strings.Join(arguments, " ")
		if name == "nft" && joined == "-f -" && !strings.Contains(string(input), "socket cgroupv2") && permitRemovalFailures > 0 {
			permitRemovalFailures--
			return nil, errors.New("injected first permit removal failure")
		}
		if name == "nft" && len(arguments) > 0 && arguments[0] == "--numeric" {
			return nftJSONForGuard(guard), nil
		}
		if name == "ip" && joined == "-j link show dev wwan0" {
			return []byte(fmt.Sprintf(`[{"ifname":"wwan0","group":%d}]`, InterfaceGroup)), nil
		}
		if name == "ip" && joined == "link set dev wwan0 up" {
			return nil, errors.New("injected link-up failure")
		}
		if name == "ip" && (strings.HasPrefix(joined, "rule del ") || strings.HasPrefix(joined, "address del ") ||
			strings.HasPrefix(joined, "route flush ")) {
			absentDeleteCalls++
			return nil, errors.New("absent kernel object")
		}
		return nil, nil
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys", "class", "net", "wwan0"), 0o755); err != nil {
		t.Fatal(err)
	}
	guard = newGuard(filepath.Join(root, "sys"), filepath.Join(root, "etc"), filepath.Join(root, "state"), run)
	permit, err := guard.OpenDataPermit(context.Background(), "equipment\x00card")
	if err != nil {
		t.Fatal(err)
	}
	route, err := guard.ConfigureDataRoute(context.Background(), permit, "wwan0",
		netip.MustParsePrefix("10.1.2.1/30"), netip.MustParseAddr("10.1.2.2"))
	if err == nil {
		t.Fatal("injected route failure was hidden")
	}
	if route.Permit.Mark != permit.Mark || route.Permit.Interface != "wwan0" || guard.permits[permit.Mark] != route.Permit {
		t.Fatalf("route failure lost cleanup identity: route=%+v owner=%+v", route, guard.permits[permit.Mark])
	}
	if err := guard.CloseDataRoute(context.Background(), &route); err == nil {
		t.Fatal("injected first permit revocation failure was hidden")
	}
	if current := guard.permits[permit.Mark]; current != route.Permit {
		t.Fatalf("first failed cleanup lost exact permit: current=%+v route=%+v", current, route)
	}
	if err := guard.CloseDataRoute(context.Background(), &route); err != nil {
		t.Fatalf("second exact cleanup attempt failed: %v", err)
	}
	if _, exists := guard.permits[permit.Mark]; exists {
		t.Fatal("second cleanup retained an already-revoked permit")
	}
	if absentDeleteCalls != 0 {
		t.Fatalf("stage-aware cleanup attempted %d absent kernel deletes", absentDeleteCalls)
	}
}

func nftJSONForGuard(guard *Guard) []byte {
	permit := ""
	for mark, current := range guard.permits {
		if current.Interface != "" {
			permit = fmt.Sprintf(`{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"output","expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":%d}},{"match":{"op":"==","left":{"meta":{"key":"oifname"}},"right":"%s"}},{"match":{"op":"==","left":{"socket":{"key":"cgroupv2","level":2}},"right":"%s"}},{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":%d}},{"counter":{}},{"accept":null}]}},`, InterfaceGroup, current.Interface, agentCGroup, mark)
		}
	}
	return []byte(fmt.Sprintf(`{"nftables":[{"table":{"family":"inet","name":"mdd_cellular_guard"}},{"chain":{"family":"inet","table":"mdd_cellular_guard","name":"output","type":"filter","hook":"output","prio":0,"policy":"accept"}},{"chain":{"family":"inet","table":"mdd_cellular_guard","name":"input","type":"filter","hook":"input","prio":0,"policy":"accept"}},{"chain":{"family":"inet","table":"mdd_cellular_guard","name":"forward","type":"filter","hook":"forward","prio":0,"policy":"accept"}},%s{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"output","expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":%d}},{"counter":{}},{"drop":null}]}},{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"input","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":%d}},{"match":{"op":"in","left":{"ct":{"key":"state"}},"right":{"set":["established","related"]}}},{"counter":{}},{"accept":null}]}},{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"input","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":%d}},{"counter":{}},{"drop":null}]}},{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"forward","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifgroup"}},"right":%d}},{"counter":{}},{"drop":null}]}},{"rule":{"family":"inet","table":"mdd_cellular_guard","chain":"forward","expr":[{"match":{"op":"==","left":{"meta":{"key":"oifgroup"}},"right":%d}},{"counter":{}},{"drop":null}]}}]}`, permit, InterfaceGroup, InterfaceGroup, InterfaceGroup, InterfaceGroup, InterfaceGroup))
}

func stringReplaceOnce(value, old, replacement string) string {
	index := strings.Index(value, old)
	if index < 0 {
		return value
	}
	return value[:index] + replacement + value[index+len(old):]
}
