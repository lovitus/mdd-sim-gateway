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
			if result.Removed {
				return true
			}
			result.Failure = failure("transport", "pcsc_transaction_release_failed", true)
			return false
		}
		return true
	}
	target, unique := findSecureElement(current.secureElements, request.EID)
	requiredCapability := target != nil && target.fact != nil && target.fact.NotificationInventory
	if request.Action == agentlink.EUICCNotificationDeliver {
		requiredCapability = target != nil && target.fact != nil && target.fact.NotificationDelivery
	} else if request.Action == agentlink.EUICCNotificationRemove {
		requiredCapability = target != nil && target.fact != nil && target.fact.NotificationRemoval
	}
	if !unique || !requiredCapability {
		if !release() {
			return result
		}
		result.Failure = failure("conflict", "euicc_identity_mismatch", false)
		return result
	}
	live, inspectErr := inspectEUICCWithAID(ctx, current.card, target.aid)
	liveCapable := live != nil && live.NotificationInventory
	if request.Action == agentlink.EUICCNotificationDeliver {
		liveCapable = live != nil && live.NotificationDelivery
	} else if request.Action == agentlink.EUICCNotificationRemove {
		liveCapable = live != nil && live.NotificationRemoval
	}
	if inspectErr != nil || live == nil || live.EID != request.EID || !liveCapable {
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
	if request.Action == agentlink.EUICCNotificationDeliver {
		acknowledged, removed, deliverErr := manager.deliverNotification(ctx, current.card, target.aid, *request.Expected)
		result.Acknowledged, result.Removed = acknowledged, removed
		if !release() {
			return result
		}
		if deliverErr != nil {
			result.Failure = classifyNotificationDeliveryError(ctx, deliverErr, acknowledged)
		}
		return result
	}
	if request.Action == agentlink.EUICCNotificationRemove {
		removed, removeErr := manager.removeNotification(ctx, current.card, target.aid, *request.Expected)
		result.Removed = removed
		if !release() {
			return result
		}
		if removeErr != nil {
			result.Failure = classifyNotificationRemovalError(ctx, removeErr)
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

func classifyNotificationRemovalError(ctx context.Context, err error) *agentlink.RemoteError {
	if errors.Is(err, errNotificationChanged) {
		return failure("conflict", "euicc_notification_changed", false)
	}
	if errors.Is(err, errNotificationNotFound) {
		return failure("conflict", "euicc_notification_not_found", false)
	}
	if ctx.Err() != nil {
		return failure("transport", "euicc_notification_removal_outcome_unknown", false)
	}
	return failure("transport", "euicc_notification_removal_failed", false)
}

func classifyNotificationDeliveryError(ctx context.Context, err error, acknowledged bool) *agentlink.RemoteError {
	if acknowledged {
		return failure("failed", "euicc_notification_acknowledged_not_removed", false)
	}
	if errors.Is(err, errNotificationChanged) {
		return failure("conflict", "euicc_notification_changed", false)
	}
	if errors.Is(err, errNotificationNotFound) {
		return failure("conflict", "euicc_notification_not_found", false)
	}
	if errors.Is(err, errNotificationAddress) {
		return failure("rejected", "euicc_notification_receiver_invalid", false)
	}
	if errors.Is(err, errNotificationReceiverRejected) {
		return failure("failed", "euicc_notification_receiver_rejected", false)
	}
	if errors.Is(err, errNotificationOutcomeUnknown) || ctx.Err() != nil {
		return failure("transport", "euicc_notification_delivery_outcome_unknown", false)
	}
	return failure("transport", "euicc_notification_retrieve_failed", true)
}

func notificationContextFailure(err error) *agentlink.RemoteError {
	if errors.Is(err, context.DeadlineExceeded) {
		return failure("transport", "euicc_notification_inventory_timeout", true)
	}
	return failure("transport", "operation_canceled", true)
}
