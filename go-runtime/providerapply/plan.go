package providerapply

import (
	"sort"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
)

type Plan struct {
	SchemaVersion   int       `json:"schema_version"`
	CatalogRevision uint64    `json:"catalog_revision"`
	Safe            bool      `json:"safe"`
	Added           []Change  `json:"added"`
	Changed         []Change  `json:"changed"`
	Removed         []Change  `json:"removed"`
	Blockers        []Blocker `json:"blockers"`
}

type Change struct {
	LineID       string `json:"line_id"`
	UnitInstance string `json:"unit_instance"`
}

type Blocker struct {
	LineID string `json:"line_id,omitempty"`
	Code   string `json:"code"`
}

func BuildPlan(current, candidate providerconfig.Manifest, preflight Snapshot) Plan {
	plan := Plan{SchemaVersion: 1, CatalogRevision: candidate.CatalogRevision,
		Added: []Change{}, Changed: []Change{}, Removed: []Change{}, Blockers: []Blocker{}}
	if candidate.SchemaVersion != 1 || candidate.CatalogRevision == 0 || preflight.CatalogRevision != candidate.CatalogRevision {
		plan.Blockers = append(plan.Blockers, Blocker{Code: "catalog_revision_changed"})
	}
	oldLines, newLines := manifestMap(current), manifestMap(candidate)
	for lineID, next := range newLines {
		previous, exists := oldLines[lineID]
		switch {
		case !exists:
			plan.Added = append(plan.Added, Change{LineID: lineID, UnitInstance: next.UnitInstance})
		case previous.UnitInstance != next.UnitInstance || previous.ConfigSHA256 != next.ConfigSHA256:
			plan.Changed = append(plan.Changed, Change{LineID: lineID, UnitInstance: next.UnitInstance})
		}
	}
	for lineID, previous := range oldLines {
		if _, exists := newLines[lineID]; !exists {
			plan.Removed = append(plan.Removed, Change{LineID: lineID, UnitInstance: previous.UnitInstance})
		}
	}
	statuses := make(map[string]LineStatus, len(preflight.Lines))
	for _, status := range preflight.Lines {
		statuses[status.LineID] = status
	}
	for _, change := range plan.Added {
		if status, found := statuses[change.LineID]; found && status.ProviderPresent {
			code := "provider_already_present"
			if status.ActiveCall != nil {
				code = "active_call"
			}
			plan.Blockers = append(plan.Blockers, Blocker{LineID: change.LineID, Code: code})
		}
	}
	for _, change := range append(append([]Change{}, plan.Changed...), plan.Removed...) {
		status, found := statuses[change.LineID]
		switch {
		case !found:
			plan.Blockers = append(plan.Blockers, Blocker{LineID: change.LineID, Code: "preflight_missing"})
		case status.ActiveCall != nil:
			plan.Blockers = append(plan.Blockers, Blocker{LineID: change.LineID, Code: "active_call"})
		case status.Code != "provider_reachable" && status.Code != "provider_absent":
			plan.Blockers = append(plan.Blockers, Blocker{LineID: change.LineID, Code: "provider_unreachable"})
		}
	}
	sortChanges(plan.Added)
	sortChanges(plan.Changed)
	sortChanges(plan.Removed)
	sort.Slice(plan.Blockers, func(left, right int) bool {
		if plan.Blockers[left].LineID == plan.Blockers[right].LineID {
			return plan.Blockers[left].Code < plan.Blockers[right].Code
		}
		return plan.Blockers[left].LineID < plan.Blockers[right].LineID
	})
	plan.Safe = len(plan.Blockers) == 0
	return plan
}

func manifestMap(manifest providerconfig.Manifest) map[string]providerconfig.ManifestEntry {
	result := make(map[string]providerconfig.ManifestEntry, len(manifest.Providers))
	for _, entry := range manifest.Providers {
		result[entry.LineID] = entry
	}
	return result
}

func sortChanges(changes []Change) {
	sort.Slice(changes, func(left, right int) bool { return changes[left].LineID < changes[right].LineID })
}
