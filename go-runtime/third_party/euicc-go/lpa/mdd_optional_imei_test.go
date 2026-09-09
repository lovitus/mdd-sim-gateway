package lpa

import (
	"net/url"
	"testing"
)

func TestMDDOptionalDownloadIMEI(t *testing.T) {
	for _, test := range []struct {
		value string
		valid bool
	}{
		{"", true}, {"123456789012347", true}, {"123", false}, {"12345678901234A", false},
	} {
		code := ActivationCode{SMDP: &url.URL{Scheme: "https", Host: "example.com"}, IMEI: test.value}
		if (code.validate() == nil) != test.valid {
			t.Fatalf("unexpected validation for %q", test.value)
		}
	}
}
