package agentsim

import (
	"crypto/x509"
	"errors"
	"net"
	"strconv"
	"strings"

	euicchttp "github.com/damonto/euicc-go/http"
	sgp22 "github.com/damonto/euicc-go/v2"
)

// Preserve upstream machine codes, never server-supplied text or request URLs.
func downloadErrorCode(err error) string {
	var rsp *sgp22.StatusCodeData
	var value sgp22.StatusCodeData
	if errors.As(err, &rsp) && rsp != nil {
		return rspDownloadCode(*rsp)
	}
	if errors.As(err, &value) {
		return rspDownloadCode(value)
	}
	var status *euicchttp.StatusError
	if errors.As(err, &status) && status != nil && status.StatusCode >= 300 && status.StatusCode <= 599 {
		return "euicc_http_" + strconv.Itoa(status.StatusCode)
	}
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return "euicc_tls_certificate_failed"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "euicc_dns_failed"
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "euicc_network_timeout"
		}
		return "euicc_network_failed"
	}
	return "euicc_download_failed"
}

func rspDownloadCode(status sgp22.StatusCodeData) string {
	valid := func(code string) bool {
		if len(code) == 0 || len(code) > 24 {
			return false
		}
		for _, part := range strings.Split(code, ".") {
			if part == "" {
				return false
			}
			for _, c := range part {
				if c < '0' || c > '9' {
					return false
				}
			}
		}
		return true
	}
	if !valid(status.SubjectCode) || !valid(status.ReasonCode) {
		return "euicc_rsp_rejected"
	}
	return "euicc_rsp_" + status.SubjectCode + "_" + status.ReasonCode
}
