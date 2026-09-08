package runtimereconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
)

func TestRecoveryHTTPManualRetryKeepsIdentityAndIsOneShot(t *testing.T) {
	calls := 0
	var seen egressconfig.RecoveryRequest
	apply := selectionApplyFunc(func(_ context.Context, request egressconfig.RecoveryRequest) (egressconfig.ApplyResult, error) {
		calls++
		seen = request
		return egressconfig.ApplyResult{}, context.DeadlineExceeded
	})
	r, line := pendingSelectionFixture(t, apply)
	stored, err := r.exitRecovery.Store.ExitRecovery(line.ID)
	if err != nil {
		t.Fatal(err)
	}
	original := stored.Ledger.Selection.Request
	stored.Ledger.Selection.Attempts = 5
	stored.Ledger.Selection.State = "unknown"
	stored, err = r.exitRecovery.Store.PutExitRecoveryExpected(line.ID, stored.Ledger, stored.Revision)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(method string, revision uint64) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"expected_revision": revision, "failure_id": original.FailureID})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, "/v1/lines/line-1/recovery", bytes.NewReader(payload))
		request.SetPathValue("lineID", line.ID)
		response := httptest.NewRecorder()
		r.RecoveryHandler().ServeHTTP(response, request)
		return response
	}
	get := serve(http.MethodGet, 0)
	if get.Code != 200 || strings.Contains(get.Body.String(), "card_id") || strings.Contains(get.Body.String(), "expected_generation") {
		t.Fatal(get.Code, get.Body.String())
	}
	if wrong := serve(http.MethodPost, stored.Revision-1); wrong.Code != 409 || calls != 0 {
		t.Fatal(wrong.Code, calls)
	}
	posted := serve(http.MethodPost, stored.Revision)
	if posted.Code != 200 || calls != 1 || seen != original {
		t.Fatal(posted.Code, calls, seen)
	}
	if err := r.executeExitSelection(line.ID); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("manual retry restarted automatic retry loop")
	}
	after, err := r.exitRecovery.Store.ExitRecovery(line.ID)
	if err != nil || after.Ledger.Selection.Attempts != 6 || after.Ledger.Selection.State != "unknown" {
		t.Fatal(after, err)
	}
}
