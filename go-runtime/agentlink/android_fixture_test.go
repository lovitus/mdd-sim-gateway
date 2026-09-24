package agentlink

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAndroidNativeHealthFixture(t *testing.T) {
	raw, err := os.ReadFile("../../android-agent/app/src/test/resources/health.json")
	if err != nil {
		t.Fatal(err)
	}
	var report HealthReport
	if err = json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if err = report.Validate(); err != nil {
		t.Fatal(err)
	}
	if report.Topology == nil || len(report.Topology.Readers) != 1 {
		t.Fatal("reader was not registered")
	}
}
