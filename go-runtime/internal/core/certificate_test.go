package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"
	"time"
)

func TestPublicCertificateDescribesActualSignatureAndNames(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture-root"}, DNSNames: []string{"gateway.example"}, NotBefore: now, NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, root, root, public, private)
	if err != nil {
		t.Fatal(err)
	}
	var info PublicRuntimeInfo
	if err := info.DescribeCertificate(der); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(der)
	if info.Certificate.SelfSigned == nil || !*info.Certificate.SelfSigned || info.Certificate.DNSNames[0] != "gateway.example" || info.TLSFingerprintSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal(info)
	}
	leaf := *root
	leaf.SerialNumber = big.NewInt(2)
	leaf.Subject = pkix.Name{CommonName: "fixture-leaf"}
	leaf.IsCA = false
	leaf.KeyUsage = x509.KeyUsageDigitalSignature
	der, err = x509.CreateCertificate(rand.Reader, &leaf, root, public, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := info.DescribeCertificate(der); err != nil {
		t.Fatal(err)
	}
	if info.Certificate.SelfSigned == nil || *info.Certificate.SelfSigned {
		t.Fatal("issuer-signed certificate was labeled self-signed")
	}
	before, _ := json.Marshal(info)
	if err := info.DescribeCertificate([]byte("invalid")); err == nil {
		t.Fatal("invalid certificate accepted")
	}
	after, _ := json.Marshal(info)
	if string(before) != string(after) {
		t.Fatal("failed parse changed public certificate facts")
	}
}

func TestRuntimeCertificateIsAnIndependentSnapshot(t *testing.T) {
	self := true
	info := RuntimeInfo{Public: PublicRuntimeInfo{Certificate: &PublicCertificate{SelfSigned: &self, DNSNames: []string{"original.example"}}}}
	server := &Server{}
	WithRuntimeInfo(info)(server)
	self = false
	info.Public.Certificate.DNSNames[0] = "changed.example"
	if !*server.runtimeInfo.Public.Certificate.SelfSigned || server.runtimeInfo.Public.Certificate.DNSNames[0] != "original.example" {
		t.Fatal("caller changed published certificate facts")
	}
}
