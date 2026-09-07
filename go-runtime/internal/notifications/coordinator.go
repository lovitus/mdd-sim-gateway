package notifications

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/allowance"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systemstatus"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

type SMSOutbox interface {
	PendingNotificationSources(int) ([]providermessages.NotificationSource, error)
	AckNotificationSource(string) error
}

type CallOutbox interface {
	PendingNotificationSources(time.Time, int) ([]callhistory.NotificationSource, error)
	AckNotificationSource(string) error
}

type SystemStatusSource interface {
	Snapshot(time.Time) systemstatus.Snapshot
}

type CatalogSource interface {
	Snapshot() (linecatalog.Snapshot, error)
	Get(string) (linecatalog.Line, error)
}

type AllowanceSource interface {
	Snapshot(string) (allowance.Snapshot, error)
}

type CoordinatorConfig struct {
	Context      context.Context
	Store        *Store
	Engine       *Engine
	SMS          SMSOutbox
	Calls        CallOutbox
	SystemStatus SystemStatusSource
	Catalog      CatalogSource
	Allowance    AllowanceSource
	Now          func() time.Time
	Interval     time.Duration
	Logf         func(string, ...any)
}

type Coordinator struct {
	config        CoordinatorConfig
	ctx           context.Context
	cancel        context.CancelFunc
	wake          chan struct{}
	wait          sync.WaitGroup
	engineStarted bool
}

func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {
	if config.Context == nil || config.Store == nil || config.Engine == nil || config.SMS == nil ||
		config.Calls == nil || config.SystemStatus == nil || config.Catalog == nil || config.Allowance == nil {
		return nil, errors.New("invalid notification coordinator configuration")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Interval == 0 {
		config.Interval = time.Second
	}
	if config.Interval < 100*time.Millisecond || config.Interval > time.Minute {
		return nil, errors.New("invalid notification coordinator interval")
	}
	if config.Logf == nil {
		config.Logf = log.Printf
	}
	ctx, cancel := context.WithCancel(config.Context)
	return &Coordinator{config: config, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}, nil
}

func (coordinator *Coordinator) Start() {
	coordinator.wait.Add(1)
	go coordinator.run()
}

func (coordinator *Coordinator) Close() error {
	coordinator.cancel()
	coordinator.wait.Wait()
	return coordinator.config.Engine.Close()
}

func (coordinator *Coordinator) Wake() {
	select {
	case coordinator.wake <- struct{}{}:
	default:
	}
}

func (coordinator *Coordinator) ValidateNotificationEvent(_ context.Context, event Event) string {
	if event.Type != EventActivationReminder || event.Reminder == nil {
		return ""
	}
	line, err := coordinator.config.Catalog.Get(event.LineID)
	if err != nil || strings.TrimSpace(line.CardID) != event.Reminder.ExpectedCardID || line.CardID != event.CardID {
		return "activation_reminder_card_changed"
	}
	snapshot, err := coordinator.config.Allowance.Snapshot(event.LineID)
	if err != nil || strings.TrimSpace(snapshot.Values.ValidUntil) != event.Reminder.ValidUntil {
		return "activation_reminder_date_changed"
	}
	location, err := time.LoadLocation(event.Reminder.Timezone)
	if err != nil || calendarDays(coordinator.config.Now().In(location), event.Reminder.ValidUntil) != event.Reminder.DaysBeforeExpiry {
		return "activation_reminder_day_changed"
	}
	return ""
}

func (coordinator *Coordinator) run() {
	defer coordinator.wait.Done()
	ticker := time.NewTicker(coordinator.config.Interval)
	defer ticker.Stop()
	for {
		if err := coordinator.cycle(); err != nil && coordinator.ctx.Err() == nil {
			coordinator.config.Logf("mdd-core: notification coordinator: %s", notificationErrorCode(err))
		}
		select {
		case <-coordinator.ctx.Done():
			return
		case <-coordinator.wake:
		case <-ticker.C:
		}
	}
}

func (coordinator *Coordinator) cycle() error {
	now := coordinator.config.Now().UTC()
	if !coordinator.engineStarted {
		if err := coordinator.config.Engine.Start(); err != nil {
			return err
		}
		coordinator.engineStarted = true
	}
	if err := coordinator.drainSMS(now); err != nil {
		return err
	}
	if err := coordinator.drainCalls(now); err != nil {
		return err
	}
	// SMS and call outboxes contain only post-upgrade real-time facts and must
	// not be blocked by an unrelated System Status baseline problem. Host and
	// reminder producers remain gated until their no-replay baseline is exact.
	coordinator.config.Engine.Wake()
	seeded, err := coordinator.config.Store.Seeded()
	if err != nil {
		return err
	}
	if !seeded {
		if err := coordinator.seed(now); err != nil {
			return err
		}
		seeded, err = coordinator.config.Store.Seeded()
		if err != nil {
			return err
		}
		if !seeded {
			return errors.New("notification producer baseline was not committed")
		}
	}
	if err := coordinator.reconcileHost(now); err != nil {
		return err
	}
	if err := coordinator.produceReminders(now); err != nil {
		return err
	}
	if err := coordinator.cancelStaleReminders(now); err != nil {
		return err
	}
	coordinator.config.Engine.Wake()
	return nil
}

func (coordinator *Coordinator) seed(now time.Time) error {
	status := coordinator.config.SystemStatus.Snapshot(now)
	if status.State != "complete" || status.Stale || status.SampledAt == nil {
		return errors.New("notification baseline awaits complete system status")
	}
	config, err := coordinator.config.Store.Config()
	if err != nil {
		return err
	}
	catalog, err := coordinator.config.Catalog.Snapshot()
	if err != nil {
		return err
	}
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return err
	}
	baseline := []Event{}
	for _, line := range catalog.Lines {
		snapshot, err := coordinator.config.Allowance.Snapshot(line.ID)
		if err != nil {
			return err
		}
		validUntil := strings.TrimSpace(snapshot.Values.ValidUntil)
		if !cardID(strings.TrimSpace(line.CardID)) || !isoDate(validUntil) {
			continue
		}
		days := calendarDays(now.In(location), validUntil)
		if days > 3 {
			continue
		}
		for _, threshold := range []int{3, 2, 1} {
			if days <= threshold {
				baseline = append(baseline, reminderEvent(line, snapshot, config.Timezone, threshold, now))
			}
		}
	}
	active := make([]HostAlertInput, 0, len(status.Alerts))
	for _, alert := range status.Alerts {
		active = append(active, notificationHostAlert(alert))
	}
	return coordinator.config.Store.SeedReceipts(baseline, active)
}

