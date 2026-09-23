package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func request(f *fixture, control bool, path string, value object, authorized bool) *httptest.ResponseRecorder {
	b, _ := json.Marshal(value)
	r := httptest.NewRequest("POST", path, bytes.NewReader(b))
	if authorized {
		token := f.token
		if control {
			token = f.control
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-MDD-CSRF-Token", f.csrf)
	}
	w := httptest.NewRecorder()
	if control {
		f.controlHTTP(w, r)
	} else {
		f.dataHTTP(w, r)
	}
	return w
}

func TestControlIsSeparateAndRequiresItsOwnToken(t *testing.T) {
	f := newFixture()
	if w := request(f, true, "/case", object{"mode": "vowifi", "scenario": "sms"}, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if f.mode != "" {
		t.Fatal("unauthenticated control changed state")
	}
	if w := request(f, false, "/case", object{"mode": "vowifi", "scenario": "sms"}, true); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if f.mode != "" {
		t.Fatal("data-plane client reached control")
	}
	if w := request(f, true, "/case", object{"mode": "vowifi", "scenario": "sms"}, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestReceiptMatchesOriginalPayloadWithoutSending(t *testing.T) {
	for _, mode := range []string{"cellular", "vowifi"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture()
			f.mode = mode
			f.scenario = "sms"
			path := "/v1/lines/" + lineID + "/" + mode + "/messages"
			if mode == "vowifi" {
				path += "/send"
			}
			b := object{"operation_id": "original-a", "message_id": "original-a", "expected_card_id": cardID, "recipient": "+15550100999", "body": "fixture-A"}
			if w := request(f, false, path, b, true); w.Code != 502 {
				t.Fatal(w.Code)
			}
			if mode == "vowifi" {
				path = "/v1/lines/" + lineID + "/vowifi/messages/receipt"
			} else {
				b["reconcile_only"] = true
			}
			if w := request(f, false, path, b, true); w.Code != 200 {
				t.Fatal(w.Code)
			}
			if f.counts["sends"] != 1 || f.counts["receipts"] != 1 {
				t.Fatal(f.counts)
			}
			b["body"] = "changed"
			if w := request(f, false, path, b, true); w.Code != 409 {
				t.Fatal(w.Code)
			}
			if f.counts["sends"] != 1 || f.counts["receipts"] != 1 {
				t.Fatal("mismatch changed counters")
			}
		})
	}
}

func TestRecoveryCannotCreateOrEndOriginalOperation(t *testing.T) {
	f := newFixture()
	f.mode = "cellular"
	f.scenario = "submitted"
	f.lease = object{"call_id": "original-call", "operation_id": "original-op", "recovery_key": "original-key"}
	f.terminal = true
	b := object{"call_id": "original-call", "operation_id": "original-op", "recovery_key": "wrong", "action": "status"}
	path := "/v1/lines/" + lineID + "/cellular/calls/recovery"
	if w := request(f, false, path, b, true); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if f.counts["statuses"] != 0 {
		t.Fatal("wrong identity accepted")
	}
	b["recovery_key"] = "original-key"
	if w := request(f, false, path, b, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if f.counts["starts"] != 0 || f.counts["ends"] != 0 || f.counts["statuses"] != 1 {
		t.Fatal(f.counts)
	}
}

func TestReauthenticatedControlRequiresOriginalCapabilityAndEndIdentity(t *testing.T) {
	f := newFixture()
	f.mode = "cellular"
	f.scenario = "control"
	f.counts["guard_attempts"] = 1
	f.lease = object{"call_id": "original-call", "operation_id": "original-op", "recovery_key": "original-key"}
	oldToken, oldCSRF := f.token, f.csrf
	if w := request(f, false, "/api/auth/login", object{"username": f.user, "password": f.password}, false); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if f.token == oldToken || f.csrf == oldCSRF {
		t.Fatal("login must replace both credentials")
	}
	path := "/v1/lines/" + lineID + "/cellular/calls/recovery"
	b := object{"call_id": "original-call", "operation_id": "original-op", "recovery_key": "original-key", "action": "end", "end_operation_id": "original-end"}
	raw, _ := json.Marshal(b)
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+oldToken)
	r.Header.Set("X-MDD-CSRF-Token", oldCSRF)
	w := httptest.NewRecorder()
	f.dataHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("revoked session reached control", w.Code)
	}
	b["recovery_key"] = "unrelated-key"
	if w = request(f, false, path, b, true); w.Code != 409 {
		t.Fatal("new session alone took control", w.Code)
	}
	b["recovery_key"] = "original-key"
	if w = request(f, false, path, b, true); w.Code != 409 {
		t.Fatal("unprompted end admitted", w.Code)
	}
	if f.counts["ends"] != 0 {
		t.Fatal("end side effect before admission")
	}
	if w = request(f, true, "/permit-end", object{}, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request(f, false, path, b, true); w.Code != 502 {
		t.Fatal(w.Code)
	}
	b["action"] = "status"
	if w = request(f, false, path, b, true); w.Code != 503 {
		t.Fatal("terminal gate was bypassed", w.Code)
	}
	b["action"] = "end"
	if w = request(f, true, "/permit-end", object{}, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	b["end_operation_id"] = "different-end"
	if w = request(f, false, path, b, true); w.Code != 409 {
		t.Fatal("changed end ID accepted", w.Code)
	}
	b["end_operation_id"] = "original-end"
	if w = request(f, false, path, b, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if f.counts["end_requests"] != 2 || f.counts["ends"] != 1 || f.endReceiptHeld {
		t.Fatal("repeat end was not receipt-only", f.counts)
	}
}
