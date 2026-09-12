// Package credentials loads installation-owned transport credentials. It never
// enrols a source, shares device keys or includes private PEM in diagnostics.
package credentials

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
)

// RelayTLS requires an independently issued client identity for this Desk,
// with explicit Hub server trust and normal endpoint hostname verification.
func RelayTLS(directory string) (*tls.Config, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, errors.New("TLS: нужен абсолютный путь к каталогу сертификата Desk")
	}
	if err := checkPrivatePath(directory, true); err != nil {
		return nil, err
	}
	key := filepath.Join(directory, "client-key.pem")
	if err := checkPrivatePath(key, false); err != nil {
		return nil, err
	}
	certificate, err := tls.LoadX509KeyPair(filepath.Join(directory, "client.pem"), key)
	if err != nil {
		return nil, errors.New("TLS: не удалось загрузить сертификат и ключ Desk")
	}
	data, err := os.ReadFile(filepath.Join(directory, "server-ca.pem"))
	if err != nil {
		return nil, errors.New("TLS: не удалось загрузить доверенный сертификат Hub")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("TLS: неверный доверенный сертификат Hub")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, RootCAs: roots}, nil
}
