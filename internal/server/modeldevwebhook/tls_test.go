package modeldevwebhook_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
)

func certificateAuthority(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-only CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte{1, 2, 3, 4}}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}

func leafCertificate(t *testing.T, ca *x509.Certificate, signingKey *ecdsa.PrivateKey, serial int64, expired bool) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "webhook.test"}, DNSNames: []string{"webhook.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, SubjectKeyId: []byte{byte(serial)}, AuthorityKeyId: ca.SubjectKeyId}
	if expired {
		template.NotBefore, template.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
}

func writePair(t *testing.T, config modeldevwebhook.Config, certificate, key []byte) {
	t.Helper()
	for path, body := range map[string][]byte{config.CertificateFile: certificate, config.PrivateKeyFile: key} {
		temporary := path + ".new"
		if err := os.WriteFile(temporary, body, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temporary, path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRealHTTPSAdmissionAndCertificateRotationWithoutRestart(t *testing.T) {
	config, reader := fixtureConfig(t)
	directory := t.TempDir()
	config.CertificateFile, config.PrivateKeyFile = filepath.Join(directory, "tls.crt"), filepath.Join(directory, "tls.key")
	ca, caKey, caPEM := certificateAuthority(t)
	certificate, key := leafCertificate(t, ca, caKey, 2, false)
	writePair(t, config, certificate, key)
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server, err := modeldevwebhook.NewServer(config, handler)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	defer func() { _ = server.Close(); <-done }()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA unavailable")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "webhook.test", MinVersion: tls.VersionTLS13}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	url := "https://" + listener.Addr().String()
	check := func(serial int64, ready int) {
		t.Helper()
		response, err := client.Get(url + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != ready || response.TLS == nil || response.TLS.PeerCertificates[0].SerialNumber.Int64() != serial || len(response.TLS.VerifiedChains) == 0 {
			t.Fatalf("expected trusted serial %d ready %d, response %+v", serial, ready, response)
		}
	}
	check(2, 200)
	certificate, key = leafCertificate(t, ca, caKey, 3, false)
	writePair(t, config, certificate, key)
	check(3, 200)
	if err := os.WriteFile(config.PrivateKeyFile, []byte("not a private key"), 0600); err != nil {
		t.Fatal(err)
	}
	// A bad projected renewal fails readiness; the current valid identity serves
	// until the pair becomes coherent, with no expiry extension or new identity.
	check(3, 503)
	writePair(t, config, certificate, key)
	check(3, 200)
	bad := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "wrong.test"}}, Timeout: time.Second}
	if response, err := bad.Get(url + "/livez"); err == nil {
		_ = response.Body.Close()
		t.Fatal("wrong hostname accepted")
	}
	expired, expiredKey := leafCertificate(t, ca, caKey, 4, true)
	writePair(t, config, expired, expiredKey)
	if _, err := modeldevwebhook.NewCertificateSource(config.CertificateFile, config.PrivateKeyFile); err == nil {
		t.Fatal("expired initial certificate accepted")
	}
}
