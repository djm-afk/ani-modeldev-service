package server

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
)

// The mounted command certificate must present the one registered server
// identity; a mismatch would silently fail every Governance handshake.
func TestValidCommandServerCertificateRequiresRegisteredIdentity(t *testing.T) {
	certificates := commandtls.New(t)
	cases := []struct {
		name   string
		mutate func(*x509.Certificate)
		valid  bool
	}{
		{"registered server identity", func(leaf *x509.Certificate) {
			leaf.DNSNames = []string{CommandServerDNSName}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}, true},
		{"registered plus extra SAN", func(leaf *x509.Certificate) {
			leaf.DNSNames = []string{CommandServerDNSName, "extra.internal"}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}, true},
		{"wildcard identity", func(leaf *x509.Certificate) {
			leaf.DNSNames = []string{"*.ani-modeldev-service"}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}, false},
		{"registered plus wildcard SAN", func(leaf *x509.Certificate) {
			leaf.DNSNames = []string{CommandServerDNSName, "*.internal"}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}, false},
		{"unregistered identity", func(leaf *x509.Certificate) {
			leaf.DNSNames = []string{"other-modeldev.test"}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}, false},
		{"missing server usage", func(leaf *x509.Certificate) {
			leaf.DNSNames = []string{CommandServerDNSName}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}, false},
		{"expired certificate", func(leaf *x509.Certificate) {
			leaf.DNSNames = []string{CommandServerDNSName}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			leaf.NotAfter = time.Now().Add(-time.Minute)
		}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			certificate := certificates.ClientCertificate(t, testCase.mutate)
			if got := ValidCommandServerCertificate(certificate); got != testCase.valid {
				t.Fatalf("ValidCommandServerCertificate = %t, want %t", got, testCase.valid)
			}
		})
	}
}
