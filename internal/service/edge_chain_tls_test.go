//go:build linux && edgeintegration

package service

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/tcp"
)

type edgeChainTLS struct {
	serverDir, clientDir, publicKey string
	clientTLS                       *tls.Config
	replacementTLS                  *tls.Config
	replacementKey                  string
	relayDir                        string
	allowRelay                      bool
	automaticFeibot                 bool
}

func (f *edgeChainTLS) copyServerBundle(t *testing.T, hub *edgeChainHub) {
	t.Helper()
	// Extract as the container's existing unprivileged UID. Docker cp -a
	// cannot resolve its numeric user in this image's passwd database; no
	// root chown, world-readable key or source bind mount is required.
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	if err := w.WriteHeader(&tar.Header{Name: "edge-chain-tls", Typeflag: tar.TypeDir, Mode: 0700}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"server.pem", "server-key.pem", "ca.pem", ".ready"} {
		var content []byte
		if name != ".ready" {
			var err error
			content, err = os.ReadFile(filepath.Join(f.serverDir, name))
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := w.WriteHeader(&tar.Header{Name: "edge-chain-tls/" + name, Mode: 0600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "exec", "-i", hub.id, "tar", "-x", "-C", "/tmp")
	cmd.Stdin = &data
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unprivileged synthetic TLS bundle extraction: %v %s", err, output)
	}
}

// Generate the real Edge private key/CSR using its unprivileged CLI, then sign
// only the verified CSR in this synthetic offline issuer. The issuer key is
// never copied into either process, source tree, configuration or output.
func newEdgeChainTLS(t *testing.T) *edgeChainTLS {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := &edgeChainTLS{serverDir: filepath.Join(root, "hub"), clientDir: filepath.Join(root, "device")}
	cmd := exec.Command(edgeChainBinary(t, "EDGE_SIDECAR_BINARY"), "tls-request", "-directory", fixture.clientDir)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("unprivileged Edge CSR command: %v", err)
	}
	var request struct {
		PublicKeySHA256 string `json:"public_key_sha256"`
		CSRPath         string `json:"csr_path"`
	}
	if json.Unmarshal(output, &request) != nil || request.CSRPath != filepath.Join(fixture.clientDir, "client.csr") {
		t.Fatal("invalid CSR command public response")
	}
	data, err := os.ReadFile(request.CSRPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("missing public CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		t.Fatal("unsigned/invalid CSR")
	}
	hash := sha256.Sum256(csr.RawSubjectPublicKeyInfo)
	fixture.publicKey = hex.EncodeToString(hash[:])
	if fixture.publicKey != request.PublicKeySHA256 {
		t.Fatal("CSR identity mismatch")
	}
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, ca, serverPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{SerialNumber: big.NewInt(3), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, ca, csr.PublicKey, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	expiredTemplate := *clientTemplate
	expiredTemplate.SerialNumber = big.NewInt(4)
	expiredTemplate.NotBefore, expiredTemplate.NotAfter = now.Add(-2*time.Hour), now.Add(-time.Hour)
	expiredDER, err := x509.CreateCertificate(rand.Reader, &expiredTemplate, ca, csr.PublicKey, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := x509.MarshalPKCS8PrivateKey(serverPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.serverDir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(directory, name, kind string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, name), pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: data}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(fixture.serverDir, "server.pem", "CERTIFICATE", serverDER)
	write(fixture.serverDir, "server-key.pem", "PRIVATE KEY", serverKey)
	write(fixture.serverDir, "ca.pem", "CERTIFICATE", caDER)
	write(fixture.clientDir, "client.pem", "CERTIFICATE", clientDER)
	write(fixture.clientDir, "expired-client.pem", "CERTIFICATE", expiredDER)
	write(fixture.clientDir, "ca.pem", "CERTIFICATE", caDER)
	write(fixture.clientDir, "server-ca.pem", "CERTIFICATE", caDER)
	certificate, err := tls.LoadX509KeyPair(filepath.Join(fixture.clientDir, "client.pem"), filepath.Join(fixture.clientDir, "client-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	fixture.clientTLS = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: "localhost"}
	replacementPublic, replacementPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	replacementTemplate := *clientTemplate
	replacementTemplate.SerialNumber = big.NewInt(5)
	replacementDER, err := x509.CreateCertificate(rand.Reader, &replacementTemplate, ca, replacementPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	replacementLeaf, err := x509.ParseCertificate(replacementDER)
	if err != nil {
		t.Fatal(err)
	}
	replacementHash := sha256.Sum256(replacementLeaf.RawSubjectPublicKeyInfo)
	fixture.replacementKey = hex.EncodeToString(replacementHash[:])
	fixture.replacementTLS = fixture.clientTLS.Clone()
	fixture.replacementTLS.Certificates = []tls.Certificate{{Certificate: [][]byte{replacementDER}, PrivateKey: replacementPrivate}}
	fixture.relayDir = filepath.Join(root, "desk-relay")
	if err := os.Mkdir(fixture.relayDir, 0700); err != nil {
		t.Fatal(err)
	}
	replacementPrivateDER, err := x509.MarshalPKCS8PrivateKey(replacementPrivate)
	if err != nil {
		t.Fatal(err)
	}
	write(fixture.relayDir, "client.pem", "CERTIFICATE", replacementDER)
	write(fixture.relayDir, "client-key.pem", "PRIVATE KEY", replacementPrivateDER)
	write(fixture.relayDir, "server-ca.pem", "CERTIFICATE", caDER)
	return fixture
}

func TestEdgeMutualTLSKeyRotationAndRevocationOnReplacementListener(t *testing.T) {
	security := newEdgeChainTLS(t)
	data, err := os.ReadFile("testdata/edge-observation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := edge.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := edge.Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, rotated := range []bool{false, true} {
		name := "original-key"
		if rotated {
			name = "old-key-revoked"
		}
		// Each subtest joins and removes its own original listener before the
		// replacement starts. No authenticated connection survives the cutover.
		t.Run(name, func(t *testing.T) {
			selected := *security
			allowed, denied := security.clientTLS, security.replacementTLS
			if rotated {
				selected.publicKey = security.replacementKey
				allowed, denied = denied, allowed
			}
			hub := newEdgeChainHubTopology(t, event.ExternalEventID, event.Board, event.SourceSessionID, "none", false, &selected)
			client := tcp.LineClient{Endpoint: "tls://" + hub.endpoint, Timeout: time.Second, TLSConfig: denied}
			if err := client.Send(t.Context(), payload, strings.TrimSpace(string(edge.ACK(event)))); err == nil {
				t.Fatal("CA-signed but unlisted/revoked key received an ACK")
			}
			_ = client.Close()
			if len(hub.entries(t)) != 0 {
				t.Fatal("unlisted/revoked key reached Redis")
			}
			client.TLSConfig = allowed
			defer client.Close()
			if err := client.Send(t.Context(), payload, strings.TrimSpace(string(edge.ACK(event)))); err != nil || len(hub.entries(t)) != 1 {
				t.Fatalf("explicitly authorized key failed: %v", err)
			}
		})
	}
}
