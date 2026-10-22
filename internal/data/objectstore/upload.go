package objectstore

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// ArtifactUploader copies one already-verified published object version into a
// Model-issued upload ticket. It owns only the byte transport: the ticket, the
// frozen object and the decision that this copy may happen all come from the
// caller. It never retries on its own, because a failed copy is a real fact the
// durable dispatch arbitration must settle.
type ArtifactUploader struct {
	client       *s3.Client
	connectionID string
	httpClient   *http.Client
}

var _ biz.ArtifactCopyUploader = (*ArtifactUploader)(nil)

// uploadTimeout bounds one whole copy. A large bundle is streamed, not
// buffered, so this is a transfer budget rather than a response budget.
const uploadTimeout = 30 * time.Minute

func NewArtifactUploader(client *s3.Client, connectionID string) *ArtifactUploader {
	return &ArtifactUploader{
		client:       client,
		connectionID: connectionID,
		httpClient:   &http.Client{Timeout: uploadTimeout},
	}
}

// WithHTTPClient overrides the copy transport. It exists for isolated tests
// that must observe the exact bytes and headers; production keeps the bounded
// default.
func (u *ArtifactUploader) WithHTTPClient(client *http.Client) *ArtifactUploader {
	if client != nil {
		u.httpClient = client
	}
	return u
}

// Upload streams the exact frozen object version through the Model-issued
// upload ticket. It never selects a different endpoint, bucket, key or version,
// and it never falls back to the mutable latest object.
func (u *ArtifactUploader) Upload(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, ticket biz.UploadTicket) (biz.UploadReceipt, error) {
	if ctx == nil || u == nil || u.client == nil || u.httpClient == nil || u.connectionID == "" ||
		scope.StorageConnectionID != u.connectionID || object.StorageConnectionID != u.connectionID ||
		object.Bucket != scope.Bucket || !bucketPattern.MatchString(scope.Bucket) ||
		!biz.ValidStorageKey(scope.ApprovedPrefix) || !biz.ValidStorageKey(object.Key) ||
		!strings.HasPrefix(object.Key, scope.ApprovedPrefix+"/") {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	if err := ctx.Err(); err != nil {
		return biz.UploadReceipt{}, err
	}
	if object.VersionID == nil || object.ImmutableCopy != nil || !biz.ValidStorageReference(*object.VersionID) ||
		strings.EqualFold(*object.VersionID, "null") || object.SizeBytes <= 0 ||
		len(object.SHA256) != 64 || strings.ToLower(object.SHA256) != object.SHA256 {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	// The ticket must describe the same object the registration froze, and its
	// target must be the provider's own storage path rather than a caller
	// value. A provider that requires no extra headers is valid: Model issues a
	// plain presigned PUT, so an empty header set is honoured rather than
	// rejected.
	if ticket.StoragePath == "" || !biz.ValidStorageKey(ticket.StoragePath) ||
		ticket.SizeBytes != object.SizeBytes || ticket.SHA256 != object.SHA256 ||
		!secureUploadURL(ticket.URL) || !ticket.ExpiresAt.After(time.Now()) {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	version := *object.VersionID
	response, err := u.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(object.Bucket), Key: aws.String(object.Key), VersionId: &version})
	if err != nil {
		if ctx.Err() != nil {
			return biz.UploadReceipt{}, ctx.Err()
		}
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	if response == nil || response.Body == nil {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	defer response.Body.Close()
	// The read is bounded by the frozen size; the remote read version is
	// re-checked so a bucket that moved between resolution and copy cannot
	// silently upload different bytes.
	if response.VersionId == nil || *response.VersionId != version {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, ticket.URL, io.LimitReader(response.Body, object.SizeBytes))
	if err != nil {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	request.ContentLength = object.SizeBytes
	request.Header.Set("Content-Type", "application/octet-stream")
	for name, value := range ticket.Headers {
		if strings.EqualFold(name, "Content-Type") || strings.HasPrefix(strings.ToLower(name), "host") {
			return biz.UploadReceipt{}, biz.ErrUploadUnavailable
		}
		request.Header.Set(name, value)
	}
	uploaded, err := u.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return biz.UploadReceipt{}, ctx.Err()
		}
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	defer uploaded.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(uploaded.Body, 4<<10))
	if uploaded.StatusCode < 200 || uploaded.StatusCode >= 300 {
		return biz.UploadReceipt{}, biz.ErrUploadUnavailable
	}
	// Only the ticket's own storage path is reported back: the caller persists
	// the provider receipt, not an invented location.
	return biz.UploadReceipt{StoragePath: ticket.StoragePath, SizeBytes: object.SizeBytes, ChecksumSHA256: object.SHA256}, nil
}

// secureUploadURL rejects anything that would leak credentials or downgrade
// transport. The provider issues this ticket, so a malformed one is a provider
// contract failure rather than a caller error.
func secureUploadURL(raw string) bool {
	location, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if location.Scheme != "https" || location.Hostname() == "" || location.User != nil ||
		location.Fragment != "" || location.ForceQuery {
		return false
	}
	return true
}
