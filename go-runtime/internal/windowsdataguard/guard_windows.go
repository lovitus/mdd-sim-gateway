//go:build windows

package windowsdataguard

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"
)

const (
	guardName    = "MDD cellular data guard"
	sublayerName = "MDD cellular data quarantine"
	// BFE may return a higher effective sublayer weight than was requested;
	// the target Windows build did so when this value matched a built-in
	// sublayer. Treat it as the minimum accepted priority instead of requiring
	// the requested value to round-trip byte-for-byte.
	sublayerMinimumWeight uint16 = 0x7fff

	// These are the Windows interface types assigned to cellular packet data.
	ifTypeWWANPP  uint32 = 243
	ifTypeWWANPP2 uint32 = 244

	usbipdPort uint16 = 3240
)

var (
	sublayerID = wf.SublayerID(mustGUID("{8eb62901-66c8-43e1-a94c-ba76a0a5fc95}"))

	iphlpapi                   = windows.NewLazySystemDLL("iphlpapi.dll")
	procConvertInterfaceToLUID = iphlpapi.NewProc("ConvertInterfaceGuidToLuid")
	procConvertLUIDToIndex     = iphlpapi.NewProc("ConvertInterfaceLuidToIndex")
)

// Guard owns persistent Windows Filtering Platform objects which prevent the
// host (including forwarding software) from using cellular packet data. The
// objects deliberately outlive the Agent process; Close only releases the WFP
// management session.
type Guard struct {
	mu      sync.Mutex
	session *wf.Session
	rules   map[wf.RuleID]*wf.Rule
}

// Borrow owns a dynamic WFP session. Closing it atomically removes every
// temporary permit while the persistent quarantine rules remain installed.
type Borrow struct {
	mu      sync.Mutex
	session *wf.Session
	luid    uint64
	appID   string
	rules   map[string]struct{}
}

func (guard *Guard) BeginBorrow(ctx context.Context, attachmentID string) (*Borrow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	guid, _, err := parseAttachmentID(attachmentID)
	if err != nil {
		return nil, err
	}
	luid, err := interfaceLUID(&guid)
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve Agent executable: %w", err)
	}
	appID, err := wf.AppID(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve Agent WFP app ID: %w", err)
	}
	session, err := wf.New(&wf.Options{Name: "MDD cellular data borrowing", Description: "Dynamic permits for one explicit MDD borrowing session", Dynamic: true})
	if err != nil {
		return nil, fmt.Errorf("open dynamic WFP session: %w", err)
	}
	return &Borrow{session: session, luid: luid, appID: appID, rules: map[string]struct{}{}}, nil
}

func (borrow *Borrow) Permit(ctx context.Context, network string, address netip.Addr, port uint16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !address.IsValid() || address.IsUnspecified() || port == 0 || network != "tcp" && network != "udp" {
		return errors.New("invalid cellular permit target")
	}
	address = address.Unmap()
	key := fmt.Sprintf("%s/%s/%d", network, address, port)
	borrow.mu.Lock()
	defer borrow.mu.Unlock()
	if borrow.session == nil {
		return errors.New("cellular borrowing guard is closed")
	}
	if _, exists := borrow.rules[key]; exists {
		return nil
	}
	rules := borrowRules(borrow.appID, borrow.luid, network, address, port)
	for index, rule := range rules {
		if err := borrow.session.AddRule(rule); err != nil {
			for prior := 0; prior < index; prior++ {
				_ = borrow.session.DeleteRule(rules[prior].ID)
			}
			return fmt.Errorf("add dynamic cellular permit: %w", err)
		}
	}
	borrow.rules[key] = struct{}{}
	return nil
}

func borrowRules(appID string, luid uint64, network string, address netip.Addr, port uint16) []*wf.Rule {
	protocol := wf.IPProtoTCP
	if network == "udp" {
		protocol = wf.IPProtoUDP
	}
	family, connectLayer, packetLayer := "v4", wf.LayerALEAuthConnectV4, wf.LayerOutboundIPPacketV4
	if address.Is6() {
		family, connectLayer, packetLayer = "v6", wf.LayerALEAuthConnectV6, wf.LayerOutboundIPPacketV6
	}
	label := fmt.Sprintf("borrow-%s-%s/%s/%d", family, network, address, port)
	return []*wf.Rule{
		permitRule(label+"-connect", connectLayer,
			&wf.Match{Field: wf.FieldALEAppID, Op: wf.MatchTypeEqual, Value: appID},
			&wf.Match{Field: wf.FieldIPLocalInterface, Op: wf.MatchTypeEqual, Value: luid},
			&wf.Match{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: protocol},
			&wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: address},
			&wf.Match{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeEqual, Value: port}),
		permitRule(label+"-packet", packetLayer,
			&wf.Match{Field: wf.FieldIPLocalInterface, Op: wf.MatchTypeEqual, Value: luid},
			&wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: address}),
	}
}

