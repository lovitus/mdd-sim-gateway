package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	upstreamswu "github.com/boa-z/vowifi-go/engine/swu"
)

type scheduledPacketSession struct {
	fakePacketSession
	due              time.Time
	calls            int
	failure          error
	stopAfterAttempt bool
}

func (session *scheduledPacketSession) NextChildSARekeyDue() (time.Time, bool) {
	if session.stopAfterAttempt && session.calls > 0 {
		return time.Time{}, false
	}
	return session.due, true
}

func TestRekeyMaintenanceRetainsTunnelAndFailureBackoff(t *testing.T) {
	base := &scheduledPacketSession{due: time.Now().Add(-time.Second), failure: errors.New("rekey rejected"), stopAfterAttempt: true}
	session := &Session{base: base}
	if err := session.RunRekeyMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	retryAt, failure := session.RekeyMaintenanceStatus()
	if !errors.Is(failure, base.failure) || time.Until(retryAt) < 4*time.Minute || base.calls != 1 || base.closed {
		t.Fatalf("failure=%v retry=%v calls=%d closed=%v", failure, retryAt, base.calls, base.closed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base = &scheduledPacketSession{due: time.Now().Add(time.Hour)}
	if err := (&Session{base: base}).RunRekeyMaintenance(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if base.calls != 0 {
		t.Fatal("canceled maintenance executed a rekey")
	}
}
func (session *scheduledPacketSession) ChildSARekeySnapshot() upstreamswu.ChildSARekeySnapshot {
	return upstreamswu.ChildSARekeySnapshot{Enabled: true, DueAt: session.due}
}
func (session *scheduledPacketSession) RekeyChildSA(context.Context) (upstreamswu.TunnelResult, error) {
	session.calls++
	return session.result, session.failure
}
func (session *scheduledPacketSession) RunChildSARekeyDue(_ context.Context, now time.Time) (upstreamswu.ChildSARekeyDecision, error) {
	session.calls++
	return upstreamswu.ChildSARekeyDecision{DueAt: now}, session.failure
}

func TestSessionForwardsRekeyWithoutInventingSuccess(t *testing.T) {
	base := &scheduledPacketSession{due: time.Now(), failure: errors.New("rekey rejected")}
	session := &Session{base: base}
	if due, ok := session.NextChildSARekeyDue(); !ok || !due.Equal(base.due) {
		t.Fatal("schedule lost")
	}
	if !session.ChildSARekeySnapshot().Enabled {
		t.Fatal("snapshot lost")
	}
	if _, err := session.RunChildSARekeyDue(context.Background(), base.due); !errors.Is(err, base.failure) {
		t.Fatalf("failure lost: %v", err)
	}
	if base.calls != 1 {
		t.Fatal("adapter repeated rekey")
	}
	session.closed.Store(true)
	if _, err := session.RekeyChildSA(context.Background()); !errors.Is(err, ErrProviderSessionClose) {
		t.Fatalf("closed session: %v", err)
	}
	if base.calls != 1 {
		t.Fatal("closed session reached upstream")
	}
	if _, err := (&Session{base: &fakePacketSession{}}).RekeyChildSA(context.Background()); !errors.Is(err, ErrRekeyUnsupported) {
		t.Fatalf("unsupported session: %v", err)
	}
}
