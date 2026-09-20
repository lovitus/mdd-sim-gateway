package agentlink

import (
	"errors"
	"strings"
	"testing"
)

func TestTopologyDiagnosticIdentifiesInvalidReaderFieldWithoutValues(t *testing.T) {
	for _, tc := range []struct {
		field  string
		change func(*ReaderFact)
	}{
		{"card_id", func(r *ReaderFact) { r.CardID = "secret-invalid-card" }},
		{"atr_sha256", func(r *ReaderFact) { r.ATRSHA256 = "secret-atr" }},
		{"session_generation", func(r *ReaderFact) { r.SessionGeneration = "private space" }},
		{"identity_detail", func(r *ReaderFact) { r.IdentityDetail = strings.Repeat("private", 200) }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			topology := TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{{ReaderName: "private-reader", IdentityState: CardAbsent}}}
			tc.change(&topology.Readers[0])
			err := topology.Validate()
			if err == nil || TopologyValidationField(err) != "readers[0]."+tc.field {
				t.Fatalf("field=%s error=%v", TopologyValidationField(err), err)
			}
			if err.Error() != "Agent topology contains an invalid card fact" {
				t.Fatalf("rule changed or raw values exposed: %v", err)
			}
		})
	}
	if TopologyValidationField(errors.New("private value")) != "topology" {
		t.Fatal("untyped error leaked into field diagnostic")
	}
}
