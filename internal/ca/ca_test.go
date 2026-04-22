package ca

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

func TestLoadOrCreate_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	c1, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if !c1.Cert.IsCA {
		t.Fatalf("root is not marked as CA")
	}
	c2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if !c1.Cert.Equal(c2.Cert) {
		t.Fatalf("reload produced different cert")
	}
}

func TestLeafFor_ChainsToRoot(t *testing.T) {
	c, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	leaf, err := c.LeafFor("example.com")
	if err != nil {
		t.Fatalf("leaf: %v", err)
	}
	if leaf.Leaf == nil || leaf.Leaf.Subject.CommonName != "example.com" {
		t.Fatalf("unexpected leaf CN: %+v", leaf.Leaf)
	}

	pool := x509.NewCertPool()
	pool.AddCert(c.Cert)
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{
		Roots:   pool,
		DNSName: "example.com",
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestLeafFor_Cached(t *testing.T) {
	c, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	a, _ := c.LeafFor("example.com")
	b, _ := c.LeafFor("example.com")
	if a != b {
		t.Fatalf("leaf cache did not return identical *tls.Certificate")
	}
}

var _ = tls.Certificate{}
