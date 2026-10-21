package objectstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
)

type uploadFixture struct {
	scope  cpup01.StorageScope
	object cpup01.FixedObjectRef
	body   []byte
}

func newUploadFixture() uploadFixture {
	body := []byte("frozen-bundle-bytes")
	sum := sha256.Sum256(body)
	scope := cpup01.StorageScope{StorageConnectionID: "approved-store", Bucket: "artifacts", ApprovedPrefix: "tenant/execution"}
	version := "immutable-version-1"
	return uploadFixture{
		scope: scope,
		body:  body,
		object: cpup01.FixedObjectRef{
			StorageConnectionID: scope.StorageConnectionID,
			Bucket:              scope.Bucket,
			Key:                 scope.ApprovedPrefix + "/bundle.tar",
			VersionID:           &version,
			SizeBytes:           int64(len(body)),
			SHA256:              hex.EncodeToString(sum[:]),
		},
	}
}

func (f uploadFixture) ticket(url string) biz.UploadTicket {
	return biz.UploadTicket{
		URL:         url,
		StoragePath: "11111111/22222222/exec-1/bundle.tar",
		Headers:     map[string]string{"X-Amz-Server-Side-Encryption": "AES256"},
		SizeBytes:   f.object.SizeBytes,
		SHA256:      f.object.SHA256,
		ExpiresAt:   time.Now().Add(15 * time.Minute),
	}
}

// The uploader is exercised through a real HTTP receiver and a stubbed S3 read
// path. Nothing here proves Model's authorization or a real provider ticket.
func TestArtifactUploaderStreamsFrozenVersionToTicket(t *testing.T) {
	fixture := newUploadFixture()
	var received []byte
	var receivedHeaders http.Header
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedHeaders = request.Header.Clone()
		received, _ = io.ReadAll(request.Body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	uploader := newTestUploader(fixture).WithHTTPClient(receiver.Client())
	receipt, err := uploader.Upload(context.Background(), fixture.scope, fixture.object, fixture.ticket(receiver.URL))
	if err != nil {
		t.Fatalf("frozen version copy failed: %v", err)
	}
	if string(received) != string(fixture.body) || receivedHeaders.Get("X-Amz-Server-Side-Encryption") != "AES256" {
		t.Fatal("copy did not stream the frozen bytes with the ticket headers")
	}
	if receipt.StoragePath != fixture.ticket(receiver.URL).StoragePath || receipt.SizeBytes != fixture.object.SizeBytes || receipt.ChecksumSHA256 != fixture.object.SHA256 {
		t.Fatalf("receipt did not report the provider ticket target: %+v", receipt)
	}
}

func TestArtifactUploaderRejectsUnusableTicketsAndObjects(t *testing.T) {
	fixture := newUploadFixture()
	uploader := newTestUploader(fixture)
	valid := fixture.ticket("https://storage.example.test/copy")
	unversioned := fixture.object
	unversioned.VersionID = nil
	wrongScope := cpup01.StorageScope{StorageConnectionID: fixture.scope.StorageConnectionID, Bucket: fixture.scope.Bucket, ApprovedPrefix: "other-tenant"}
	expired := valid
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	insecure := valid
	insecure.URL = "http://storage.example.test/copy"
	mismatchedSize := valid
	mismatchedSize.SizeBytes = fixture.object.SizeBytes + 1
	mismatchedSHA := valid
	mismatchedSHA.SHA256 = strings.Repeat("f", 64)
	emptyTarget := valid
	emptyTarget.StoragePath = ""
	reservedHeader := valid
	reservedHeader.Headers = map[string]string{"Host": "attacker.example.test"}
	tests := []struct {
		name   string
		scope  cpup01.StorageScope
		object cpup01.FixedObjectRef
		ticket biz.UploadTicket
	}{
		{name: "unversioned object", scope: fixture.scope, object: unversioned, ticket: valid},
		{name: "unapproved scope", scope: wrongScope, object: fixture.object, ticket: valid},
		{name: "expired ticket", scope: fixture.scope, object: fixture.object, ticket: expired},
		{name: "insecure ticket", scope: fixture.scope, object: fixture.object, ticket: insecure},
		{name: "size disagrees with frozen object", scope: fixture.scope, object: fixture.object, ticket: mismatchedSize},
		{name: "digest disagrees with frozen object", scope: fixture.scope, object: fixture.object, ticket: mismatchedSHA},
		{name: "missing provider target", scope: fixture.scope, object: fixture.object, ticket: emptyTarget},
		{name: "ticket tries to set the host header", scope: fixture.scope, object: fixture.object, ticket: reservedHeader},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receipt, err := uploader.Upload(context.Background(), test.scope, test.object, test.ticket)
			if !errors.Is(err, biz.ErrUploadUnavailable) || receipt != (biz.UploadReceipt{}) {
				t.Fatalf("unusable copy input produced a receipt: %v %+v", err, receipt)
			}
		})
	}
}

func TestArtifactUploaderDoesNotRetryARejectedProvider(t *testing.T) {
	fixture := newUploadFixture()
	attempts := 0
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer receiver.Close()
	uploader := newTestUploader(fixture).WithHTTPClient(receiver.Client())
	// A provider failure must surface as one unresolved attempt; the durable
	// dispatch arbitration decides whether it is ever re-sent.
	if _, err := uploader.Upload(context.Background(), fixture.scope, fixture.object, fixture.ticket(receiver.URL)); !errors.Is(err, biz.ErrUploadUnavailable) {
		t.Fatalf("provider failure was not surfaced: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("the uploader retried by itself: %d attempts", attempts)
	}
}

// newTestUploader wires the uploader to a stubbed S3 read path; no real storage
// service is reached.
func newTestUploader(fixture uploadFixture) *objectstore.ArtifactUploader {
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String("https://storage.example.test"),
		UsePathStyle: true,
		Credentials:  aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return aws.Credentials{AccessKeyID: "synthetic-key", SecretAccessKey: "synthetic-secret"}, nil }),
		HTTPClient:   &stubS3Reader{body: fixture.body, version: *fixture.object.VersionID},
	})
	return objectstore.NewArtifactUploader(client, fixture.scope.StorageConnectionID)
}

// stubS3Reader answers the versioned GetObject read with fixed frozen bytes.
type stubS3Reader struct {
	body    []byte
	version string
}

func (r *stubS3Reader) Do(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet {
		return nil, errors.New("unexpected non-read object request")
	}
	if request.URL.Query().Get("versionId") != r.version {
		return nil, errors.New("read did not pin the frozen object version")
	}
	header := make(http.Header)
	header.Set("Content-Length", "0")
	header.Set("ETag", `"synthetic-etag"`)
	header.Set("x-amz-version-id", r.version)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(string(r.body))),
		ContentLength: int64(len(r.body)),
		Request:       request,
	}, nil
}
