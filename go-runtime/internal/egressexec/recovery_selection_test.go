package egressexec

import (
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
)

func TestRecoverySelectionIsExactWithoutChangingUserPin(t *testing.T) {
	nodes, err := parseSubscription([]byte(testSubscription))
	if err != nil {
		t.Fatal(err)
	}
	document := egressDocument(strings.Repeat("a", 64), "")
	document.EgressConfigRevision, document.CatalogRevision = 3, 4
	document.Proxy.Profiles["node"] = egressconfig.Profile{Type: "subscription"}
	exit := document.Proxy.Exits["gb"]
	exit.Keywords = []string{"GB"}
	document.Proxy.Exits["gb"] = exit
	request := egressconfig.RecoveryRequest{SchemaVersion: egressconfig.SchemaVersion, ConfigRevision: 3, CatalogRevision: 4,
		LineID: "line-1", ProviderGeneration: "provider-1", FailureID: strings.Repeat("b", 64), ExpectedGeneration: strings.Repeat("a", 64),
		Country: "gb", FromNode: "GB London", ToNode: "GB Manchester"}
	document.RecoverySelections = map[string]egressconfig.RecoveryRequest{"gb": request}
	result, err := renderAtBase(document, 22000, map[string][]subscriptionNode{"node": nodes}, map[string]string{"gb": "GB London"})
	if err != nil || result.Status.Exits["gb"].Node != "GB Manchester" {
		t.Fatal(result.Status, err)
	}
	if document.Proxy.Exits["gb"].PinnedNode != "" {
		t.Fatal("recovery changed user pin")
	}
	request.ToNode = "missing"
	document.RecoverySelections["gb"] = request
	if _, err := renderAtBase(document, 22000, map[string][]subscriptionNode{"node": nodes}, nil); err == nil {
		t.Fatal("missing recovery target fell back")
	}
	request.ToNode = "GB Manchester"
	document.RecoverySelections["gb"] = request
	exit.PinnedNode, exit.PinMode = "GB London", "lock"
	document.Proxy.Exits["gb"] = exit
	if _, err := renderAtBase(document, 22000, map[string][]subscriptionNode{"node": nodes}, nil); err == nil {
		t.Fatal("user-locked node was overridden")
	}
	exit.PinMode = "prefer"
	document.Proxy.Exits["gb"] = exit
	if _, err := renderAtBase(document, 22000, map[string][]subscriptionNode{"node": nodes}, nil); err != nil {
		t.Fatal(err)
	}
	document.CatalogRevision++
	if _, err := renderAtBase(document, 22000, map[string][]subscriptionNode{"node": nodes}, nil); err == nil {
		t.Fatal("stale selection accepted")
	}
}
