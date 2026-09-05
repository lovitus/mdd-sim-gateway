package agentat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

type provisionATFake struct {
	sms, apn    string
	writes      []string
	state       SIMPINState
	exchangeErr map[string]error
}

func (fake *provisionATFake) SIMPINStatusFresh(context.Context, string) (SIMPINStatus, error) {
	state := fake.state
	if state == "" {
		state = SIMPINNotRequired
	}
	return SIMPINStatus{CardID: "card-1", State: state}, nil
}

func (fake *provisionATFake) Exchange(_ context.Context, _, command string, _ time.Duration) ([]byte, error) {
	if err := fake.exchangeErr[command]; err != nil {
		return nil, err
	}
	switch command {
	case "AT+CGSN":
		return []byte("862547055201716\r\nOK\r\n"), nil
	case "AT+CIMI":
		return []byte("234101234567890\r\nOK\r\n"), nil
	case "AT+CSCA?":
		return []byte(`+CSCA: "` + fake.sms + `",145\r\nOK\r\n`), nil
	case "AT+CGDCONT?":
		return []byte(`+CGDCONT: 1,"IP","` + fake.apn + `","0.0.0.0"\r\nOK\r\n`), nil
	default:
		fake.writes = append(fake.writes, command)
		if strings.HasPrefix(command, `AT+CSCA="`) {
			fake.sms = strings.TrimSuffix(strings.TrimPrefix(command, `AT+CSCA="`), `"`)
		}
		if strings.HasPrefix(command, `AT+CGDCONT=1,"IP","`) {
			fake.apn = strings.TrimSuffix(strings.TrimPrefix(command, `AT+CGDCONT=1,"IP","`), `"`)
		}
		return []byte("OK\r\n"), nil
	}
}

func provisionRequest() agentlink.ProvisionRequest {
	return agentlink.ProvisionRequest{ProvisionCommand: agentlink.ProvisionCommand{
		OperationID: "op-1", LineID: "line-1", EquipmentID: "862547055201716",
		CardID: "card-1", AttachmentID: "attach-1", SIMSessionGeneration: "sim-1",
		IMSI: "234101234567890", MCC: "234", MNC: "10", IMEI: "862547055201716",
		SMSC: "+447785016005", APN: "internet",
	}}
}

func TestProvisionHardwareWritesAndReadsBackPortableFields(t *testing.T) {
	fake := &provisionATFake{}
	step, readback, err := NewProvisionHardware(fake).ApplyProvision(context.Background(), provisionRequest())
	if err != nil {
		t.Fatalf("ApplyProvision() error = %v", err)
	}
	if step != "readback" || readback.SMSC != "+447785016005" || readback.APN != "internet" {
		t.Fatalf("unexpected result: step=%q readback=%+v", step, readback)
	}
	if len(fake.writes) != 2 {
		t.Fatalf("writes = %v, want SMSC and APN only", fake.writes)
	}
}

func TestProvisionHardwareNeverWritesIMEI(t *testing.T) {
	fake := &provisionATFake{}
	_, _, err := NewProvisionHardware(fake).ApplyProvision(context.Background(), provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range fake.writes {
		if command == "AT+CGSN" || command == "AT+CIMI" {
			t.Fatalf("identity command was treated as a write: %q", command)
		}
	}
}

func TestProvisionHardwareRejectsLockedSIMBeforeAnyWrite(t *testing.T) {
	fake := &provisionATFake{state: SIMPINRequired}
	_, _, err := NewProvisionHardware(fake).ApplyProvision(context.Background(), provisionRequest())
	if err == nil {
		t.Fatal("locked SIM was accepted")
	}
	if len(fake.writes) != 0 {
		t.Fatalf("writes=%v, want none for locked SIM", fake.writes)
	}
}

func TestProvisionHardwareRejectsQuotedAPNBeforeWrite(t *testing.T) {
	fake := &provisionATFake{}
	request := provisionRequest()
	request.APN = `internet","AT+CLCK="SC`
	_, _, err := NewProvisionHardware(fake).ApplyProvision(context.Background(), request)
	if err == nil {
		t.Fatal("quoted APN was accepted")
	}
	if len(fake.writes) != 1 || !strings.HasPrefix(fake.writes[0], `AT+CSCA="`) {
		t.Fatalf("writes=%v, want only SMSC write before APN validation", fake.writes)
	}
}

func TestProvisionHardwareRejectsMissingIMSI(t *testing.T) {
	fake := &provisionATFake{}
	adapter := NewProvisionHardware(fake)
	_, _, err := adapter.ApplyProvision(context.Background(), agentlink.ProvisionRequest{
		ProvisionCommand: agentlink.ProvisionCommand{
			EquipmentID: "modem-1", CardID: "card-1", IMEI: "123456789012345",
		},
	})
	if err == nil || len(fake.writes) != 0 {
		t.Fatalf("expected missing IMSI to fail before AT access, err=%v writes=%v", err, fake.writes)
	}
}

func TestProvisionHardwareFailsWhenAPNReadbackUnavailable(t *testing.T) {
	fake := &provisionATFake{exchangeErr: map[string]error{"AT+CGDCONT?": errors.New("readback unavailable")}}
	adapter := NewProvisionHardware(fake)
	_, _, err := adapter.ApplyProvision(context.Background(), agentlink.ProvisionRequest{
		ProvisionCommand: agentlink.ProvisionCommand{
			EquipmentID: "modem-1", CardID: "card-1", IMEI: "123456789012345",
			IMSI: "310260123456789", APN: "internet",
		},
	})
	if err == nil {
		t.Fatal("expected APN readback failure")
	}
}