func (borrow *Borrow) InterfaceIndex() (uint32, error) {
	borrow.mu.Lock()
	defer borrow.mu.Unlock()
	if borrow.session == nil {
		return 0, errors.New("cellular borrowing guard is closed")
	}
	var index uint32
	status, _, _ := procConvertLUIDToIndex.Call(uintptr(unsafe.Pointer(&borrow.luid)), uintptr(unsafe.Pointer(&index)))
	if status != 0 {
		return 0, windows.Errno(status)
	}
	if index == 0 {
		return 0, errors.New("cellular interface index is zero")
	}
	return index, nil
}

func permitRule(label string, layer wf.LayerID, conditions ...*wf.Match) *wf.Rule {
	return &wf.Rule{ID: ruleID(label), Name: "MDD cellular borrow: " + label,
		Description: "Dynamic exact-flow permit; removed when the borrowing session closes",
		Layer:       layer, Sublayer: sublayerID, Weight: 0xffff, Conditions: conditions,
		Action: wf.ActionPermit, HardAction: true}
}

func (borrow *Borrow) Close() error {
	borrow.mu.Lock()
	defer borrow.mu.Unlock()
	if borrow.session == nil {
		return nil
	}
	err := borrow.session.Close()
	borrow.session = nil
	borrow.rules = nil
	return err
}

// New opens WFP and installs the interface-type rules first. Those rules cover
// newly hot-plugged WWAN interfaces before an MBN inventory cycle learns their
// attachment GUID. Per-attachment rules are added by Protect.
func New() (*Guard, error) {
	session, err := wf.New(&wf.Options{
		Name:        guardName,
		Description: "Persistent fail-closed guard for MDD-managed cellular interfaces",
		Dynamic:     false,
	})
	if err != nil {
		return nil, fmt.Errorf("open WFP session: %w", err)
	}
	guard := &Guard{session: session, rules: make(map[wf.RuleID]*wf.Rule)}
	if err := guard.initialize(); err != nil {
		_ = session.Close()
		return nil, err
	}
	return guard, nil
}

func (guard *Guard) initialize() error {
	if err := guard.ensureSublayer(); err != nil {
		return err
	}
	rules, err := guard.session.Rules()
	if err != nil {
		return fmt.Errorf("enumerate WFP rules: %w", err)
	}
	for _, rule := range rules {
		guard.rules[rule.ID] = rule
	}
	for _, rule := range globalRules() {
		if err := guard.ensureRule(rule, false); err != nil {
			return err
		}
	}
	return nil
}

func (guard *Guard) ensureSublayer() error {
	sublayers, err := guard.session.Sublayers()
	if err != nil {
		return fmt.Errorf("enumerate WFP sublayers: %w", err)
	}
	for _, sublayer := range sublayers {
		if sublayer.ID != sublayerID {
			continue
		}
		if !sublayerMatchesContract(sublayer) {
			return errors.New("existing MDD WFP sublayer does not match the fail-closed contract")
		}
		return nil
	}
	if err := guard.session.AddSublayer(&wf.Sublayer{
		ID:          sublayerID,
		Name:        sublayerName,
		Description: "Blocks host and forwarded traffic on cellular interfaces",
		Persistent:  true,
		Weight:      sublayerMinimumWeight,
	}); err != nil {
		return fmt.Errorf("add WFP sublayer: %w", err)
	}
	return nil
}

func sublayerMatchesContract(sublayer *wf.Sublayer) bool {
	return sublayer != nil && sublayer.Name == sublayerName && sublayer.Persistent &&
		sublayer.Provider == (wf.ProviderID{}) && sublayer.Weight >= sublayerMinimumWeight
}

// Protect adds attachment-specific blocks after resolving the MBN attachment
// GUID to its current NET_LUID. It is idempotent and replaces only an older MDD
// rule whose sole difference is a stale LUID from an earlier Windows boot.
func (guard *Guard) Protect(ctx context.Context, attachmentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	guid, canonical, err := parseAttachmentID(attachmentID)
	if err != nil {
		return err
	}
	luid, err := interfaceLUID(&guid)
	if err != nil {
		return fmt.Errorf("resolve cellular interface %s: %w", canonical, err)
	}

	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.session == nil {
		return errors.New("cellular data guard is closed")
	}
	for _, rule := range attachmentRules(canonical, luid) {
		if err := guard.ensureRule(rule, true); err != nil {
			return err
		}
	}
	return nil
}

