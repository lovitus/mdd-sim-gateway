package notifications

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type recoveryNoticeSource struct {
	*events.BoltStore
	loseAck bool
}

func (source *recoveryNoticeSource) AckExitRecoveryNotice(id string) error {
	if source.loseAck {
		source.loseAck = false
		return errors.New("ack unavailable")
	}
	return source.BoltStore.AckExitRecoveryNotice(id)
}

func TestRecoveryNoticeSurvivesLostAckWithoutDuplicateDelivery(t *testing.T) {
	store := openNotificationStore(t)
	now := time.Now().UTC()
	config := enableWebhook(t, store, now)
	if config.Webhook.Events.LineUnrecoverable {
		t.Fatal("new subscription was enabled implicitly")
	}
	config.Webhook.Events.LineUnrecoverable = true
	if _, _, err := store.PutConfigExpected(config.Revision, config, now); err != nil {
		t.Fatal(err)
	}
	eventStore, err := events.OpenBoltStore(filepath.Join(t.TempDir(), "events.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer eventStore.Close()
	notice := &recovery.ExitNotice{ID: strings.Repeat("a", 64), LineID: "line-1", LineName: "Fixture", CardID: "8944100000000000001", Text: "Recovery needs attention", OccurredAt: now}
	if _, err := eventStore.PutExitRecoveryExpected("line-1", recovery.ExitLedger{Failures: 6, Reported: true, Notice: notice}, 0); err != nil {
		t.Fatal(err)
	}
	source := &recoveryNoticeSource{BoltStore: eventStore, loseAck: true}
	coordinator := &Coordinator{config: CoordinatorConfig{Store: store, Recovery: source}}
	if err := coordinator.drainRecovery(now); err == nil {
		t.Fatal("lost acknowledgement was hidden")
	}
	if err := coordinator.drainRecovery(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	deliveries, err := store.Deliveries(100)
	if err != nil || len(deliveries) != 1 || deliveries[0].EventType != EventLineUnrecoverable {
		t.Fatal(deliveries, err)
	}
	pending, err := eventStore.PendingExitRecoveryNotices(100)
	if err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
}

func TestLegacyRecoveryEventChoiceIsRetained(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		subscriptions, warnings := legacySubscriptions(map[string]bool{EventLineUnrecoverable: enabled}, nil)
		if subscriptions.LineUnrecoverable != enabled || len(warnings) != 0 {
			t.Fatal(subscriptions, warnings)
		}
	}
	if DefaultConfig().Telegram.Events.LineUnrecoverable {
		t.Fatal("upgrade must not subscribe existing channels implicitly")
	}
}
