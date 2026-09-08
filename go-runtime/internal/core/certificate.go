package core

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"time"
)

type PublicCertificate struct {
	SelfSigned *bool     `json:"self_signed,omitempty"`
	DNSNames   []string  `json:"dns_names"`
	NotAfter   time.Time `json:"not_after"`
}

// Describe only the loaded public certificate, never private-key paths or
// trust inferred from a name. Non-self-signed does not mean a trusted chain.
func (info *PublicRuntimeInfo) DescribeCertificate(der []byte) error {
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	value := &PublicCertificate{DNSNames: append([]string{}, certificate.DNSNames...), NotAfter: certificate.NotAfter.UTC()}
	if !bytes.Equal(certificate.RawIssuer, certificate.RawSubject) {
		self := false
		value.SelfSigned = &self
	} else if certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature) == nil {
		self := true
		value.SelfSigned = &self
	}
	digest := sha256.Sum256(der)
	info.TLSFingerprintSHA256 = hex.EncodeToString(digest[:])
	info.Certificate = value
	return nil
}