func (guard *Guard) ensureRule(expected *wf.Rule, allowStaleLUID bool) error {
	if existing := guard.rules[expected.ID]; existing != nil {
		if ruleEqual(existing, expected) {
			return nil
		}
		if !allowStaleLUID || !onlyLUIDChanged(existing, expected) {
			return fmt.Errorf("existing MDD WFP rule %q does not match the fail-closed contract", expected.Name)
		}
		if err := guard.session.DeleteRule(existing.ID); err != nil {
			return fmt.Errorf("replace stale WFP rule %q: %w", expected.Name, err)
		}
		delete(guard.rules, existing.ID)
	}
	if err := guard.session.AddRule(expected); err != nil {
		return fmt.Errorf("add WFP rule %q: %w", expected.Name, err)
	}
	guard.rules[expected.ID] = expected
	return nil
}

func globalRules() []*wf.Rule {
	var rules []*wf.Rule
	for _, family := range []struct {
		name  string
		layer wf.LayerID
	}{{"v4", wf.LayerOutboundIPPacketV4}, {"v6", wf.LayerOutboundIPPacketV6}} {
		for _, interfaceType := range []uint32{ifTypeWWANPP, ifTypeWWANPP2} {
			label := fmt.Sprintf("global-%s-iftype-%d", family.name, interfaceType)
			rules = append(rules, blockRule(label, family.layer, wf.FieldInterfaceType, interfaceType))
		}
	}
	// usbipd-win remains the signed driver and persistent PnP record owner, but
	// its user-mode TCP service is disabled and MDD never uses port 3240.
	// Persistent hard blocks remain as fail-closed protection if an MSI repair
	// starts the service or recreates the upstream allow rule.
	rules = append(rules,
		blockRuleWithMatches("usbipd-non-loopback-v4", wf.LayerALEAuthRecvAcceptV4,
			&wf.Match{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoTCP},
			&wf.Match{Field: wf.FieldIPLocalPort, Op: wf.MatchTypeEqual, Value: usbipdPort},
			&wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeNotEqual, Value: netip.MustParseAddr("127.0.0.1")}),
		blockRuleWithMatches("usbipd-non-loopback-v6", wf.LayerALEAuthRecvAcceptV6,
			&wf.Match{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoTCP},
			&wf.Match{Field: wf.FieldIPLocalPort, Op: wf.MatchTypeEqual, Value: usbipdPort},
			&wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeNotEqual, Value: netip.MustParseAddr("::1")}),
		blockRuleWithMatches("usbipd-local-client-v4", wf.LayerALEAuthConnectV4,
			&wf.Match{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoTCP},
			&wf.Match{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeEqual, Value: usbipdPort},
			&wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: netip.MustParseAddr("127.0.0.1")}),
		blockRuleWithMatches("usbipd-local-client-v6", wf.LayerALEAuthConnectV6,
			&wf.Match{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoTCP},
			&wf.Match{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeEqual, Value: usbipdPort},
			&wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: netip.MustParseAddr("::1")}),
	)
	return rules
}

func attachmentRules(attachment string, luid uint64) []*wf.Rule {
	return []*wf.Rule{
		blockRule("attachment-"+attachment+"-connect-v4", wf.LayerALEAuthConnectV4, wf.FieldIPLocalInterface, luid),
		blockRule("attachment-"+attachment+"-connect-v6", wf.LayerALEAuthConnectV6, wf.FieldIPLocalInterface, luid),
		blockRule("attachment-"+attachment+"-forward-v4", wf.LayerIPForwardV4, wf.FieldIPForwardInterface, luid),
		blockRule("attachment-"+attachment+"-forward-v6", wf.LayerIPForwardV6, wf.FieldIPForwardInterface, luid),
	}
}

func blockRule(label string, layer wf.LayerID, field wf.FieldID, value interface{}) *wf.Rule {
	return blockRuleWithMatches(label, layer,
		&wf.Match{Field: field, Op: wf.MatchTypeEqual, Value: value})
}

