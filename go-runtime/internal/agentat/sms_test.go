package agentat

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/warthog618/sms"
	"github.com/warthog618/sms/encoding/pdumode"
)

type smsSubmitPort struct {
	mu          sync.Mutex
	response    []byte
	writes      [][]byte
	failPayload bool
}

func (port *smsSubmitPort) Read(buffer []byte) (int, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	if len(port.response) == 0 {
		return 0, nil
	}
	count := copy(buffer, port.response)
	port.response = port.response[count:]
	return count, nil
}

func (port *smsSubmitPort) Write(value []byte) (int, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.writes = append(port.writes, append([]byte(nil), value...))
	switch {
	case string(value) == "AT+CMGF=0\r":
		port.response = []byte("\r\nOK\r\n")
	case strings.HasPrefix(string(value), "AT+CMGS="):
		port.response = []byte("\r\n> ")
	case len(value) > 0 && value[len(value)-1] == 0x1a:
		if port.failPayload {
			return 0, io.ErrClosedPipe
		}
		port.response = []byte("\r\n+CMGS: 0\r\n\r\nOK\r\n")
	}
	return len(value), nil
}

func (*smsSubmitPort) Close() error            { return nil }
func (*smsSubmitPort) Drain() error            { return nil }
func (*smsSubmitPort) ResetInputBuffer() error { return nil }

type typedSMSSubmitPort struct {
	smsSubmitPort
	length       int
	payload      string
	possiblySent bool
	err          error
}

func (port *typedSMSSubmitPort) SubmitSMSPDU(_ context.Context, length int, payload string) ([]byte, bool, error) {
	port.length, port.payload = length, payload
	if port.err != nil {
		return nil, port.possiblySent, port.err
	}
	return []byte("\r\n+CMGS: 23\r\n\r\nOK\r\n"), false, nil
}

func TestSendSMSUsesPDUAndTreatsLostPayloadWriteAsUncertain(t *testing.T) {
	port := &smsSubmitPort{}
	owner := &Owner{port: port, equipmentID: "862547055201716", capabilities: Capabilities{SMS: true}}
	references, err := owner.SendSMS(context.Background(), "862547055201716", "+15550100124", "hello 世界")
	if err != nil || len(references) != 1 || references[0] != 0 || len(port.writes) != 3 {
		t.Fatalf("references=%v writes=%d err=%v", references, len(port.writes), err)
	}
	if !strings.HasPrefix(string(port.writes[1]), "AT+CMGS=") || port.writes[2][len(port.writes[2])-1] != 0x1a {
		t.Fatalf("unexpected submit exchange: %q %x", port.writes[1], port.writes[2])
	}

	failed := &smsSubmitPort{failPayload: true}
	owner.port = failed
	if _, err := owner.SendSMS(context.Background(), "862547055201716", "+15550100124", "hello"); err == nil || !SMSPossiblySent(err) {
		t.Fatalf("payload write error was not uncertain: %v", err)
	}
}

func TestSendSMSUsesTypedAtomicSubmissionWhenPortProvidesIt(t *testing.T) {
	port := &typedSMSSubmitPort{}
	owner := &Owner{port: port, equipmentID: "862547055201716", capabilities: Capabilities{SMS: true}}
	references, err := owner.SendSMS(context.Background(), "862547055201716", "+15550100123", "typed")
	if err != nil || len(references) != 1 || references[0] != 23 || port.length < 1 || port.payload == "" {
		t.Fatalf("references=%v typed_length=%d payload=%q err=%v", references, port.length, port.payload, err)
	}
	if len(port.writes) != 1 || string(port.writes[0]) != "AT+CMGF=0\r" {
		t.Fatalf("typed path unexpectedly used split CMGS writes: %q", port.writes)
	}

	port.err, port.possiblySent = io.ErrUnexpectedEOF, true
	if _, err := owner.SendSMS(context.Background(), "862547055201716", "+15550100123", "uncertain"); err == nil || !SMSPossiblySent(err) {
		t.Fatalf("typed unknown outcome lost duplicate-safety evidence: %v", err)
	}
}

func TestDecodeSMSListReassemblesCompleteMultipartAndSkipsIncomplete(t *testing.T) {
	body := strings.Repeat("跨境短信需要完整重组。", 24)
	pdus, err := sms.Encode([]byte(body), sms.AsDeliver, sms.From("+15550100123"), sms.WithAllCharsets)
	if err != nil || len(pdus) < 2 || len(pdus) > maximumSMSParts {
		t.Fatalf("parts=%d err=%v", len(pdus), err)
	}
	var response strings.Builder
	for index, message := range pdus {
		wire, err := message.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		pdu := pdumode.PDU{TPDU: wire}
		hexPDU, err := pdu.MarshalHexString()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&response, "+CMGL: %d,0,,%d\r\n%s\r\n", index+1, len(wire), hexPDU)
	}
	response.WriteString("\r\nOK\r\n")
	messages, err := decodeSMSList([]byte(response.String()), testSMSFallback())
	if err != nil || len(messages) != 1 || messages[0].Body != body || messages[0].Peer != "+15550100123" ||
		messages[0].State != "received" || len(messages[0].Indices) != len(pdus) {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	responses := map[string][][]byte{
		"AT+CMGF=0": {[]byte("OK\r\n")}, "AT+CMGL=4": {[]byte(response.String())},
	}
	for index := len(pdus); index >= 1; index-- {
		responses[fmt.Sprintf("AT+CMGD=%d", index)] = [][]byte{[]byte("OK\r\n")}
	}
	port := &scriptedCallPort{responses: responses}
	owner := &Owner{port: port, equipmentID: "862547055201716", capabilities: Capabilities{SMS: true}}
	if err := owner.DeleteSMS(context.Background(), owner.equipmentID, messages[0].Indices, messages[0].Fingerprint); err != nil {
		t.Fatal(err)
	}
	if len(port.commands) != 2+len(pdus) || port.commands[2] != fmt.Sprintf("AT+CMGD=%d", len(pdus)) {
		t.Fatalf("multipart delete commands=%v", port.commands)
	}

	firstEnd := strings.Index(response.String(), "+CMGL: 2")
	partial, err := decodeSMSList([]byte(response.String()[:firstEnd]+"\r\nOK\r\n"), testSMSFallback())
	if err != nil || len(partial) != 0 {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
}

func testSMSFallback() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
