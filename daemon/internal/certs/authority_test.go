package certs

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestLoadOrCreatePersistsAuthority(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.CertificatePath() != second.CertificatePath() {
		t.Fatalf("certificate path changed")
	}
	if string(first.CertificatePEM()) != string(second.CertificatePEM()) {
		t.Fatalf("certificate was not persisted")
	}
}

func TestLeafCertificateForHost(t *testing.T) {
	authority, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := authority.TLSCertificateForHost("example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.Certificate) == 0 {
		t.Fatal("expected certificate chain")
	}
	cert, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("example.test"); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(authority.CertificatePEM())
	if block == nil {
		t.Fatal("expected pem block")
	}
}

func TestRotateAuthority(t *testing.T) {
	authority, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before := string(authority.CertificatePEM())
	if err := authority.Rotate(); err != nil {
		t.Fatal(err)
	}
	after := string(authority.CertificatePEM())
	if before == after {
		t.Fatal("expected rotated certificate")
	}
}
