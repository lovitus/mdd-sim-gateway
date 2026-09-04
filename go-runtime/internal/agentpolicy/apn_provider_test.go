package agentpolicy

import "testing"

func TestProviderAPNCandidatesUseBothMNCWidthsWithoutDuplicates(t *testing.T) {
	profiles := ProviderAPNCandidates("234101234567890")
	found := false
	seen := map[string]struct{}{}
	for _, profile := range profiles {
		if profile.APN == "giffgaff.com" && profile.Source == "provider" {
			found = true
		}
		if _, duplicate := seen[profile.APN]; duplicate {
			t.Fatalf("duplicate APN %q in %+v", profile.APN, profiles)
		}
		seen[profile.APN] = struct{}{}
	}
	if !found {
		t.Fatalf("public provider data did not return giffgaff APN: %+v", profiles)
	}
}
