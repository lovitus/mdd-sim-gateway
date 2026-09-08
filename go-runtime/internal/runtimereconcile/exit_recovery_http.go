package runtimereconcile

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func (reconciler *Reconciler) RecoveryHandler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		fail := func(status int, code string) {
			response.WriteHeader(status)
			_ = json.NewEncoder(response).Encode(map[string]string{"code": code})
		}
		if reconciler.exitRecovery == nil {
			fail(503, "exit_recovery_unavailable")
			return
		}
		lineID := request.PathValue("lineID")
		if _, err := reconciler.catalog.Get(lineID); err != nil {
			if errors.Is(err, linecatalog.ErrNotFound) {
				fail(404, "line_not_found")
			} else {
				fail(503, "catalog_unavailable")
			}
			return
		}
		current, err := reconciler.exitRecovery.Store.ExitRecovery(lineID)
		if err != nil {
			fail(503, "exit_recovery_unavailable")
			return
		}
		if request.Method == http.MethodPost {
			var input struct {
				ExpectedRevision uint64 `json:"expected_revision"`
				FailureID        string `json:"failure_id"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
				fail(400, "invalid_recovery_retry")
				return
			}
			selection := current.Ledger.Selection
			if input.ExpectedRevision != current.Revision || !selection.Pending() || input.FailureID != selection.Request.FailureID {
				fail(409, "exit_recovery_changed")
				return
			}
			if selection.Attempts < 5 {
				fail(409, "exit_recovery_automatic_retry_pending")
				return
			}
			if !reconciler.selectionMu.TryLock() {
				fail(409, "exit_recovery_in_progress")
				return
			}
			defer reconciler.selectionMu.Unlock()
			copy := *selection
			// One explicit attempt after the automatic budget, with the same identity.
			copy.NextAttempt = reconciler.now()
			current.Ledger.Selection = &copy
			current, err = reconciler.exitRecovery.Store.PutExitRecoveryExpected(lineID, current.Ledger, current.Revision)
			if err != nil {
				if errors.Is(err, events.ErrExitRecoveryRevision) {
					fail(409, "exit_recovery_changed")
				} else {
					fail(503, "exit_recovery_unavailable")
				}
				return
			}
			if err := reconciler.runExitSelection(lineID, true); err != nil {
				fail(503, "exit_recovery_unavailable")
				return
			}
			current, err = reconciler.exitRecovery.Store.ExitRecovery(lineID)
			if err != nil {
				fail(503, "exit_recovery_unavailable")
				return
			}
			reconciler.Wake()
		} else if request.Method != http.MethodGet {
			fail(405, "method_not_allowed")
			return
		}
		ledger := current.Ledger
		result := map[string]any{"schema_version": 1, "line_id": lineID, "revision": current.Revision, "failures": ledger.Failures,
			"decision": ledger.LastDecision, "node": ledger.Node, "strikes": ledger.Strikes, "tried_count": len(ledger.Tried),
			"held_for_peer": ledger.HeldForPeer, "given_up": ledger.GivenUp, "exhausted": ledger.Exhausted, "retry_after": ledger.RetryAfter}
		if selection := ledger.Selection; selection != nil {
			result["selection"] = map[string]any{"state": selection.State, "attempts": selection.Attempts,
				"next_attempt": selection.NextAttempt, "code": selection.Code, "from_node": selection.Request.FromNode, "to_node": selection.Request.ToNode,
				"failure_id": selection.Request.FailureID}
		}
		_ = json.NewEncoder(response).Encode(result)
	})
}
