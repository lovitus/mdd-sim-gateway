package agentusbip

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

const (
	testExporterToken = "usbip-exporter-server-token-at-least-32-bytes"
	testImporterToken = "usbip-importer-server-token-at-least-32-bytes"
)

func TestBrokerPairsExactExporterAndImporterWSS(t *testing.T) {
	broker := testBroker(t)
	reservation := testReservation()
	if err := broker.Reserve(reservation); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	exporterConnector, err := NewConnector(server.URL, testExporterToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	importerConnector, err := NewConnector(server.URL, testImporterToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		conn net.Conn
		err  error
	}
	exporterResult, importerResult := make(chan result, 1), make(chan result, 1)
	go func() {
		conn, connectErr := exporterConnector.Connect(context.Background(), endpointFor(reservation, RoleExporter))
		exporterResult <- result{conn: conn, err: connectErr}
	}()
	go func() {
		conn, connectErr := importerConnector.Connect(context.Background(), endpointFor(reservation, RoleImporter))
		importerResult <- result{conn: conn, err: connectErr}
	}()
	exporter, importer := <-exporterResult, <-importerResult
	if exporter.err != nil || importer.err != nil {
		t.Fatalf("exporter=%v importer=%v", exporter.err, importer.err)
	}
	defer exporter.conn.Close()
	defer importer.conn.Close()
	want := []byte("one-paired-multiplexed-usbip-stream")
	go func() { _, _ = exporter.conn.Write(want) }()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(importer.conn, got); err != nil || string(got) != string(want) {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestBrokerRejectsChangedPhysicalSIMOrEndpointRole(t *testing.T) {
	broker := testBroker(t)
	reservation := testReservation()
	if err := broker.Reserve(reservation); err != nil {
		t.Fatal(err)
	}
	record := brokerRecord(broker, reservation.StreamID)
	changed := endpointFor(reservation, RoleExporter)
	changed.CardID = "8944100000000000002"
	if record.endpointAllowed(changed) {
		t.Fatal("changed SIM matched an existing USB/IP reservation")
	}
	wrongRole := endpointFor(reservation, RoleImporter)
	wrongRole.AgentID = reservation.SourceAgentID
	wrongRole.ProcessGeneration = reservation.SourceProcessGeneration
	if record.endpointAllowed(wrongRole) {
		t.Fatal("source Agent claimed the importer role")
	}
	if record.tokenMatches(RoleExporter, reservation.ImporterStreamToken) ||
		record.tokenMatches(RoleImporter, reservation.ExporterStreamToken) {
		t.Fatal("one endpoint role accepted the other endpoint's one-time token")
	}
}

func TestBrokerRejectsExporterTokenOnImporterEndpoint(t *testing.T) {
	broker := testBroker(t)
	reservation := testReservation()
	if err := broker.Reserve(reservation); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	connector, err := NewConnector(server.URL, testImporterToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	identity := endpointFor(reservation, RoleImporter)
	identity.StreamToken = reservation.ExporterStreamToken
	if _, err := connector.Connect(context.Background(), identity); err == nil {
		t.Fatal("importer endpoint accepted the exporter's one-time token")
	}
}

func TestBrokerRejectsWrongAgentBearer(t *testing.T) {
	broker := testBroker(t)
	server := httptest.NewServer(broker)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/agent/usbip/ws", nil)
	request.Header.Set("Authorization", "Bearer wrong-token-that-is-still-long-enough-to-test")
	request.Header.Set("X-MDD-Agent-ID", "agent-a")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

func TestBrokerExpiresUnpairedReservation(t *testing.T) {
	now := time.Now().UTC()
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(context.Context, string) (string, error) {
		return testExporterToken, nil
	}), func() time.Time { return now }, 0)
	if err != nil {
		t.Fatal(err)
	}
	reservation := testReservation()
	reservation.ExpiresAt = now.Add(time.Minute)
	if err := broker.Reserve(reservation); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	broker.purge(now)
	if brokerHasReservation(broker, reservation.StreamID) {
		t.Fatal("expired USB/IP reservation remains")
	}
}

func TestBrokerAutonomouslyExpiresSingleConnectedEndpoint(t *testing.T) {
	broker := testBroker(t)
	reservation := testReservation()
	reservation.ExpiresAt = time.Now().Add(100 * time.Millisecond)
	if err := broker.Reserve(reservation); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	connector, err := NewConnector(server.URL, testExporterToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, connectErr := connector.Connect(context.Background(), endpointFor(reservation, RoleExporter))
		result <- connectErr
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("single endpoint unexpectedly completed the USB/IP handshake")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("single endpoint remained blocked after the reservation deadline")
	}
	if brokerHasReservation(broker, reservation.StreamID) {
		t.Fatal("expired connected endpoint remains reserved")
	}
}

func TestBrokerDoesNotExpireFullyAcknowledgedSession(t *testing.T) {
	broker := testBroker(t)
	reservation := testReservation()
	reservation.ExpiresAt = time.Now().Add(300 * time.Millisecond)
	if err := broker.Reserve(reservation); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(broker)
	defer server.Close()
	exporterConnector, err := NewConnector(server.URL, testExporterToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	importerConnector, err := NewConnector(server.URL, testImporterToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		conn net.Conn
		err  error
	}
	exporterResult, importerResult := make(chan result, 1), make(chan result, 1)
	go func() {
		conn, connectErr := exporterConnector.Connect(context.Background(), endpointFor(reservation, RoleExporter))
		exporterResult <- result{conn: conn, err: connectErr}
	}()
	go func() {
		conn, connectErr := importerConnector.Connect(context.Background(), endpointFor(reservation, RoleImporter))
		importerResult <- result{conn: conn, err: connectErr}
	}()
	exporter, importer := <-exporterResult, <-importerResult
	if exporter.err != nil || importer.err != nil {
		t.Fatalf("exporter=%v importer=%v", exporter.err, importer.err)
	}
	defer exporter.conn.Close()
	defer importer.conn.Close()
	time.Sleep(time.Until(reservation.ExpiresAt.Add(100 * time.Millisecond)))
	broker.purge(reservation.ExpiresAt.Add(time.Second))
	if !brokerHasReservation(broker, reservation.StreamID) {
		t.Fatal("fully acknowledged USB/IP session was expired at its handshake deadline")
	}
	want := []byte("still-active")
	go func() { _, _ = exporter.conn.Write(want) }()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(importer.conn, got); err != nil || string(got) != string(want) {
		t.Fatalf("active stream got=%q err=%v", got, err)
	}
}

func TestBrokerDisconnectAgentRevokesEitherSideOfUSBIPStream(t *testing.T) {
	broker := testBroker(t)
	first := testReservation()
	second := testReservation()
	second.StreamID = "usb-stream-b"
	second.USBSessionID = "usb-session-b"
	second.SourceAgentID = "agent-c"
	second.ImporterAgentID = "agent-d"
	for _, input := range []Reservation{first, second} {
		if err := broker.Reserve(input); err != nil {
			t.Fatal(err)
		}
	}
	broker.DisconnectAgent("agent-b")
	if brokerHasReservation(broker, first.StreamID) || !brokerHasReservation(broker, second.StreamID) {
		t.Fatal("disconnecting the importer did not revoke only its USB/IP stream")
	}
}

func testBroker(t *testing.T) *Broker {
	t.Helper()
	broker, err := NewBroker(agentlink.TokenResolverFunc(func(_ context.Context, agentID string) (string, error) {
		switch agentID {
		case "agent-a":
			return testExporterToken, nil
		case "agent-b":
			return testImporterToken, nil
		default:
			return "", errors.New("unknown Agent")
		}
	}), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func brokerRecord(broker *Broker, streamID string) *reservation {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return broker.items[streamID]
}

func brokerHasReservation(broker *Broker, streamID string) bool {
	return brokerRecord(broker, streamID) != nil
}

func testReservation() Reservation {
	return Reservation{
		SessionIdentity: SessionIdentity{
			SourceAgentID: "agent-a", SourceProcessGeneration: "process-a", AttachmentID: "attachment-a",
			SessionGeneration: "card-generation-a", EquipmentID: "867530900000001", CardID: "8944100000000000001",
			USBSessionID: "usb-session-a", CaptureGeneration: "capture-generation-a", StreamID: "usb-stream-a",
		},
		ImporterAgentID: "agent-b", ImporterProcessGeneration: "process-b",
		ExporterStreamToken: "one-time-exporter-stream-token-at-least-32-bytes",
		ImporterStreamToken: "one-time-importer-stream-token-at-least-32-bytes",
		ExpiresAt:           time.Now().Add(time.Minute),
	}
}

func endpointFor(reservation Reservation, role Role) EndpointIdentity {
	identity := EndpointIdentity{SessionIdentity: reservation.SessionIdentity, Role: role}
	if role == RoleExporter {
		identity.StreamToken = reservation.ExporterStreamToken
		identity.AgentID, identity.ProcessGeneration = reservation.SourceAgentID, reservation.SourceProcessGeneration
	} else {
		identity.StreamToken = reservation.ImporterStreamToken
		identity.AgentID, identity.ProcessGeneration = reservation.ImporterAgentID, reservation.ImporterProcessGeneration
	}
	return identity
}
