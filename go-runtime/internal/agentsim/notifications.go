package agentsim

import (
	"context"
	"errors"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

// ExecuteEUICCNotification performs one read-only ES10b ListNotification on
// the exact live secure element. It neither sends nor removes notifications.
func (manager *Manager) ExecuteEUICCNotification(ctx context.Context,
	request agentlink.EUICCNotificationRequest) agentlink.EUICCNotificationResponse {
	result := agentlink.EUICCNotificationResponse{
		OperationID: request.OperationID, SessionGeneration: request.SessionGeneration, EID: request.EID,
	}
	if err := request.Validate(); err != nil {
		result.Failure = failure("rejected", "invalid_euicc_notification_request", false)
		return result
	}
	manager.mu.RLock()
	current := manager.sessions[request.SessionGeneration]
	manager.mu.RUnlock()
	if current == nil || !current.active.Load() {
		result.Failure = failure("not_ready", "card_session_replaced", true)
		return result
	}
	current.operation.Lock()
	defer current.operation.Unlock()
	if !current.active.Load() {
		result.Failure = failure("not_ready", "card_session_replaced", true)
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Failure = notificationContextFailure(err)
		return result
	}
	if err := current.card.BeginTransaction(); err != nil {
		result.Failure = failure("transport", "pcsc_transaction_failed", true)
		return result
	}
	release := func() bool {
		if err := current.card.EndTransaction(); err != nil {
			result.Failure = failure("transport", "pcsc_transaction_release_failed", true)
			return false
		}
		return true
	}
	target, unique := findSecureElement(current.secureElements, request.EID)
	if !unique || target.fact == nil || !target.fact.NotificationInventory {
		if !release() {
			return result
		}
		result.Failure = failure("conflict", "euicc_identity_mismatch", false)
		return result
	}
	live, inspectErr := inspectEUICCWithAID(ctx, current.card, target.aid)
	if inspectErr != nil || live == nil || live.EID != request.EID || !live.NotificationInventory {
		if !release() {
			return result
		}
		if err := ctx.Err(); err != nil {
			result.Failure = notificationContextFailure(err)
		} else {
			result.Failure = failure("not_ready", "euicc_inventory_unavailable", true)
		}
		return result
	}
	entries, listErr := manager.listNotifications(ctx, current.card, target.aid)
	if !release() {
		return result
	}
	if listErr != nil {
		if err := ctx.Err(); err != nil {
			result.Failure = notificationContextFailure(err)
		} else {
			result.Failure = failure("transport", "euicc_notification_inventory_failed", true)
		}
		return result
	}
	result.Entries = entries
	return result
}

func notificationContextFailure(err error) *agentlink.RemoteError {
	if errors.Is(err, context.DeadlineExceeded) {
		return failure("transport", "euicc_notification_inventory_timeout", true)
	}
	return failure("transport", "operation_canceled", true)
}
