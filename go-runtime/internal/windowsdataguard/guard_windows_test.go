//go:build windows

package windowsdataguard

import (
	"testing"

	"github.com/tailscale/wf"
)

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
