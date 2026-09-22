// Command android-process-fixture is an external Android QA peer, never a Core
// mode or a release component. It has no Agent, Provider or carrier connection.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

type object = map[string]any

const lineID = "process-fixture-line"
const cardID = "8944100000000000001"
const sessionID = "process-fixture-session"

type fixture struct {
	mu                                   sync.Mutex
	changed                              chan struct{}
	mode, scenario, failure              string
	user, password, token, csrf, control string
	counts                               map[string]int
	lease                                object
	messages                             map[string]object
	terminal                             bool
	sequence                             int
	canaryAt                             time.Time
	endOperation                         string
	endPermits                           int
	endReceiptHeld, guardScheduled       bool
}

func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func newFixture() *fixture {
	return &fixture{changed: make(chan struct{}), user: "fixture-owner", password: secret(), token: secret(), csrf: secret(), control: secret(), counts: map[string]int{}, messages: map[string]object{}}
}
func (f *fixture) notifyLocked() { close(f.changed); f.changed = make(chan struct{}) }
func (f *fixture) increment(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[key]++
	if key == "canary" {
		f.canaryAt = time.Now()
	}
	f.notifyLocked()
}
func (f *fixture) fail(code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failure == "" {
		f.failure = code
	}
	f.notifyLocked()
}
func (f *fixture) stateLocked() object {
	counts := map[string]int{}
	for k, v := range f.counts {
		counts[k] = v
	}
	age := int64(-1)
	if !f.canaryAt.IsZero() {
		age = time.Since(f.canaryAt).Milliseconds()
	}
	return object{"counts": counts, "failure": f.failure, "mode": f.mode, "scenario": f.scenario, "terminal": f.terminal, "canary_age_ms": age, "end_receipt_held": f.endReceiptHeld, "simulated_guard_unknown": f.counts["guard_attempts"] > 0}
}
func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}
func body(r *http.Request) (object, error) {
	defer r.Body.Close()
	d := json.NewDecoder(io.LimitReader(r.Body, 65537))
	var value object
	if err := d.Decode(&value); err != nil || value == nil {
		return nil, errors.New("invalid body")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing body")
	}
	return value, nil
}
func str(b object, key string) string     { s, _ := b[key].(string); return s }
func number(b object, key string) float64 { n, _ := b[key].(float64); return n }
func (f *fixture) mismatch(w http.ResponseWriter, code string) {
	f.fail(code)
	reply(w, 409, object{"code": "fixture_contract_mismatch"})
}

