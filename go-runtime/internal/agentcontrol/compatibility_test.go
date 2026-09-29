package agentcontrol

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The pre-extension wire contract used by installed strict local clients.
type legacyControlSnapshot struct {
	RuntimeConfig *RuntimeConfig `json:"runtime_config,omitempty"`
	State         State          `json:"state"`
	Generation    uint64         `json:"generation"`
	ChangedAt     time.Time      `json:"changed_at"`
	Code          string         `json:"code,omitempty"`
	Detail        string         `json:"detail,omitempty"`
}

type connectionWorker struct{ fakeWorker }

func (*connectionWorker) CoreConnection() CoreConnection {
	return CoreConnection{State: "connected", ChangedAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)}
}

func TestAgentControlConnectionCompatibility(t *testing.T) {
	newAPI := func(t *testing.T, ready, extension bool) (*API, *Controller) {
		t.Helper()
		var worker Worker = &fakeWorker{ready: ready, exit: make(chan error)}
		if extension {
			worker = &connectionWorker{fakeWorker{ready: ready, exit: make(chan error)}}
		}
		controller, err := New(worker, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		api, err := NewAPI(controller, testControlToken, time.Second, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := controller.Stop(ctx); err != nil {
				t.Error(err)
			}
		})
		return api, controller
	}
	for _, tc := range []struct {
		name, method, path string
		start, cancel      bool
		status             int
	}{
		{"stopped", "GET", "/v1/status", false, false, 200},
		{"running", "GET", "/v1/status", true, false, 200},
		{"start", "POST", "/v1/runtime/start", false, false, 200},
		{"conflict", "POST", "/v1/runtime/start", true, false, 409},
		{"stop", "POST", "/v1/runtime/stop", true, false, 200},
		{"timeout", "POST", "/v1/runtime/start", false, true, 504},
	} {
		t.Run("legacy/"+tc.name, func(t *testing.T) {
			api, controller := newAPI(t, !tc.cancel, true)
			if tc.start {
				if _, err := controller.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", "Bearer "+testControlToken)
			if tc.cancel {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("HTTP status: got %d, want %d", response.Code, tc.status)
			}
			var snapshot legacyControlSnapshot
			var result any = &snapshot
			failure := struct {
				Code   string                `json:"code"`
				Status legacyControlSnapshot `json:"status"`
			}{}
			if tc.status != http.StatusOK {
				result = &failure
			}
			if err := decodeControlJSON(response.Body.Bytes(), result); err != nil {
				t.Fatalf("legacy decoder rejected response: %v", err)
			}
			if tc.status != http.StatusOK {
				snapshot = failure.Status
			}
			if snapshot.State == "" || snapshot.ChangedAt.IsZero() {
				t.Fatal("legacy runtime identity was lost")
			}
		})
	}
	for _, oldService := range []bool{false, true} {
		name := "new-client/new-service"
		if oldService {
			name = "new-client/legacy-format-service"
		}
		t.Run(name, func(t *testing.T) {
			// A worker without the extension emits the exact older response schema.
			// This tests that format, not an installed older service binary.
			api, _ := newAPI(t, true, !oldService)
			server := httptest.NewServer(api)
			defer server.Close()
			client, err := NewClient(server.URL, testControlToken, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			status, err := client.Status(context.Background())
			if err != nil || status.State != StateRunning {
				t.Fatalf("status=%+v error=%v", status, err)
			}
			if oldService && status.CoreConnection != nil {
				t.Fatal("legacy service must leave connection unknown")
			}
			if !oldService && (status.CoreConnection == nil || status.CoreConnection.State != "connected") {
				t.Fatal("requested connection metadata missing")
			}
			stopped, err := client.Stop(context.Background())
			if err != nil || stopped.State != StateStopped {
				t.Fatalf("stop=%+v error=%v", stopped, err)
			}
			if !oldService && (stopped.CoreConnection == nil || stopped.CoreConnection.State != "stopped") {
				t.Fatal("stopped runtime retained or lost connection state")
			}
			request, _ := http.NewRequest("GET", server.URL+"/v1/status", nil)
			request.Header.Set("X-MDD-Agent-Core-Connection", "1")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusUnauthorized || string(body) != "{\"code\":\"unauthorized\"}\n" {
				t.Fatalf("extension bypassed authentication: %d %s", response.StatusCode, body)
			}
		})
	}
}
