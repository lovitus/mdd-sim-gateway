package darwinmodem

import (
	"reflect"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

func TestParseModemIdentitiesAndNetworkFacts(t *testing.T) {
	if got := parseEquipmentID([]byte("AT+CGSN\r\n862547055201716\r\nOK\r\n")); got != "862547055201716" {
		t.Fatalf("equipment ID=%q", got)
	}
	if got := parseICCID([]byte("\r\n+QCCID: 89852012345678901234\r\nOK\r\n")); got != "89852012345678901234" {
		t.Fatalf("ICCID=%q", got)
	}
	if got := parseIMSI([]byte("AT+CIMI\r\n234101234567890\r\nOK\r\n")); got != "234101234567890" {
		t.Fatalf("IMSI=%q", got)
	}
	if got := parseMSISDNs([]byte("+CNUM: \"\",\"+85222333322\",145\r\n+CNUM: \"\",\"+85222333322\",145\r\n")); !reflect.DeepEqual(got, []string{"+85222333322"}) {
		t.Fatalf("MSISDNs=%v", got)
	}
	if got := parseRegistration([]byte("+CREG: 2,5\r\n")); got != agentmodem.RegistrationRoaming {
		t.Fatalf("registration=%q", got)
	}
	if id, name := parseOperator([]byte("+COPS: 0,2,\"23410\",7\r\n")); id != "23410" || name != "" {
		t.Fatalf("operator id=%q name=%q", id, name)
	}
	if id, name := parseOperator([]byte("+COPS: 0,0,\"giffgaff\",7\r\n")); id != "" || name != "giffgaff" {
		t.Fatalf("operator id=%q name=%q", id, name)
	}
	if signal := parseSignal([]byte("+CSQ: 31,99\r\n")); signal == nil || *signal != 100 {
		t.Fatalf("signal=%v", signal)
	}
	if got := parseRadio([]byte("+CFUN: 1\r\n")); got != agentmodem.RadioOn {
		t.Fatalf("radio=%q", got)
	}
	if got := parseSMSC([]byte("+CSCA: \"+447785016005\",145\r\n")); got != "+447785016005" {
		t.Fatalf("SMSC=%q", got)
	}
}

func TestParseModemFactsRejectsAmbiguousOrUnavailableValues(t *testing.T) {
	if got := parseEquipmentID([]byte("123 456")); got != "" {
		t.Fatalf("short equipment ID accepted: %q", got)
	}
	if got := parseRegistration([]byte("+CREG: 2,3\r\n")); got != agentmodem.RegistrationDenied {
		t.Fatalf("denied registration=%q", got)
	}
	if signal := parseSignal([]byte("+CSQ: 99,99\r\n")); signal != nil {
		t.Fatalf("unknown signal accepted: %v", signal)
	}
	if got := parsePINState([]byte("+CPIN: SIM PUK\r\n")); got != "puk_required" {
		t.Fatalf("PIN state=%q", got)
	}
}