// Control has its own loopback listener. Only the TLS data port is reversed to
// Android; the control token never enters App config or device command arguments.
func (f *fixture) controlHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.control {
		reply(w, 401, nil)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/state" {
		f.mu.Lock()
		defer f.mu.Unlock()
		reply(w, 200, f.stateLocked())
		return
	}
	if r.Method != "POST" {
		reply(w, 404, nil)
		return
	}
	b, err := body(r)
	if err != nil {
		reply(w, 400, nil)
		return
	}
	switch r.URL.Path {
	case "/case":
		mode, scenario := str(b, "mode"), str(b, "scenario")
		if (mode != "vowifi" && mode != "cellular") || (scenario != "preparing" && scenario != "submitted" && scenario != "sms" && scenario != "control") {
			reply(w, 400, nil)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.mode != "" {
			reply(w, 409, nil)
			return
		}
		f.mode, f.scenario, f.failure = mode, scenario, ""
		f.counts = map[string]int{}
		f.lease = nil
		f.messages = map[string]object{}
		f.terminal = false
		f.sequence = 0
		f.notifyLocked()
		reply(w, 200, f.stateLocked())
	case "/terminal":
		f.mu.Lock()
		defer f.mu.Unlock()
		f.terminal = true
		f.notifyLocked()
		reply(w, 200, f.stateLocked())
	case "/permit-end":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.scenario != "control" || f.counts["guard_attempts"] != 1 || f.endPermits != 0 || f.counts["end_requests"] >= 2 {
			reply(w, 409, nil)
			return
		}
		f.endPermits = 1
		reply(w, 200, f.stateLocked())
	case "/wait":
		key, target := str(b, "event"), int(number(b, "count"))
		allowed := map[string]bool{"observers": true, "canary": true, "starts": true, "deletes": true, "statuses": true, "sends": true, "receipts": true, "logins": true, "guard_attempts": true, "end_requests": true, "status_unavailable": true, "new_login_statuses": true}
		if !allowed[key] || target < 1 || target > 10 {
			reply(w, 400, nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		for {
			f.mu.Lock()
			state, wake := f.stateLocked(), f.changed
			done := f.counts[key] >= target || f.failure != ""
			f.mu.Unlock()
			if done {
				reply(w, 200, state)
				return
			}
			select {
			case <-ctx.Done():
				reply(w, 408, state)
				return
			case <-wake:
			}
		}
	default:
		reply(w, 404, nil)
	}
}
func (f *fixture) line() object {
	f.mu.Lock()
	mode := f.mode
	f.mu.Unlock()
	return object{"id": lineID, "card_id": cardID, "name": "External process fixture", "number": "+15550100000", "enabled": true, "ims": "ready", "operations": object{mode + "_call": object{"ready": true}, mode + "_sms": object{"ready": true}}}
}
func (f *fixture) dataHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/api/auth/login" && r.Method == "POST" {
		b, err := body(r)
		if err != nil || str(b, "username") != f.user || str(b, "password") != f.password {
			reply(w, 401, nil)
			return
		}
		f.mu.Lock()
		f.token, f.csrf = secret(), secret()
		token, csrf := f.token, f.csrf
		f.counts["logins"]++
		f.notifyLocked()
		f.mu.Unlock()
		reply(w, 200, object{"token": token, "csrf": csrf})
		return
	}
	f.mu.Lock()
	token, csrf := f.token, f.csrf
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+token {
		reply(w, 401, nil)
		return
	}
	if r.Method != "GET" && r.Header.Get("X-MDD-CSRF-Token") != csrf {
		reply(w, 403, nil)
		return
	}
	switch path {
	case "/api/auth/status":
		reply(w, 200, object{"authenticated": true, "username": f.user, "token": token, "csrf": csrf})
		return
	case "/v1/messages":
		reply(w, 200, object{"messages": []any{}, "cursor": strings.Repeat("a", 32) + ":0", "initial": r.URL.Query().Get("after") == "", "more": false})
		return
	case "/v1/mobile/lines/" + lineID:
		reply(w, 200, f.line())
		return
	case "/v1/mobile/ws":
		f.observer(w, r)
		return
	}
	f.mu.Lock()
	mode, scenario := f.mode, f.scenario
	f.mu.Unlock()
	leases, media := "/v1/media/leases", "/api/browser-media/"+sessionID+"/ws"
	if mode == "cellular" {
		leases, media = "/v1/cellular/media/leases", "/api/cellular-browser-media/"+sessionID+"/ws"
	}
	if path == media {
		f.media(w, r)
		return
	}
	b, err := body(r)
	if err != nil {
		f.mismatch(w, "invalid_data_body")
		return
	}
	if path == leases {
		if r.Method == "DELETE" {
			if str(b, "session_id") != sessionID {
				f.mismatch(w, "delete_identity")
				return
			}
			f.increment("deletes")
			reply(w, 204, nil)
			return
		}
		if r.Method != "POST" || str(b, "line_id") != lineID || str(b, "operation_id") == "" || str(b, "call_id") == "" || str(b, "recovery_key") == "" || (mode == "cellular" && str(b, "expected_card_id") != cardID) {
			f.mismatch(w, "lease_identity")
			return
		}
		f.mu.Lock()
		f.lease = b
		f.counts["leases"]++
		duplicate := f.counts["leases"] != 1
		f.notifyLocked()
		f.mu.Unlock()
		if duplicate {
			f.mismatch(w, "duplicate_lease")
			return
		}
		reply(w, 201, object{"session_id": sessionID, "ws_path": media, "expires_at": "2099-01-01T00:00:00Z"})
		return
	}
	prefix := "/v1/lines/" + lineID + "/" + mode + "/calls/"
	if path == prefix+"start" && r.Method == "POST" {
		f.increment("start_requests")
		f.mu.Lock()
		lease := f.lease
		f.mu.Unlock()
		valid := str(b, "operation_id") == str(lease, "operation_id") && str(b, "expected_card_id") == cardID && str(b, "callee") == "+15550100999"
		if mode == "cellular" {
			valid = valid && str(b, "session_id") == sessionID
		} else {
			valid = valid && str(b, "media_session_id") == sessionID && str(b, "call_id") == str(lease, "call_id")
		}
		if !valid || lease == nil || (scenario != "submitted" && scenario != "control") {
			f.mismatch(w, "start_identity_or_phase")
			return
		}
		f.increment("starts")
		if scenario == "control" {
			if mode == "cellular" {
				reply(w, 200, object{"code": "cellular_call_started", "session_id": sessionID, "call_id": lease["call_id"], "state": "dialing"})
			} else {
				reply(w, 200, object{"operation_id": lease["operation_id"], "accepted": true, "code": "active", "call_id": lease["call_id"]})
			}
			return
		}
		// Acceptance and response delivery are deliberately distinct. This request
		// stays unacknowledged until its original App process/socket disappears.
		<-r.Context().Done()
		return
	}
	if path == prefix+"recovery" && r.Method == "POST" {
		f.mu.Lock()
		lease, terminal := f.lease, f.terminal
		f.mu.Unlock()
		if lease == nil || str(b, "call_id") != str(lease, "call_id") || str(b, "operation_id") != str(lease, "operation_id") || str(b, "recovery_key") != str(lease, "recovery_key") {
			f.mismatch(w, "recovery_identity")
			return
		}
		if str(b, "action") == "end" && scenario == "control" {
			f.controlEnd(w, b, lease)
			return
		}
		if str(b, "action") != "status" {
			f.increment("ends")
			f.mismatch(w, "unsolicited_end")
			return
		}
		f.increment("statuses")
		f.mu.Lock()
		held := f.endReceiptHeld
		newLogin := f.counts["logins"] > 1
		guardUnknown := f.counts["guard_attempts"] > 0
		f.mu.Unlock()
		if newLogin {
			f.increment("new_login_statuses")
		}
		if held {
			f.increment("status_unavailable")
			reply(w, 503, object{"code": "fixture_receipt_unavailable"})
			return
		}
		if terminal {
			terminalReply(w, lease)
		} else {
			state, reason := "active", ""
			if guardUnknown {
				state, reason = "hangup_unconfirmed", "fixture_guard_outcome_unknown"
			}
			reply(w, 200, object{"state": state, "reason": reason, "terminal_confirmed": false})
		}
		return
	}
	sendPath := "/v1/lines/" + lineID + "/" + mode + "/messages"
	if mode == "vowifi" {
		sendPath += "/send"
	}
	if r.Method == "POST" && (path == sendPath || path == "/v1/lines/"+lineID+"/vowifi/messages/receipt") {
		f.sms(w, b, path != sendPath || b["reconcile_only"] == true, mode)
		return
	}
	f.mismatch(w, "unexpected_route")
}
func terminalReply(w http.ResponseWriter, lease object) {
	reply(w, 200, object{"state": "terminal", "call_id": lease["call_id"], "operation_id": lease["operation_id"], "session_id": sessionID, "terminal_confirmed": true, "terminal_at": "2026-09-23T00:00:00Z"})
}
func (f *fixture) controlEnd(w http.ResponseWriter, b, lease object) {
	f.mu.Lock()
	id := str(b, "end_operation_id")
	valid := id != "" && id != str(lease, "operation_id") && f.endPermits == 1 && (f.endOperation == "" || id == f.endOperation)
	if !valid {
		f.mu.Unlock()
		f.mismatch(w, "end_not_permitted_or_identity_changed")
		return
	}
	f.endPermits = 0
	f.counts["end_requests"]++
	first := f.endOperation == ""
	if first {
		f.endOperation = id
		f.counts["ends"]++
		f.terminal = true
		f.endReceiptHeld = true
	} else {
		f.endReceiptHeld = false
	}
	f.notifyLocked()
	f.mu.Unlock()
	if first {
		reply(w, 502, object{"code": "fixture_end_ack_lost"})
		return
	}
	terminalReply(w, lease)
}
func (f *fixture) guardUnknownAfterMediaLoss() {
	f.mu.Lock()
	if f.scenario != "control" || f.counts["starts"] != 1 || f.guardScheduled {
		f.mu.Unlock()
		return
	}
	f.guardScheduled = true
	f.mu.Unlock()
	// Explicit failed-guard fixture, not a disabled or extended production guard.
	time.AfterFunc(10*time.Second, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.terminal {
			f.counts["guard_attempts"]++
			f.notifyLocked()
		}
	})
}
func (f *fixture) sms(w http.ResponseWriter, b object, query bool, mode string) {
	if !query {
		f.increment("send_requests")
	}
	id := str(b, "operation_id")
	if id == "" || id != str(b, "message_id") || str(b, "expected_card_id") != cardID || str(b, "recipient") != "+15550100999" {
		f.mismatch(w, "sms_identity")
		return
	}
	f.mu.Lock()
	prior := f.messages[id]
	if query {
		valid := prior != nil && str(prior, "body") == str(b, "body") && str(prior, "recipient") == str(b, "recipient")
		if valid {
			f.counts["receipts"]++
			f.notifyLocked()
		}
		f.mu.Unlock()
		if !valid {
			f.mismatch(w, "sms_receipt_identity")
			return
		}
	} else {
		f.counts["sends"]++
		count := f.counts["sends"]
		valid := prior == nil && ((count == 1 && str(b, "body") == "fixture-A") || (count == 2 && str(b, "body") == "fixture-B"))
		f.messages[id] = b
		f.notifyLocked()
		f.mu.Unlock()
		if !valid {
			f.mismatch(w, "sms_duplicate_or_body")
			return
		}
		if count == 1 {
			reply(w, 502, object{"code": "submission_uncertain"})
			return
		}
	}
	if mode == "cellular" {
		reply(w, 200, object{"code": "cellular_sms_submitted", "message_id": id, "references": []int{7}})
	} else {
		reply(w, 200, object{"accepted": true, "code": "sent", "operation_id": id, "message_id": id})
	}
}
func (f *fixture) observer(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	f.increment("observers")
	f.mu.Lock()
	f.sequence++
	sequence := f.sequence
	f.mu.Unlock()
	b, _ := json.Marshal(object{"type": "mobile.snapshot", "schema_version": 1, "sequence": sequence, "data": object{"lines": []any{f.line()}, "incoming_lines": []any{}, "cellular_calls": []any{}, "messages": []any{}}})
	if ws.Write(r.Context(), websocket.MessageText, b) != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				f.mu.Lock()
				f.sequence++
				seq := f.sequence
				f.mu.Unlock()
				b, _ := json.Marshal(object{"type": "mobile.heartbeat", "schema_version": 1, "sequence": seq, "at": time.Now().UTC()})
				if ws.Write(ctx, websocket.MessageText, b) != nil {
					return
				}
			}
		}
	}()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
	}
}
func (f *fixture) media(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	defer f.guardUnknownAfterMediaLoss()
	f.increment("media")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	send := func(b object) error { raw, _ := json.Marshal(b); return ws.Write(ctx, websocket.MessageText, raw) }
	started, ready := false, false
	for {
		kind, raw, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if kind == websocket.MessageBinary {
			if len(raw) != 320 {
				f.fail("pcm_size")
				return
			}
			f.increment("pcm")
			continue
		}
		var b object
		if json.Unmarshal(raw, &b) != nil {
			f.fail("media_json")
			return
		}
		switch str(b, "type") {
		case "browser.media.hello":
			f.mu.Lock()
			lease := f.lease
			f.mu.Unlock()
			if started || lease == nil || str(b, "session_id") != sessionID || str(b, "ticket") != str(lease, "call_id") {
				f.fail("media_identity")
				return
			}
			started = true
			if send(object{"type": "browser.media.claimed", "version": 1, "challenge": "fixture-challenge", "resume_ticket": "fixture-resume", "connection_epoch": 1}) != nil {
				return
			}
			if send(object{"type": "browser.media.started", "version": 1, "purpose": "canary"}) != nil {
				return
			}
			go func() {
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				frame := tone()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if ws.Write(ctx, websocket.MessageBinary, frame) != nil {
							return
						}
					}
				}
			}()
		case "browser.media.evidence":
			if str(b, "challenge") != "fixture-challenge" {
				f.fail("media_challenge")
				return
			}
			f.mu.Lock()
			pcm, scenario := f.counts["pcm"], f.scenario
			f.mu.Unlock()
			if !ready && number(b, "capture_callbacks") > 0 && number(b, "played_frames") > 1 && pcm > 1 {
				ready = true
				f.increment("canary")
				if scenario != "preparing" {
					if send(object{"type": "browser.media.ready", "version": 1, "ready": true}) != nil {
						return
					}
				}
			}
		default:
			f.fail("unexpected_media_command")
			return
		}
	}
}
func tone() []byte {
	b := make([]byte, 320)
	for i := 0; i < 160; i++ {
		v := int16(200 * math.Sin(2*math.Pi*440*float64(i)/8000))
		b[2*i], b[2*i+1] = byte(v), byte(v>>8)
	}
	return b
}
func certificate() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "MDD external QA fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	sum := sha256.Sum256(der)
	return cert, hex.EncodeToString(sum[:]), err
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 580*time.Second)
	defer cancel()
	cert, pin, err := certificate()
	if err != nil {
		log.Fatal("fixture TLS setup failed")
	}
	data, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		log.Fatal("fixture data listen failed")
	}
	defer data.Close()
	control, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		log.Fatal("fixture control listen failed")
	}
	defer control.Close()
	f := newFixture()
	makeServer := func(h http.HandlerFunc) *http.Server {
		return &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
	}
	ds, cs := makeServer(f.dataHTTP), makeServer(f.controlHTTP)
	defer ds.Close()
	defer cs.Close()
	go func() {
		_ = ds.Serve(tls.NewListener(data, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}))
	}()
	go func() { _ = cs.Serve(control) }()
	// A single inherited stdout pipe carries ephemeral bootstrap credentials to
	// the owning controller, which must never log or upload this record.
	_ = json.NewEncoder(os.Stdout).Encode(object{"data_port": data.Addr().(*net.TCPAddr).Port, "control_port": control.Addr().(*net.TCPAddr).Port, "control_token": f.control, "username": f.user, "password": f.password, "pin": pin})
	<-ctx.Done()
}
