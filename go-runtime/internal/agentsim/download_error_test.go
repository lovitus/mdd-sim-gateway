package agentsim

import (
	"crypto/x509"
	"errors"
	"fmt"
	euicchttp "github.com/damonto/euicc-go/http"
	sgp22 "github.com/damonto/euicc-go/v2"
	"net"
	"net/url"
	"testing"
)

func TestDownloadErrorCodesDoNotExposeSecrets(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{&sgp22.StatusCodeData{SubjectCode: "8.8.2", ReasonCode: "3.1", Message: "secret activation code"}, "euicc_rsp_8.8.2_3.1"},
		{sgp22.StatusCodeData{SubjectCode: "8.1.3", ReasonCode: "6.1"}, "euicc_rsp_8.1.3_6.1"},
		{&sgp22.StatusCodeData{SubjectCode: "secret", ReasonCode: "3.1"}, "euicc_rsp_rejected"},
		{&euicchttp.StatusError{StatusCode: 403}, "euicc_http_403"},
		{&url.Error{Op: "Post", URL: "https://secret", Err: x509.UnknownAuthorityError{}}, "euicc_tls_certificate_failed"},
		{&net.DNSError{Err: "secret", Name: "private-host"}, "euicc_dns_failed"},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("secret")}, "euicc_network_failed"},
		{errors.New("secret unknown error"), "euicc_download_failed"},
	} {
		if got := downloadErrorCode(fmt.Errorf("wrapped: %w", test.err)); got != test.code {
			t.Fatalf("got %q want %q", got, test.code)
		}
	}
}
