package cellularmedia

import (
	"errors"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func TestUpdateBusyIncludesPreparingReadyAndIncomingOwnership(t *testing.T) {
	for _, phase := range []string{"preparing", "ready", "starting", "active", "uncertain", "ending"} {
		t.Run(phase, func(t *testing.T) {
			service := &Service{sessions: map[string]*session{"session-one": {phase: phase}}, claims: map[string]*incomingClaim{}}
			if service.UpdateBusy() == nil {
				t.Fatalf("CELLULAR_UPDATE_BUSY: %s was not fenced", phase)
			}
			delete(service.sessions, "session-one")
			if err := service.UpdateBusy(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, state := range []string{"claiming", "uncertain", "active", "terminal"} {
		service := &Service{claims: map[string]*incomingClaim{"event-one": {state: state}}}
		if (service.UpdateBusy() != nil) != (state != "terminal") {
			t.Fatalf("CELLULAR_CLAIM_BUSY: %s", state)
		}
	}
}

func TestUpdateBlocksNewMediaBeforeResolvingHardware(t *testing.T) {
	blocked := errors.New("original update retained")
	service := &Service{config: Config{Catalog: fakeCatalog{line: linecatalog.Line{ID: "line"}}, Admit: func() (func(), error) { return nil, blocked }}}
	// The missing card would be a different failure after catalog resolution.
	if _, err := service.prepare(t.Context(), "subject", "line", "call", "8985200000000000001", "out", nil); !errors.Is(err, blocked) {
		t.Fatalf("MEDIA_ADMISSION: %v", err)
	}
}
