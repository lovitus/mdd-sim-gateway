package providerapply

import "testing"

func TestAutomaticApplyCannotPublishUnrelatedChanges(t *testing.T) {
	for _, plan := range []Plan{
		{Safe: true},
		{Safe: true, Changed: []Change{{LineID: "existing"}}},
		{Safe: true, Removed: []Change{{LineID: "existing"}}},
		{Safe: true, Added: []Change{{LineID: "other"}}},
	} {
		if RestrictAddedLine(plan, "new").Safe {
			t.Fatal("automatic apply included unrelated changes")
		}
	}
	if !RestrictAddedLine(Plan{Safe: true, Added: []Change{{LineID: "new"}}}, "new").Safe {
		t.Fatal("new line rejected")
	}
	if RestrictAddedLine(Plan{Safe: false}, "new").Safe {
		t.Fatal("existing preflight failure bypassed")
	}
}
