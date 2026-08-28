package providerdeploy

import "testing"

func TestCheckedUnitRejectsUnexpectedSystemdTargetsWithoutPanicking(t *testing.T) {
	if unit, err := checkedUnit("mdd-vowifi@line-123.service"); err != nil || unit == "" {
		t.Fatalf("valid unit=%q err=%v", unit, err)
	}
	for _, unit := range []string{"ssh.service", "mdd-vowifi@../ssh.service", "mdd-vowifi@line-1.service --now"} {
		if _, err := checkedUnit(unit); err == nil {
			t.Fatalf("unsafe unit %q was accepted", unit)
		}
	}
}
