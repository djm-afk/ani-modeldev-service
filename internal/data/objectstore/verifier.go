package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// Verifier uses an owner-configured S3 client. Endpoints and credentials never
// come from a component-supplied object reference.
type Verifier struct {
	client       *s3.Client
	connectionID string
}

func NewVerifier(client *s3.Client, connectionID string) *Verifier {
	return &Verifier{client: client, connectionID: connectionID}
}

// Verify reads an exact object version under the owner's approved storage scope.
// The first slice requires a real VersionID; mutable unversioned keys are not
// sufficient to turn a point-in-time read into an immutable artifact reference.
func (v *Verifier) Verify(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef) (biz.VerifiedObject, error) {
	return v.verify(ctx, scope, object, nil)
}

// inspect, when present, must consume the complete bounded stream and may
// reject its structure. Both paths verify the same actual length and digest.
func (v *Verifier) verify(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, inspect func(io.Reader) error) (biz.VerifiedObject, error) {
	if err := ctx.Err(); err != nil {
		return biz.VerifiedObject{}, err
	}
	if v == nil || v.client == nil || v.connectionID == "" || scope.StorageConnectionID != v.connectionID || object.StorageConnectionID != v.connectionID || object.Bucket != scope.Bucket || !bucketPattern.MatchString(scope.Bucket) || !biz.ValidStorageKey(scope.ApprovedPrefix) || !biz.ValidStorageKey(object.Key) || !strings.HasPrefix(object.Key, scope.ApprovedPrefix+"/") {
		// Scope and client settings belong to the trusted owner. Their failure
		// prevents an observation; it does not establish invalid source bytes.
		return biz.VerifiedObject{}, biz.ErrObjectSourceUnavailable
	}
	if object.VersionID == nil || object.ImmutableCopy != nil || !biz.ValidStorageReference(*object.VersionID) || strings.EqualFold(*object.VersionID, "null") || object.SizeBytes < 0 || len(object.SHA256) != 64 || strings.ToLower(object.SHA256) != object.SHA256 {
		return biz.VerifiedObject{}, biz.ErrObjectVerification
	}
	if _, err := hex.DecodeString(object.SHA256); err != nil {
		return biz.VerifiedObject{}, biz.ErrObjectVerification
	}
	version := *object.VersionID
	response, err := v.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(object.Bucket), Key: aws.String(object.Key), VersionId: &version})
	if err != nil {
		if ctx.Err() != nil {
			return biz.VerifiedObject{}, ctx.Err()
		}
		return biz.VerifiedObject{}, biz.ErrObjectSourceUnavailable
	}
	if response == nil || response.Body == nil {
		return biz.VerifiedObject{}, biz.ErrObjectSourceUnavailable
	}
	defer response.Body.Close()
	digest := sha256.New()
	measured := &measuredReader{reader: io.TeeReader(io.LimitReader(response.Body, object.SizeBytes), digest)}
	var inspectErr error
	if inspect != nil {
		inspectErr = inspect(measured)
	}
	// A parser can reject early or replace a transport read error with its own
	// syntax error. Finish only the remaining bounded stream, retaining actual
	// reader failures before deciding whether content is permanently invalid.
	_, readErr := io.Copy(io.Discard, measured)
	if err := ctx.Err(); err != nil {
		return biz.VerifiedObject{}, err
	}
	if measured.readErr != nil || readErr != nil {
		return biz.VerifiedObject{}, biz.ErrObjectSourceUnavailable
	}
	var extra [1]byte
	n, extraErr := response.Body.Read(extra[:])
	if err := ctx.Err(); err != nil {
		return biz.VerifiedObject{}, err
	}
	if extraErr != nil && extraErr != io.EOF || n == 0 && extraErr == nil {
		return biz.VerifiedObject{}, biz.ErrObjectSourceUnavailable
	}
	if response.VersionId == nil || *response.VersionId != version || measured.size != object.SizeBytes || n != 0 || hex.EncodeToString(digest.Sum(nil)) != object.SHA256 {
		return biz.VerifiedObject{}, biz.ErrObjectVerification
	}
	if inspectErr != nil {
		return biz.VerifiedObject{}, inspectErr
	}
	object.VersionID = &version
	return biz.VerifiedObject{Object: object, VerifiedAt: time.Now().UTC()}, nil
}

type measuredReader struct {
	reader  io.Reader
	size    int64
	readErr error
}

func (r *measuredReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.size += int64(n)
	if err != nil && err != io.EOF && r.readErr == nil {
		r.readErr = err
	}
	return n, err
}

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
