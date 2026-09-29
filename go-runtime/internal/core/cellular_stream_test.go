package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/callhistory"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmedia"
)

type streamCellularHandler struct {
	mu    sync.Mutex
	calls []cellularmedia.IncomingCallView
}

func (handler *streamCellularHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	http.NotFound(response, request)
}

func (handler *streamCellularHandler) IncomingCalls() ([]cellularmedia.IncomingCallView, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]cellularmedia.IncomingCallView{}, handler.calls...), nil
}

func TestCellularMediaIncomingReachesBrowserAndMobileStreams(t *testing.T) {
	for _, path := range []string{"/v1/browser/ws", "/v1/mobile/ws"} {
		for _, withUI := range []bool{false, true} {
			name := path + "/headless"
			if withUI {
				name = path + "/webui"
			}
			t.Run(name, func(t *testing.T) {
				now := time.Now().UTC().Round(0)
				want := cellularmedia.IncomingCallView{
					CellularCallSource: callhistory.CellularCallSource{
						IncomingEventID: "incoming-1", LastEventID: "event-1", Revision: 1,
						LineID: "line-1", AgentID: "agent-1", ProcessGeneration: "process-1",
						AttachmentID: "attachment-1", EquipmentID: "equipment-1",
						CardID: "8901000000000000001", SIMSessionGeneration: "sim-1",
						Occurrence: 1, NativeCallIndex: 1, State: "ringing_in", Direction: "in",
						Number: "+12025550123", FirstObservedAt: now, ObservedAt: now, ReceivedAt: now,
						Notify: true,
					},
					Actionable: true,
				}
				handler := &streamCellularHandler{calls: []cellularmedia.IncomingCallView{want}}
				verifier := &toggleBrowserVerifier{}
				verifier.allowed.Store(true)
				options := []Option{WithBrowserControl(verifier), WithCellularMedia(handler)}
				if withUI {
					options = append(options, WithWebUI(http.HandlerFunc(http.NotFound)))
				}
				server := NewServer(testReplay(t, now), time.Now, options...)
				server.browserEvery = 10 * time.Millisecond
				host := httptest.NewServer(server)
				defer host.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(host.URL, "http")+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
				readCalls := func() []cellularmedia.IncomingCallView {
					t.Helper()
					var frame map[string]json.RawMessage
					if err := wsjson.Read(ctx, conn, &frame); err != nil {
						t.Fatal(err)
					}
					if path == "/v1/mobile/ws" {
						var data map[string]json.RawMessage
						if err := json.Unmarshal(frame["data"], &data); err != nil {
							t.Fatal(err)
						}
						frame = data
					}
					var calls []cellularmedia.IncomingCallView
					if raw, ok := frame["cellular_calls"]; ok {
						if err := json.Unmarshal(raw, &calls); err != nil {
							t.Fatal(err)
						}
					}
					return calls
				}
				if got := readCalls(); !reflect.DeepEqual(got, []cellularmedia.IncomingCallView{want}) {
					t.Fatalf("real stream lost cellular incoming identity/actionability: got %+v, want %+v", got, want)
				}
				handler.mu.Lock()
				handler.calls = nil
				handler.mu.Unlock()
				for len(readCalls()) != 0 {
				}
			})
		}
	}
}
