package agentlink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/adminauth"
)

func TestAgentSetupEnrollmentAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	credential := `{"version":1,"username":"admin","salt":"00112233445566778899aabbccddeeff","password_hash":"ecf058348a9bfd4febce50a1ae9205da2720790fccdae3644bf0ed98c9740302","agent_token":"` + testToken + `"}`
	if err := os.WriteFile(path, []byte(credential), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := adminauth.NewManager(path, false, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := NewServer(auth)
	endpoint := httptest.NewServer(server)
	defer endpoint.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	url := strings.Replace(endpoint.URL, "http", "ws", 1)
	// Upgrade authenticates with legacy fallback, but hello has not registered.
	socket, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + testToken}, "X-Mdd-Agent-Id": {"existing-agent"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	if _, err := auth.CreateAgentToken("existing-agent", strings.Repeat("a", 32), server.ReserveEnrollment); err != adminauth.ErrEnrollmentConflict {
		t.Fatalf("ENROLLMENT_STOLE_INFLIGHT_IDENTITY: %v", err)
	}
	if err := writeEnvelope(ctx, socket, envelope{Kind: kindHello, Hello: &Hello{SchemaVersion: SchemaVersion, AgentID: "existing-agent", ProcessGeneration: "process-1"}}); err != nil {
		t.Fatal(err)
	}
	if ack, err := readEnvelope(ctx, socket); err != nil || ack.Kind != kindHelloAck {
		t.Fatalf("existing connection lost: %v", err)
	}
	token, err := auth.CreateAgentToken("new-agent", strings.Repeat("b", 32), server.ReserveEnrollment)
	if err != nil {
		t.Fatal(err)
	}
	stale, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + testToken}, "X-Mdd-Agent-Id": {"new-agent"}},
	})
	if stale != nil {
		stale.CloseNow()
	}
	if err == nil {
		t.Fatal("created identity accepted legacy fallback")
	}
	current, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}, "X-Mdd-Agent-Id": {"new-agent"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer current.CloseNow()
	if err := writeEnvelope(ctx, current, envelope{Kind: kindHello, Hello: &Hello{SchemaVersion: SchemaVersion, AgentID: "new-agent", ProcessGeneration: "process-2"}}); err != nil {
		t.Fatal(err)
	}
	if ack, err := readEnvelope(ctx, current); err != nil || ack.Kind != kindHelloAck {
		t.Fatalf("new credential rejected: %v", err)
	}
	if _, online := server.Status("existing-agent"); !online {
		t.Fatal("unrelated existing Agent disconnected")
	}
}
