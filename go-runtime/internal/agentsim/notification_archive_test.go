package agentsim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damonto/euicc-go/bertlv"
	"github.com/damonto/euicc-go/lpa"
	sgp22 "github.com/damonto/euicc-go/v2"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
)

// The card response and receiver are fixtures, not an operator-signed receipt.
func TestArchivedNotificationRoundTripRetainsPayloadAndNeverRemoves(t *testing.T) {
	for _, outcome := range []string{"acknowledged", "rejected", "disconnected"} {
		t.Run(outcome, func(t *testing.T) {
			var sends atomic.Int32
			bodies := make(chan []byte, 2)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				if r.Method != "POST" || r.URL.Path != "/gsma/rsp2/es9plus/handleNotification" {
					t.Error("wrong notification request")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				bodies <- body
				if outcome == "disconnected" {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					connection.Close()
					return
				}
				if outcome == "rejected" {
					w.WriteHeader(500)
					return
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			expected := agentlink.EUICCNotificationEntry{SequenceNumber: 17, Event: "delete", ICCID: "8944000000000000001", Address: "notify.example.com"}
			metadata := notificationMetadata(t, 17, 3, expected.ICCID, expected.Address)
			pending := bertlv.NewChildren(bertlv.ContextSpecific.Constructed(55), bertlv.NewChildren(bertlv.ContextSpecific.Constructed(39), metadata))
			card := euiccCard(t, emptyProfileResponse())
			base := card.handler
			reads, removes := 0, 0
			card.handler = func(command []byte) ([]byte, error) {
				if len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x2B}) {
					reads++
					reply := bertlv.NewChildren(bertlv.ContextSpecific.Constructed(43), bertlv.NewChildren(bertlv.ContextSpecific.Constructed(0), pending)).Bytes()
					return append(reply, 0x90, 0x00), nil
				}
				if len(command) >= 5 && command[1] == 0xE2 && bytes.Contains(command, []byte{0xBF, 0x30}) {
					removes++
					return nil, errors.New("must not remove archived notification")
				}
				return base(command)
			}
			client, err := lpa.New(&lpa.Options{Channel: &euiccCardChannel{ctx: context.Background(), card: card}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			transport := server.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			defer transport.CloseIdleConnections()
			client.HTTP.Client = &http.Client{Transport: transport, Timeout: 5 * time.Second}
			request := agentlink.EUICCNotificationRequest{OperationID: "archive-test", SessionGeneration: "fixture", EID: testEID, Action: agentlink.EUICCNotificationArchive, Expected: &expected}
			payload, ack, err := archivedEUICCNotificationWithClient(context.Background(), client, request)
			if err != nil || ack || reads != 1 || removes != 0 || sends.Load() != 0 || !bytes.Equal(payload, pending.Bytes()) {
				t.Fatalf("archive wrote or changed payload: %v", err)
			}
			path := filepath.Join(t.TempDir(), "events.db")
			store, err := events.OpenBoltStore(path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := store.SaveEUICCNotification(testEID, expected, payload)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			store, err = events.OpenBoltStore(path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			archive, fresh, err := store.BeginEUICCReplay(testEID, 17, "attempt-1", archive.SHA256)
			if err != nil || !fresh {
				t.Fatal(err)
			}
			request.Action = agentlink.EUICCNotificationReplay
			request.Payload = archive.Payload
			_, ack, err = archivedEUICCNotificationWithClient(context.Background(), client, request)
			state := "unknown"
			switch outcome {
			case "acknowledged":
				if err != nil || !ack {
					t.Fatal("ACK lost", err)
				}
				state = "acknowledged"
			case "rejected":
				if ack || !errors.Is(err, errNotificationReceiverRejected) {
					t.Fatal("rejection misreported", err)
				}
				state = "failed"
			case "disconnected":
				if ack || !errors.Is(err, errNotificationOutcomeUnknown) {
					t.Fatal("disconnect misreported", err)
				}
			}
			if sends.Load() != 1 || reads != 1 || removes != 0 {
				t.Fatalf("unexpected sends=%d reads=%d removes=%d", sends.Load(), reads, removes)
			}
			wantBody, _ := json.Marshal(&sgp22.ES9HandleNotificationRequest{PendingNotification: pending})
			if !bytes.Equal(<-bodies, wantBody) {
				t.Fatal("replay changed original notification")
			}
			if err = store.FinishEUICCReplay(testEID, 17, "attempt-1", state); err != nil {
				t.Fatal(err)
			}
			if _, fresh, err = store.BeginEUICCReplay(testEID, 17, "attempt-1", archive.SHA256); err != nil || fresh {
				t.Fatal("same attempt would send twice")
			}
			retained, err := store.EUICCNotificationArchive(testEID, 17)
			if err != nil || !bytes.Equal(retained.Payload, payload) || retained.Attempts[0].State != state {
				t.Fatal("result or original material lost")
			}
		})
	}
}
