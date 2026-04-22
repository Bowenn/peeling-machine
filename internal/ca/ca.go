// Package ca manages the local root Certificate Authority used to MITM
// TLS connections, plus on-demand signing of per-host leaf certificates.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	rootCertName = "ca.crt"
	rootKeyName  = "ca.key"
	rootOrg      = "Peeling Machine"
	rootCN       = "Peeling Machine Root CA"
	rootValidity = 10 * 365 * 24 * time.Hour
	leafValidity = 365 * 24 * time.Hour
)

type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     *rsa.PrivateKey

	leafKey *ecdsa.PrivateKey
	cache   sync.Map // host -> *tls.Certificate
	dir     string
}

// LoadOrCreate reads the root CA material from dir, generating it if absent.
func LoadOrCreate(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ca: mkdir %s: %w", dir, err)
	}
	certPath := filepath.Join(dir, rootCertName)
	keyPath := filepath.Join(dir, rootKeyName)

	if _, err := os.Stat(certPath); err == nil {
		return load(dir, certPath, keyPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return generate(dir, certPath, keyPath)
}

func load(dir, certPath, keyPath string) (*CA, error) {
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("ca: read cert: %w", err)
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("ca: read key: %w", err)
	}
	certBlock, _ := pem.Decode(certBytes)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("ca: invalid cert pem")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse cert: %w", err)
	}
	keyBlock, _ := pem.Decode(keyBytes)
	if keyBlock == nil {
		return nil, errors.New("ca: invalid key pem")
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse key: %w", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &CA{
		Cert:    cert,
		CertPEM: certBytes,
		key:     key,
		leafKey: leafKey,
		dir:     dir,
	}, nil
}

func generate(dir, certPath, keyPath string) (*CA, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   rootCN,
			Organization: []string{rootOrg},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(rootValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &CA{
		Cert:    cert,
		CertPEM: certPEM,
		key:     key,
		leafKey: leafKey,
		dir:     dir,
	}, nil
}

// LeafFor returns a server TLS certificate chained to the root, valid for host.
// Results are cached by host; host may be a DNS name or IP address (without port).
func (c *CA) LeafFor(host string) (*tls.Certificate, error) {
	if v, ok := c.cache.Load(host); ok {
		return v.(*tls.Certificate), nil
	}
	cert, err := c.sign(host)
	if err != nil {
		return nil, err
	}
	actual, _ := c.cache.LoadOrStore(host, cert)
	return actual.(*tls.Certificate), nil
}

func (c *CA) sign(host string) (*tls.Certificate, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{rootOrg}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, &c.leafKey.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, c.Cert.Raw},
		PrivateKey:  c.leafKey,
		Leaf:        leaf,
	}, nil
}

// CertPath returns the on-disk path of the root CA PEM.
func (c *CA) CertPath() string { return filepath.Join(c.dir, rootCertName) }

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}
