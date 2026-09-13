package credentials

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestPacketLANTLSIsStableAndContainsNoPrivateMaterial(t *testing.T) {
	directory := t.TempDir()
	first, err := LoadOrCreatePacketLAN(directory, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreatePacketLAN(directory, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if first.CAFingerprint == "" || first.CAFingerprint != second.CAFingerprint || string(first.CACertificate) != string(second.CACertificate) {
		t.Fatal("installation CA changed across restart")
	}
	if block, rest := pem.Decode(first.CACertificate); block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("returned CA is not one certificate")
	} else if certificate, err := x509.ParseCertificate(block.Bytes); err != nil || !certificate.IsCA {
		t.Fatal("returned certificate is not a CA")
	} else if got := sha256.Sum256(certificate.Raw); first.CAFingerprint != fmtHex(got[:]) {
		t.Fatal("CA fingerprint mismatch")
	}
	if _, err := os.Stat(filepath.Join(directory, "packet-lan-tls", "ca-key.pem")); err != nil {
		t.Fatal(err)
	}
}

func TestPacketLANTLSFailsClosedOnIncompleteMaterial(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "packet-lan-tls")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := securePrivatePath(target, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "ca.pem"), []byte("not-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreatePacketLAN(directory, "11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatal("incomplete TLS identity was silently replaced")
	}
}

func fmtHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, b := range value {
		result[index*2] = alphabet[b>>4]
		result[index*2+1] = alphabet[b&15]
	}
	return string(result)
}
