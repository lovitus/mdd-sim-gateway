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

type transactionalProvisionATFake struct {
	*provisionATFake
	transactions int
}

func (fake *transactionalProvisionATFake) WithProvisionTransaction(ctx context.Context, callback func(ProvisionAT) error) error {
	fake.transactions++
	return callback(fake.provisionATFake)
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
	case "AT+CGSN=1":
		return []byte("8625470552017160\r\nOK\r\n"), nil
	case "AT+CSCA?":
		return []byte(`+CSCA: "` + fake.sms + `",145\r\nOK\r\n`), nil
	case "AT+COPS?":
		return []byte(`+COPS: 0,2,"23410",7\r\nOK\r\n`), nil
	case "AT+CNUM":
		return []byte(`+CNUM: "line","+447700900123",145\r\nOK\r\n`), nil
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

func TestProvisionHardwareKeepsValidationWritesAndReadbackInOneTransaction(t *testing.T) {
	fake := &transactionalProvisionATFake{provisionATFake: &provisionATFake{}}
	step, _, err := NewProvisionHardware(fake).ApplyProvision(context.Background(), provisionRequest())
	if err != nil || step != "readback" || fake.transactions != 1 || len(fake.writes) != 2 {
		t.Fatalf("step=%q transactions=%d writes=%v err=%v", step, fake.transactions, fake.writes, err)
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
	if coded, ok := err.(interface{ ProvisionFailureCode() string }); !ok || coded.ProvisionFailureCode() != "provision_sim_identity_mismatch" {
		t.Fatalf("unexpected failure code: %v", err)
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
	_, _, err := adapter.ApplyProvision(context.Background(), provisionRequest())
	if err == nil {
		t.Fatal("expected APN readback failure")
	}
	if coded, ok := err.(interface{ ProvisionFailureCode() string }); !ok || coded.ProvisionFailureCode() != "provision_apn_readback_failed" {
		t.Fatalf("unexpected failure code: %v", err)
	}
}

func TestProvisionHardwareReadsOptionalIdentityFields(t *testing.T) {
	fake := &provisionATFake{}
	request := provisionRequest()
	request.MSISDN = "+447700900123"
	request.IMEISV = "8625470552017160"
	_, readback, err := NewProvisionHardware(fake).ApplyProvision(context.Background(), request)
	if err != nil || readback.MCC != "234" || readback.MNC != "10" || readback.MSISDN != request.MSISDN ||
		readback.IMEISV != "8625470552017160" {
		t.Fatalf("optional identity readback=%+v err=%v", readback, err)
	}
}

func TestProvisionIdentityParsersRejectMalformedFields(t *testing.T) {
	if number := parseProvisionMSISDN([]byte(`+CNUM: "line","447700900123",145`)); number != "" {
		t.Fatalf("number without international prefix parsed as %q", number)
	}
	if imeisv := firstDigits([]byte("86254705520171X0\r\n"), 16); imeisv != "" {
		t.Fatalf("malformed IMEISV parsed as %q", imeisv)
	}
}

func TestProvisionHardwareDoesNotRequireOptionalIdentityCommands(t *testing.T) {
	fake := &provisionATFake{exchangeErr: map[string]error{
		"AT+CGSN=1": errors.New("IMEISV unsupported"),
		"AT+CNUM":   errors.New("MSISDN unsupported"),
	}}
	_, _, err := NewProvisionHardware(fake).ApplyProvision(context.Background(), provisionRequest())
	if err != nil {
		t.Fatalf("optional commands blocked portable provision: %v", err)
	}
}

func TestProvisionHardwareUsesIMSIHomePLMNWhileRoaming(t *testing.T) {
	fake := &provisionATFake{exchangeErr: map[string]error{
		"AT+COPS?": errors.New("serving PLMN is unrelated to home PLMN"),
	}}
	_, readback, err := NewProvisionHardware(fake).ReadProvision(context.Background(), provisionRequest())
	if err != nil || readback.MCC != "234" || readback.MNC != "10" {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
}

func TestProvisionHardwareRejectsHomePLMNOutsideIMSI(t *testing.T) {
	request := provisionRequest()
	request.MCC, request.MNC = "310", "260"
	_, _, err := NewProvisionHardware(&provisionATFake{}).ReadProvision(context.Background(), request)
	if err == nil {
		t.Fatal("mismatched IMSI home PLMN was accepted")
	}
	if coded, ok := err.(interface{ ProvisionFailureCode() string }); !ok || coded.ProvisionFailureCode() != "provision_plmn_readback_failed" {
		t.Fatalf("unexpected failure code: %v", err)
	}
}