func (coordinator *Coordinator) drainSMS(now time.Time) error {
	sources, err := coordinator.config.SMS.PendingNotificationSources(100)
	if err != nil {
		return err
	}
	for _, source := range sources {
		name, msisdn := coordinator.linePresentation(source.LineID, source.CardID)
		event := Event{SourceID: source.SourceID, Type: EventIncomingSMS,
			LineID: source.LineID, LineName: name, CardID: source.CardID, MSISDN: msisdn, Transport: source.Transport,
			Title: notificationTransportLabel(source.Transport) + " 短信 · " + name,
			Text:  source.Body, Peer: source.Sender, OccurredAt: source.ReceivedAt}
		if _, _, _, err := coordinator.config.Store.Intake(event, now); err != nil {
			return err
		}
		if err := coordinator.config.SMS.AckNotificationSource(source.SourceID); err != nil {
			return err
		}
	}
	return nil
}

func (coordinator *Coordinator) drainCalls(now time.Time) error {
	sources, err := coordinator.config.Calls.PendingNotificationSources(now, 100)
	if err != nil {
		return err
	}
	for _, source := range sources {
		name, msisdn := coordinator.linePresentation(source.LineID, source.CardID)
		event := Event{SourceID: source.SourceID, Type: EventIncomingCall,
			LineID: source.LineID, LineName: name, CardID: source.CardID, MSISDN: msisdn, Transport: source.Transport,
			Title: notificationTransportLabel(source.Transport) + " 呼入 · " + name,
			Peer:  source.Peer, OccurredAt: source.ReceivedAt}
		if _, _, _, err := coordinator.config.Store.Intake(event, now); err != nil {
			return err
		}
		if err := coordinator.config.Calls.AckNotificationSource(source.SourceID); err != nil {
			return err
		}
	}
	return nil
}

func notificationTransportLabel(transport string) string {
	if transport == "cellular" {
		return "蜂窝 Modem"
	}
	return "VoWiFi"
}

func (coordinator *Coordinator) linePresentation(lineID, expectedCardID string) (string, string) {
	line, err := coordinator.config.Catalog.Get(lineID)
	if err != nil || strings.TrimSpace(line.CardID) != strings.TrimSpace(expectedCardID) {
		return lineID, ""
	}
	name := strings.TrimSpace(line.Name)
	if name == "" {
		name = lineID
	}
	return name, line.SIM.MSISDN
}

