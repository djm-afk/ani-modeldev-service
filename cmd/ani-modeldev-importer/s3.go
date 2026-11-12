package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// credentialFile is the mounted Secret shape the S3 client reads on each call.
// Rotating the Secret therefore takes effect without restarting the job, and the
// value never enters the process environment or the plan.
type credentialFile struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
	// Expiration is the session's proven expiry. It is optional so an older
	// Secret shape still works, but when present an expired session is refused
	// before the request leaves the Pod.
	Expiration string `json:"expiration"`
}

// errCredentialsExpired is a distinct cause so the caller can report a timeout
// rather than an opaque dependency failure.
var errCredentialsExpired = errors.New("import credentials expired")

func newS3Client(endpoint, region, credentialPath string) (*s3.Client, error) {
	if endpoint == "" || region == "" || credentialPath == "" {
		return nil, errors.New("s3 endpoint, region and credential file are required")
	}
	provider := aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		if ctx == nil || ctx.Err() != nil {
			return aws.Credentials{}, ctx.Err()
		}
		raw, err := os.ReadFile(credentialPath)
		if err != nil {
			return aws.Credentials{}, err
		}
		var credential credentialFile
		if err := json.Unmarshal(raw, &credential); err != nil {
			return aws.Credentials{}, err
		}
		if credential.AccessKeyID == "" || credential.SecretAccessKey == "" ||
			strings.ContainsAny(credential.AccessKeyID+credential.SecretAccessKey+credential.SessionToken, " \t\r\n") {
			return aws.Credentials{}, errors.New("invalid credential file")
		}
		// An expired session cannot sign anything. Saying so here keeps the
		// failure attributable instead of surfacing as AccessDenied from the store.
		if credential.Expiration != "" {
			expiresAt, parseErr := time.Parse(time.RFC3339, credential.Expiration)
			if parseErr != nil {
				return aws.Credentials{}, errors.New("invalid credential expiry")
			}
			if !expiresAt.After(time.Now()) {
				return aws.Credentials{}, errCredentialsExpired
			}
		}
		return aws.Credentials{
			AccessKeyID: credential.AccessKeyID, SecretAccessKey: credential.SecretAccessKey,
			SessionToken: credential.SessionToken, Source: "ani-dataset-issuance",
		}, nil
	})
	options := s3.Options{
		Region: region, BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials: provider,
	}
	// The platform store presents an internal CA. Trusting only the system roots
	// would turn every upload into an opaque TLS failure, so the mounted bundle
	// is loaded explicitly when present.
	if bundle := os.Getenv("AWS_CA_BUNDLE"); bundle != "" {
		pem, err := os.ReadFile(bundle)
		if err != nil {
			return nil, errors.New("s3 CA bundle unreadable")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("s3 CA bundle invalid")
		}
		options.HTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	}
	return s3.New(options), nil
}
