package providerapply

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

func TestBuildPlanDiffsAndRequiresVerifiedIdle(t *testing.T) {
	current := testManifest(3,
		testEntry("line-a", "a"), testEntry("line-b", "b-old"), testEntry("line-c", "c"))
	candidate := testManifest(4,
		testEntry("line-b", "b-new"), testEntry("line-c", "c"), testEntry("line-d", "d"))
	preflight := Snapshot{SchemaVersion: 1, CatalogRevision: 4, Lines: []LineStatus{
		{LineID: "line-a", Code: "provider_absent"},
		{LineID: "line-b", Code: "provider_reachable", ProviderPresent: true},
		{LineID: "line-d", Code: "provider_absent"},
	}}
	plan := BuildPlan(current, candidate, preflight)
	if !plan.Safe || len(plan.Added) != 1 || plan.Added[0].LineID != "line-d" ||
		len(plan.Changed) != 1 || plan.Changed[0].LineID != "line-b" ||
		len(plan.Removed) != 1 || plan.Removed[0].LineID != "line-a" || len(plan.Blockers) != 0 {
		t.Fatalf("plan=%+v", plan)
	}
	preflight.Lines[1].ActiveCall = &vowifiipc.ActiveCall{CallID: "call-1", Condition: vowifiipc.CallActive}
	plan = BuildPlan(current, candidate, preflight)
	if plan.Safe || len(plan.Blockers) != 1 || plan.Blockers[0].Code != "active_call" {
		t.Fatalf("active plan=%+v", plan)
	}
}

func TestBuildPlanBlocksRevisionChangeAndUnmanagedProvider(t *testing.T) {
	candidate := testManifest(8, testEntry("line-a", "a"))
	preflight := Snapshot{SchemaVersion: 1, CatalogRevision: 7, Lines: []LineStatus{{
		LineID: "line-a", Code: "provider_reachable", ProviderPresent: true,
	}}}
	plan := BuildPlan(providerconfig.Manifest{}, candidate, preflight)
	if plan.Safe || len(plan.Blockers) != 2 || plan.Blockers[0].Code != "catalog_revision_changed" ||
		plan.Blockers[1].Code != "provider_already_present" {
		t.Fatalf("plan=%+v", plan)
	}
}

func testManifest(revision uint64, entries ...providerconfig.ManifestEntry) providerconfig.Manifest {
	return providerconfig.Manifest{SchemaVersion: 1, CatalogRevision: revision, Providers: entries}
}

func testEntry(lineID, hashSeed string) providerconfig.ManifestEntry {
	digest := sha256.Sum256([]byte(hashSeed))
	instance := providerconfig.UnitInstance(lineID)
	return providerconfig.ManifestEntry{
		LineID: lineID, UnitInstance: instance, ConfigFile: instance + ".json",
		ConfigSHA256: hex.EncodeToString(digest[:]),
	}
}
