package credentials

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const PacketLANHostname = "chrono-desk.local"

type PacketLANTLS struct {
	Config        *tls.Config
	CACertificate []byte
	CAFingerprint string
}

// LoadOrCreatePacketLAN builds one installation-owned CA and leaf certificate.
// The CA is stable across application restarts so tablets need to trust it once.
func LoadOrCreatePacketLAN(dataDir, installationID string) (PacketLANTLS, error) {
	if dataDir == "" || installationID == "" {
		return PacketLANTLS{}, errors.New("packet LAN TLS identity is required")
	}
	directory := filepath.Join(dataDir, "packet-lan-tls")
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		if err := createPacketLANDirectory(dataDir, directory, installationID); err != nil {
			return PacketLANTLS{}, err
		}
	} else if err != nil {
		return PacketLANTLS{}, err
	}
	if err := securePrivatePath(directory, true); err != nil {
		return PacketLANTLS{}, fmt.Errorf("protect packet LAN TLS directory: %w", err)
	}
	paths := map[string]string{
		"ca": filepath.Join(directory, "ca.pem"), "caKey": filepath.Join(directory, "ca-key.pem"),
		"server": filepath.Join(directory, "server.pem"), "serverKey": filepath.Join(directory, "server-key.pem"),
	}
	present := 0
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			present++
		} else if !errors.Is(err, os.ErrNotExist) {
			return PacketLANTLS{}, err
		}
	}
	if present != 0 && present != len(paths) {
		return PacketLANTLS{}, errors.New("packet LAN TLS material is incomplete; restore the installation backup")
	}
	if present == 0 {
		return PacketLANTLS{}, errors.New("packet LAN TLS material is missing; restore the installation backup")
	}
	if err := checkPrivatePath(directory, true); err != nil {
		return PacketLANTLS{}, err
	}
	for _, name := range []string{"caKey", "serverKey"} {
		if err := checkPrivatePath(paths[name], false); err != nil {
			return PacketLANTLS{}, err
		}
	}
	certificate, err := tls.LoadX509KeyPair(paths["server"], paths["serverKey"])
	if err != nil {
		return PacketLANTLS{}, errors.New("packet LAN TLS certificate cannot be loaded")
	}
	caPEM, err := os.ReadFile(paths["ca"])
	if err != nil {
		return PacketLANTLS{}, err
	}
	block, rest := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return PacketLANTLS{}, errors.New("packet LAN CA certificate is invalid")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA {
		return PacketLANTLS{}, errors.New("packet LAN CA certificate is invalid")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || leaf.VerifyHostname(PacketLANHostname) != nil || time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		return PacketLANTLS{}, errors.New("packet LAN server certificate is invalid")
	}
	sum := sha256.Sum256(ca.Raw)
	return PacketLANTLS{
		Config: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate},
			// Issuance polling does not benefit from HTTP/2. Keep the Go 1.24
			// compatibility build off its known HTTP/2 server paths.
			NextProtos: []string{"http/1.1"}},
		CACertificate: append([]byte(nil), caPEM...), CAFingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

func createPacketLANDirectory(dataDir, target, installationID string) error {
	material, err := newPacketLANMaterial(installationID, time.Now().UTC())
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(dataDir, ".packet-lan-tls-")
	if err != nil {
		return fmt.Errorf("create packet LAN TLS staging directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := securePrivatePath(temporary, true); err != nil {
		return err
	}
	for name, data := range material {
		filename := map[string]string{"ca": "ca.pem", "caKey": "ca-key.pem", "server": "server.pem", "serverKey": "server-key.pem"}[name]
		mode := os.FileMode(0o644)
		if name == "caKey" || name == "serverKey" {
			mode = 0o600
		}
		if err := writeExclusive(filepath.Join(temporary, filename), data, mode); err != nil {
			return fmt.Errorf("stage packet LAN TLS material: %w", err)
		}
		if name == "caKey" || name == "serverKey" {
			if err := securePrivatePath(filepath.Join(temporary, filename), false); err != nil {
				return fmt.Errorf("protect packet LAN TLS key: %w", err)
			}
		}
	}
	directory, err := os.Open(temporary)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporary, target); err != nil {
		return fmt.Errorf("publish packet LAN TLS material: %w", err)
	}
	return nil
}

func newPacketLANMaterial(installationID string, now time.Time) (map[string][]byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caSerial, err := certificateSerial()
	if err != nil {
		return nil, err
	}
	serverSerial, err := certificateSerial()
	if err != nil {
		return nil, err
	}
	before, after := now.Add(-time.Hour), now.Add(10*365*24*time.Hour)
	caTemplate := &x509.Certificate{SerialNumber: caSerial,
		Subject:   pkix.Name{Organization: []string{"RUN5 Chrono"}, CommonName: "Chrono Desk " + installationID},
		NotBefore: before, NotAfter: after, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	serverTemplate := &x509.Certificate{SerialNumber: serverSerial,
		Subject:  pkix.Name{Organization: []string{"RUN5 Chrono"}, CommonName: PacketLANHostname},
		DNSNames: []string{PacketLANHostname}, NotBefore: before, NotAfter: after,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	caPrivate, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return nil, err
	}
	serverPrivate, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"ca":        pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		"caKey":     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caPrivate}),
		"server":    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		"serverKey": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverPrivate}),
	}, nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func certificateSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	return serial, nil
}
