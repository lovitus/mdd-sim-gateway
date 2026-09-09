package agentlink

import "testing"

func TestEUICCDownloadOptionalIMEI(t *testing.T) {
	for _, test := range []struct {
		imei  string
		valid bool
	}{
		{"", true}, {"123456789012347", true}, {"123", false}, {"12345678901234A", false},
	} {
		command := EUICCDownloadCommand{OperationID: "download-test", EID: "89049032000000000000000000000001", Action: EUICCDownloadStart, ActivationCode: "LPA:1$example.com$test", IMEI: test.imei}
		if (command.Validate() == nil) != test.valid {
			t.Fatalf("unexpected validation for %q", test.imei)
		}
	}
}
