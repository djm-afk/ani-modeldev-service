package modeldevwebhook

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

var ErrCertificate = errors.New("modeldev webhook certificate unavailable")

// CertificateSource reloads the projected certificate/key files at each new
// TLS handshake. A rotation never needs a Pod restart. A briefly inconsistent
// projected pair can use the previous still-valid certificate, while readiness
// fails until the new pair is valid; expired certificates never fall back.
type CertificateSource struct {
	mu                       sync.Mutex
	certificateFile, keyFile string
	digest                   [32]byte
	certificate              *tls.Certificate
}

func NewCertificateSource(certificateFile, keyFile string) (*CertificateSource, error) {
	source := &CertificateSource{certificateFile: certificateFile, keyFile: keyFile}
	if _, err := source.reload(); err != nil {
		return nil, err
	}
	return source, nil
}

func readCertificate(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrCertificate
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(raw) == 0 || len(raw) > 65536 {
		return nil, ErrCertificate
	}
	return raw, nil
}

func (source *CertificateSource) reload() (*tls.Certificate, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	certificate, err := readCertificate(source.certificateFile)
	if err != nil {
		return nil, err
	}
	key, err := readCertificate(source.keyFile)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(append(append([]byte(nil), certificate...), key...))
	if source.certificate != nil && digest == source.digest {
		if validCertificate(source.certificate) {
			return source.certificate, nil
		}
		return nil, ErrCertificate
	}
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, ErrCertificate
	}
	pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !validCertificate(&pair) {
		return nil, ErrCertificate
	}
	source.digest, source.certificate = digest, &pair
	return source.certificate, nil
}

func validCertificate(certificate *tls.Certificate) bool {
	return certificate != nil && certificate.Leaf != nil && time.Now().After(certificate.Leaf.NotBefore) && time.Now().Before(certificate.Leaf.NotAfter)
}

func (source *CertificateSource) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	certificate, err := source.reload()
	if err == nil {
		return certificate, nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if validCertificate(source.certificate) {
		return source.certificate, nil
	}
	return nil, ErrCertificate
}

func (source *CertificateSource) Ready() bool { _, err := source.reload(); return err == nil }

// NewServer requires a valid initial keypair and serves TLS only. The mounted
// Secret can be renewed by cert-manager; issuer availability is an independent
// deployment prerequisite and is never assumed by this code.
func NewServer(config Config, handler *Handler) (*http.Server, error) {
	if handler == nil {
		return nil, ErrConfiguration
	}
	config.defaults()
	source, err := NewCertificateSource(config.CertificateFile, config.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	handler.certificateReady = source.Ready
	return &http.Server{Addr: config.ListenAddress, Handler: handler,
		ErrorLog:          log.New(io.Discard, "", 0),
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: source.GetCertificate},
		ReadHeaderTimeout: 2 * time.Second, ReadTimeout: time.Duration(config.RequestTimeoutMilliseconds+1000) * time.Millisecond,
		WriteTimeout: time.Duration(config.RequestTimeoutMilliseconds+1000) * time.Millisecond, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10}, nil
}