func (coordinator *Coordinator) reconcileHost(now time.Time) error {
	status := coordinator.config.SystemStatus.Snapshot(now)
	if status.State != "complete" || status.Stale {
		return nil
	}
	alerts := make([]HostAlertInput, 0, len(status.Alerts))
	authoritative := map[string]bool{
		"swap":        status.SwapRateKnown,
		"power":       status.Power.State == systemstatus.SectionAvailable,
		"route":       status.DefaultRoute.State == systemstatus.SectionAvailable,
		"disk":        status.Disk.State == systemstatus.SectionAvailable,
		"temperature": status.Temperatures.State == systemstatus.SectionAvailable,
		"systemd":     status.Systemd.State == systemstatus.SectionAvailable,
	}
	for _, alert := range status.Alerts {
		if authoritative[hostAlertFamily(alert.Code)] {
			alerts = append(alerts, notificationHostAlert(alert))
		}
	}
	_, err := coordinator.config.Store.ReconcileHostAlerts(alerts, authoritative, *status.SampledAt)
	return err
}

func (coordinator *Coordinator) produceReminders(now time.Time) error {
	config, err := coordinator.config.Store.Config()
	if err != nil {
		return err
	}
	location, err := time.LoadLocation(config.Timezone)
	if err != nil {
		return err
	}
	catalog, err := coordinator.config.Catalog.Snapshot()
	if err != nil {
		return err
	}
	for _, line := range catalog.Lines {
		snapshot, err := coordinator.config.Allowance.Snapshot(line.ID)
		if err != nil {
			return err
		}
		validUntil := strings.TrimSpace(snapshot.Values.ValidUntil)
		if !cardID(strings.TrimSpace(line.CardID)) || !isoDate(validUntil) {
			continue
		}
		days := calendarDays(now.In(location), validUntil)
		if !oneOfInt(days, 1, 2, 3) {
			continue
		}
		event := reminderEvent(line, snapshot, config.Timezone, days, now)
		if _, _, _, err := coordinator.config.Store.Intake(event, now); err != nil {
			return err
		}
	}
	return nil
}

func (coordinator *Coordinator) cancelStaleReminders(now time.Time) error {
	deliveries, err := coordinator.config.Store.Deliveries(500)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		if delivery.State != DeliveryPending || delivery.EventType != EventActivationReminder {
			continue
		}
		event, _, err := coordinator.config.Store.EventForDelivery(delivery.DeliveryID)
		if err != nil {
			return err
		}
		if code := coordinator.ValidateNotificationEvent(coordinator.ctx, event); code != "" {
			if _, err := coordinator.config.Store.Cancel(delivery.DeliveryID, code, now); err != nil && !errors.Is(err, ErrConflict) {
				return err
			}
		}
	}
	return nil
}

func reminderEvent(line linecatalog.Line, snapshot allowance.Snapshot, timezone string, days int, now time.Time) Event {
	name := line.Name
	if strings.TrimSpace(name) == "" {
		name = line.ID
	}
	validUntil := strings.TrimSpace(snapshot.Values.ValidUntil)
	return Event{
		SourceID: reminderSourceID(line.ID, line.CardID, validUntil, timezone, days),
		Type:     EventActivationReminder, LineID: line.ID, LineName: name, CardID: line.CardID, MSISDN: line.SIM.MSISDN,
		Title:      "SIM 即将到期 · " + name,
		Text:       fmt.Sprintf("线路 %s 将于 %s 到期，还剩 %d 天。", name, validUntil, days),
		OccurredAt: now,
		Reminder: &ReminderFence{ExpectedCardID: line.CardID, ValidUntil: validUntil,
			Timezone: timezone, DaysBeforeExpiry: days, AllowanceRevision: snapshot.Revision},
	}
}

func reminderSourceID(lineID, cardID, validUntil, timezone string, days int) string {
	return "activation-" + deterministicID("reminder", strings.Join([]string{
		lineID, cardID, validUntil, timezone, fmt.Sprint(days),
	}, "\x00"))
}

func calendarDays(now time.Time, validUntil string) int {
	if !isoDate(validUntil) {
		return 1 << 30
	}
	expiry, _ := time.Parse("2006-01-02", validUntil)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return int(expiry.Sub(today) / (24 * time.Hour))
}

func hostAlertKey(alert systemstatus.Alert) string {
	return deterministicID("host-alert-key", alert.Code+"\x00"+alert.Scope)
}

func hostAlertTitle(alert systemstatus.Alert) string {
	switch alert.Code {
	case "disk_usage_warning", "disk_usage_critical":
		return "MDD 数据盘空间告警"
	case "temperature_warning", "temperature_critical":
		return "MDD 主机温度告警"
	case "systemd_unit_failed":
		return "MDD systemd unit failed"
	default:
		return alert.Code
	}
}

func notificationHostAlert(alert systemstatus.Alert) HostAlertInput {
	return HostAlertInput{
		Key: hostAlertKey(alert), Code: alert.Code, Scope: alert.Scope, Severity: alert.Severity,
		Title: hostAlertTitle(alert), Text: strings.Join([]string{alert.Severity, alert.Code, alert.Scope}, " · "),
	}
}

func notificationErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "notification_coordinator_canceled"
	default:
		return "notification_coordinator_failed"
	}
}