func blockRuleWithMatches(label string, layer wf.LayerID, conditions ...*wf.Match) *wf.Rule {
	return &wf.Rule{
		ID:          ruleID(label),
		Name:        "MDD cellular quarantine: " + label,
		Description: "Persistent hard block; managed borrowing must explicitly replace this rule",
		Layer:       layer,
		Sublayer:    sublayerID,
		Weight:      0x7fff,
		Conditions:  conditions,
		Action:      wf.ActionBlock,
		HardAction:  true,
		Persistent:  true,
	}
}

// ruleEqual compares every field that can weaken or retarget the block. Rule
// weight is intentionally absent: wf reports zero after reopening these
// persisted rules on the target Windows build even though netsh reports both
// their requested and effective weights as 0x7fff.
func ruleEqual(left, right *wf.Rule) bool {
	return left.ID == right.ID && left.Layer == right.Layer && left.Sublayer == right.Sublayer &&
		left.Action == right.Action && left.HardAction == right.HardAction &&
		left.Persistent == right.Persistent && left.BootTime == right.BootTime && left.Provider == right.Provider &&
		matchesEqual(left.Conditions, right.Conditions)
}

func onlyLUIDChanged(left, right *wf.Rule) bool {
	if len(left.Conditions) != 1 || len(right.Conditions) != 1 {
		return false
	}
	leftCopy, rightCopy := *left, *right
	leftCopy.Conditions, rightCopy.Conditions = nil, nil
	if !ruleEqual(&leftCopy, &rightCopy) {
		return false
	}
	return left.Conditions[0].Field == right.Conditions[0].Field &&
		left.Conditions[0].Op == right.Conditions[0].Op && isUint64(left.Conditions[0].Value) && isUint64(right.Conditions[0].Value)
}

func matchesEqual(left, right []*wf.Match) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Field != right[index].Field || left[index].Op != right[index].Op {
			return false
		}
		switch want := right[index].Value.(type) {
		case wf.IPProto:
			switch got := left[index].Value.(type) {
			case wf.IPProto:
				if got != want {
					return false
				}
			case uint8:
				if got != uint8(want) {
					return false
				}
			default:
				return false
			}
		case uint32:
			got, ok := left[index].Value.(uint32)
			if !ok || got != want {
				return false
			}
		case uint64:
			got, ok := left[index].Value.(uint64)
			if !ok || got != want {
				return false
			}
		case uint16:
			got, ok := left[index].Value.(uint16)
			if !ok || got != want {
				return false
			}
		case uint8:
			got, ok := left[index].Value.(uint8)
			if !ok || got != want {
				return false
			}
		case netip.Addr:
			got, ok := left[index].Value.(netip.Addr)
			if !ok || got.Unmap() != want.Unmap() {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func isUint64(value interface{}) bool {
	_, ok := value.(uint64)
	return ok
}

func parseAttachmentID(value string) (windows.GUID, string, error) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")
	if trimmed == "" || strings.ContainsAny(trimmed, "{}") {
		return windows.GUID{}, "", fmt.Errorf("invalid MBN attachment GUID %q", value)
	}
	guid, err := windows.GUIDFromString("{" + trimmed + "}")
	if err != nil {
		return windows.GUID{}, "", fmt.Errorf("invalid MBN attachment GUID %q: %w", trimmed, err)
	}
	canonical := strings.Trim(strings.ToLower(guid.String()), "{}")
	return guid, canonical, nil
}

func interfaceLUID(guid *windows.GUID) (uint64, error) {
	var luid uint64
	status, _, _ := procConvertInterfaceToLUID.Call(uintptr(unsafe.Pointer(guid)), uintptr(unsafe.Pointer(&luid)))
	if status != 0 {
		return 0, windows.Errno(status)
	}
	return luid, nil
}

func ruleID(label string) wf.RuleID {
	sum := sha256.Sum256([]byte("mdd-cellular-data-guard/v1/" + label))
	return wf.RuleID(windows.GUID{
		Data1: binary.BigEndian.Uint32(sum[0:4]),
		Data2: binary.BigEndian.Uint16(sum[4:6]),
		Data3: binary.BigEndian.Uint16(sum[6:8]),
		Data4: [8]byte(sum[8:16]),
	})
}

func mustGUID(value string) windows.GUID {
	guid, err := windows.GUIDFromString(value)
	if err != nil {
		panic(err)
	}
	return guid
}

// Close releases only the management handle. Persistent WFP objects remain in
// force by design, including after Agent shutdown or failure.
func (guard *Guard) Close() error {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.session == nil {
		return nil
	}
	err := guard.session.Close()
	guard.session = nil
	return err
}
