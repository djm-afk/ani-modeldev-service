// Package commandtls creates synthetic identities only for Fedora module tests.
// Its private keys never represent a deployed ENV identity or leave test storage.
package commandtls

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
	"testing"
	"time"
)

// ServerDNSName is the registered server identity for the Governance -> ModelDev
// business listener. Fixtures mint the production literal DNS SAN so the real
// startup and client checks exercise the same name; the CA and keys stay
// synthetic and private to test storage.
const ServerDNSName = "ani-modeldev-service"
const GovernanceDNSName = "ani-governance"

type Certificates struct {
	Roots              *x509.CertPool
	Server, Governance tls.Certificate
	ca                 *x509.Certificate
	key                *ecdsa.PrivateKey
}

func New(t testing.TB) Certificates {
	return NewForServer(t, ServerDNSName)
}

// NewForServer keeps a cross-repository test aligned with its production
// client's fixed server name. This creates only a private synthetic test CA.
func NewForServer(t testing.TB, serverName string) Certificates {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CPU command test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	result := Certificates{Roots: roots, ca: ca, key: key}
	result.Server = result.issue(t, serverName, x509.ExtKeyUsageServerAuth, nil)
	result.Governance = result.ClientCertificate(t, nil)
	return result
}

func (c Certificates) ClientCertificate(t testing.TB, mutate func(*x509.Certificate)) tls.Certificate {
	t.Helper()
	return c.issue(t, GovernanceDNSName, x509.ExtKeyUsageClientAuth, mutate)
}

func (c Certificates) issue(t testing.TB, name string, usage x509.ExtKeyUsage, mutate func(*x509.Certificate)) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	leaf := &x509.Certificate{SerialNumber: serial.Add(serial, big.NewInt(1)), DNSNames: []string{name}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	if mutate != nil {
		mutate(leaf)
	}
	encoded, err := x509.CreateCertificate(rand.Reader, leaf, c.ca, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{encoded}, PrivateKey: key, Leaf: parsed}
}

type Files struct {
	CAFile, CertificateFile, PrivateKeyFile string
}

// WriteServerFiles materializes only this test's synthetic CA and server pair.
// t.TempDir owns cleanup; all files are private to the test process's user.
func (c Certificates) WriteServerFiles(t testing.TB) Files {
	t.Helper()
	directory := t.TempDir()
	files := Files{CAFile: filepath.Join(directory, "ca.pem"), CertificateFile: filepath.Join(directory, "server.pem"), PrivateKeyFile: filepath.Join(directory, "server.key")}
	privateKey, err := x509.MarshalPKCS8PrivateKey(c.Server.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	for path, block := range map[string]*pem.Block{
		files.CAFile:          {Type: "CERTIFICATE", Bytes: c.ca.Raw},
		files.CertificateFile: {Type: "CERTIFICATE", Bytes: c.Server.Certificate[0]},
		files.PrivateKeyFile:  {Type: "PRIVATE KEY", Bytes: privateKey},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal("write synthetic command TLS fixture")
		}
	}
	return files
}
