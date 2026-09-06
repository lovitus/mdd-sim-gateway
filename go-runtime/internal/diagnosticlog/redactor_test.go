package diagnosticlog

import (
	"strings"
	"testing"
)

func TestRedactStringRemovesRuntimeSecretsAndKeepsUsefulShape(t *testing.T) {
	input := strings.Join([]string{
		`Authorization: Digest username="234100000000001", nonce="secret", response="answer"`,
		`Contact: <sip:234100000000001@[2001:db8::1]:5060>`,
		`imei=123456789012345 msisdn=+441234567890 peer=+85231234567`,
		`pin=2468 token=private-token apdu=00a4040000`,
		`host=192.0.2.10 mac=00:11:22:33:44:55 path=/Users/private/runtime.log`,
		`server=epdg.example.net state=/var/lib/mdd/runtime.json windows=C:\ProgramData\MDD\config.json`,
	}, "\n")
	output := RedactString(input)
	for _, secret := range []string{"234100000000001", "secret", "answer", "2001:db8::1", "+441234567890",
		"+85231234567", "2468", "private-token", "00a4040000", "192.0.2.10", "00:11:22:33:44:55",
		"epdg.example.net", "/Users/private", "/var/lib/mdd", `C:\ProgramData\MDD`} {
		if strings.Contains(output, secret) {
			t.Fatalf("redacted output retained %q: %s", secret, output)
		}
	}
	for _, marker := range []string{"Authorization: <redacted>", "<redacted-id-", "<redacted-msisdn-",
		"<redacted-ipv4-", "<redacted-local-path>"} {
		if !strings.Contains(output, marker) {
			t.Fatalf("redacted output lost marker %q: %s", marker, output)
		}
	}
}

func TestRedactorReusesPlaceholdersWithinOneDiagnostic(t *testing.T) {
	output := NewRedactor().RedactString("peer=+441234567890 again=+441234567890")
	if strings.Count(output, "<redacted-msisdn-1>") != 2 {
		t.Fatalf("placeholder mapping was unstable: %s", output)
	}
}
