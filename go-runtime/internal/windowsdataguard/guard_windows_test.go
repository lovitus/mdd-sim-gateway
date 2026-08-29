//go:build windows

package windowsdataguard

import (
	"context"
	"net/netip"
	"os"
	"testing"

	"github.com/tailscale/wf"
)

func TestBorrowDynamicWFPIntegration(t *testing.T) {
	attachmentID := os.Getenv("MDD_TEST_ATTACHMENT_ID")
	if attachmentID == "" {
		t.Skip("MDD_TEST_ATTACHMENT_ID is not set")
	}
	guard, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	borrow, err := guard.BeginBorrow(context.Background(), attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := borrow.Permit(context.Background(), "tcp", netip.MustParseAddr("203.0.113.9"), 443); err != nil {
		_ = borrow.Close()
		t.Fatal(err)
	}
	if err := borrow.Permit(context.Background(), "udp", netip.MustParseAddr("2001:db8::9"), 53); err != nil {
		_ = borrow.Close()
		t.Fatal(err)
	}
	if err := borrow.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBorrowRulesPermitOnlyExactAgentFlowAbovePersistentBlock(t *testing.T) {
	const (
		appID = `\\device\\harddiskvolume3\\mdd-agent.exe`
		luid  = uint64(0x0102030405060708)
		port  = uint16(443)
	)
	address := netip.MustParseAddr("203.0.113.9")
	rules := borrowRules(appID, luid, "tcp", address, port)
	if len(rules) != 2 {
		t.Fatalf("borrow rule count=%d, want 2", len(rules))
	}
	for _, rule := range rules {
		if rule.Action != wf.ActionPermit || !rule.HardAction || rule.Persistent ||
			rule.Sublayer != sublayerID || rule.Weight != 0xffff {
			t.Fatalf("unsafe borrow rule: %+v", rule)
		}
	}
	connect := rules[0]
	if connect.Layer != wf.LayerALEAuthConnectV4 || len(connect.Conditions) != 5 ||
		connect.Conditions[0].Field != wf.FieldALEAppID || connect.Conditions[0].Value != appID ||
		connect.Conditions[1].Field != wf.FieldIPLocalInterface || connect.Conditions[1].Value != luid ||
		connect.Conditions[2].Field != wf.FieldIPProtocol || connect.Conditions[2].Value != wf.IPProtoTCP ||
		connect.Conditions[3].Field != wf.FieldIPRemoteAddress || connect.Conditions[3].Value != address ||
		connect.Conditions[4].Field != wf.FieldIPRemotePort || connect.Conditions[4].Value != port {
		t.Fatalf("connect permit is not exact: %+v", connect)
	}
	packet := rules[1]
	if packet.Layer != wf.LayerOutboundIPPacketV4 || len(packet.Conditions) != 2 ||
		packet.Conditions[0].Field != wf.FieldIPLocalInterface || packet.Conditions[0].Value != luid ||
		packet.Conditions[1].Field != wf.FieldIPRemoteAddress || packet.Conditions[1].Value != address {
		t.Fatalf("packet permit is not exact: %+v", packet)
	}
}

func TestGlobalRulesCoverBothCellularInterfaceTypesAndFamilies(t *testing.T) {
	rules := globalRules()
	if len(rules) != 4 {
		t.Fatalf("global rule count = %d, want 4", len(rules))
	}
	want := map[struct {
		layer wf.LayerID
		kind  uint32
	}]bool{
		{wf.LayerOutboundIPPacketV4, ifTypeWWANPP}:  true,
		{wf.LayerOutboundIPPacketV4, ifTypeWWANPP2}: true,
		{wf.LayerOutboundIPPacketV6, ifTypeWWANPP}:  true,
		{wf.LayerOutboundIPPacketV6, ifTypeWWANPP2}: true,
	}
	for _, rule := range rules {
		if !rule.Persistent || rule.Action != wf.ActionBlock || !rule.HardAction ||
			rule.Provider != (wf.ProviderID{}) || rule.Sublayer != sublayerID {
			t.Fatalf("unsafe global rule: %+v", rule)
		}
		key := struct {
			layer wf.LayerID
			kind  uint32
		}{rule.Layer, rule.Conditions[0].Value.(uint32)}
		if !want[key] {
			t.Fatalf("unexpected global rule: %+v", key)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing global rules: %+v", want)
	}
}

func TestAttachmentRulesCoverConnectAndForward(t *testing.T) {
	const luid uint64 = 0x0102030405060708
	rules := attachmentRules("{12345678-1234-1234-1234-1234567890ab}", luid)
	if len(rules) != 4 {
		t.Fatalf("attachment rule count = %d, want 4", len(rules))
	}
	want := map[struct {
		layer wf.LayerID
		field wf.FieldID
	}]bool{
		{wf.LayerALEAuthConnectV4, wf.FieldIPLocalInterface}: true,
		{wf.LayerALEAuthConnectV6, wf.FieldIPLocalInterface}: true,
		{wf.LayerIPForwardV4, wf.FieldIPForwardInterface}:    true,
		{wf.LayerIPForwardV6, wf.FieldIPForwardInterface}:    true,
	}
	for _, rule := range rules {
		match := rule.Conditions[0]
		if match.Value != luid {
			t.Fatalf("rule LUID = %#v, want %#v", match.Value, luid)
		}
		key := struct {
			layer wf.LayerID
			field wf.FieldID
		}{rule.Layer, match.Field}
		if !want[key] {
			t.Fatalf("unexpected attachment rule: %+v", key)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing attachment rules: %+v", want)
	}
}

func TestSublayerContractAcceptsBFERaisedWeightOnly(t *testing.T) {
	base := wf.Sublayer{
		ID: sublayerID, Name: sublayerName, Persistent: true, Weight: sublayerMinimumWeight,
	}
	if !sublayerMatchesContract(&base) {
		t.Fatal("requested minimum sublayer weight was rejected")
	}
	base.Weight = sublayerMinimumWeight + 3
	if !sublayerMatchesContract(&base) {
		t.Fatal("BFE-raised sublayer weight was rejected")
	}
	base.Weight = sublayerMinimumWeight - 1
	if sublayerMatchesContract(&base) {
		t.Fatal("lower-priority sublayer weight was accepted")
	}
	base.Weight = sublayerMinimumWeight
	base.Persistent = false
	if sublayerMatchesContract(&base) {
		t.Fatal("non-persistent sublayer was accepted")
	}
}

func TestOnlyLUIDChanged(t *testing.T) {
	oldRule := attachmentRules("{12345678-1234-1234-1234-1234567890ab}", 1)[0]
	newRule := attachmentRules("{12345678-1234-1234-1234-1234567890ab}", 2)[0]
	if !onlyLUIDChanged(oldRule, newRule) {
		t.Fatal("expected a stale LUID to be replaceable")
	}
	newRule.Action = wf.ActionPermit
	if onlyLUIDChanged(oldRule, newRule) {
		t.Fatal("action drift must not be treated as a stale LUID")
	}
}

func TestRuleEqualityAcceptsUnavailablePersistedEffectiveWeight(t *testing.T) {
	expected := globalRules()[0]
	existing := *expected
	// wf returns zero for persisted rule weights on the target Windows build,
	// while netsh independently reports the requested and effective weight.
	existing.Weight = 0
	if !ruleEqual(&existing, expected) {
		t.Fatal("unavailable persisted effective weight rejected an otherwise exact hard block")
	}
	existing.HardAction = false
	if ruleEqual(&existing, expected) {
		t.Fatal("soft action was accepted when the weight was unavailable")
	}
}

func TestRuleIDStableAndLabelScoped(t *testing.T) {
	first := ruleID("same")
	if first != ruleID("same") {
		t.Fatal("rule ID is not deterministic")
	}
	if first == ruleID("different") {
		t.Fatal("different rule labels share an ID")
	}
}

func TestParseAttachmentIDAcceptsNormalizedMBNIdentifier(t *testing.T) {
	_, canonical, err := parseAttachmentID("0E92748C-8CA0-47BD-8B04-DA7400A9820E")
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "0e92748c-8ca0-47bd-8b04-da7400a9820e" {
		t.Fatalf("canonical attachment = %q", canonical)
	}
	if _, _, err := parseAttachmentID("not-an-interface"); err == nil {
		t.Fatal("invalid attachment accepted")
	}
}
