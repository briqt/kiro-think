package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/briqt/kiro-think/internal/config"
)

// Manager handles CA and per-host certificate generation.
type Manager struct {
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caTLS  tls.Certificate
	cache  sync.Map // hostname -> *tls.Certificate
}

// NewManager loads or generates the CA certificate.
func NewManager() (*Manager, error) {
	dir := config.Dir()
	os.MkdirAll(dir, 0755)
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	m := &Manager{}
	if err := m.loadCA(certPath, keyPath); err != nil {
		if err := m.generateCA(certPath, keyPath); err != nil {
			return nil, err
		}
	}
	if err := m.generateCombinedCA(dir); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) loadCA(certPath, keyPath string) error {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	m.caTLS = tlsCert
	m.caCert, err = x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return err
	}
	m.caKey = tlsCert.PrivateKey.(*ecdsa.PrivateKey)
	return nil
}

func (m *Manager) generateCA(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kiro-think CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	m.caCert, _ = x509.ParseCertificate(certDER)
	m.caKey = key

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	os.WriteFile(certPath, certPEM, 0644)
	os.WriteFile(keyPath, keyPEM, 0600)

	m.caTLS, _ = tls.X509KeyPair(certPEM, keyPEM)
	return nil
}

func (m *Manager) generateCombinedCA(dir string) error {
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.caCert.Raw})

	// Read system CA bundle
	sysPaths := []string{
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/cert.pem",
	}
	var sysCA []byte
	for _, p := range sysPaths {
		if data, err := os.ReadFile(p); err == nil {
			sysCA = data
			break
		}
	}
	combined := append(sysCA, caPEM...)
	return os.WriteFile(filepath.Join(dir, "combined-ca.crt"), combined, 0644)
}

// GetCertificate returns a TLS certificate for the given hostname.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName
	if v, ok := m.cache.Load(host); ok {
		return v.(*tls.Certificate), nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, m.caCert, &key.PublicKey, m.caKey)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}
	m.cache.Store(host, cert)
	return cert, nil
}
