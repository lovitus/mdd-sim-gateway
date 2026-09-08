package providerapply

// RestrictAddedLine keeps automatic setup from publishing unrelated saved edits.
func RestrictAddedLine(plan Plan, lineID string) Plan {
	allowed := lineID != "" && len(plan.Added) == 1 && len(plan.Changed) == 0 && len(plan.Removed) == 0
	for _, change := range plan.Added {
		if change.LineID != lineID {
			allowed = false
		}
	}
	if !allowed {
		plan.Safe = false
		plan.Blockers = append(plan.Blockers, Blocker{LineID: lineID, Code: "unrelated_provider_changes_pending"})
	}
	return plan
}
