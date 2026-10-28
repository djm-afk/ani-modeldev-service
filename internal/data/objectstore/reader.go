package objectstore

import (
	"context"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// ObjectReader reads bounded bytes of one fixed published version. It shares the
// owner-configured client and connection with verification and signing: the
// object reference selects the key, never the endpoint or credentials.
type ObjectReader struct {
	client       *s3.Client
	connectionID string
}

var _ biz.MetricsObjectReader = (*ObjectReader)(nil)

func NewObjectReader(client *s3.Client, connectionID string) *ObjectReader {
	return &ObjectReader{client: client, connectionID: connectionID}
}

// ReadObject returns at most maxBytes of the exact version. Any scope, version
// or transport problem is a finite read failure; callers degrade instead of
// treating it as a statement about the execution.
func (r *ObjectReader) ReadObject(ctx context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, maxBytes int64) ([]byte, error) {
	if ctx == nil || r == nil || r.client == nil || r.connectionID == "" || maxBytes <= 0 || scope.StorageConnectionID != r.connectionID || object.StorageConnectionID != r.connectionID || object.Bucket != scope.Bucket || !bucketPattern.MatchString(scope.Bucket) || !biz.ValidStorageKey(scope.ApprovedPrefix) || !biz.ValidStorageKey(object.Key) || !strings.HasPrefix(object.Key, scope.ApprovedPrefix+"/") {
		return nil, biz.ErrObjectSourceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if object.VersionID == nil || object.ImmutableCopy != nil || !biz.ValidStorageReference(*object.VersionID) || strings.EqualFold(*object.VersionID, "null") {
		return nil, biz.ErrObjectSourceUnavailable
	}
	version := *object.VersionID
	response, err := r.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(object.Bucket), Key: aws.String(object.Key), VersionId: &version})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, biz.ErrObjectSourceUnavailable
	}
	if response == nil || response.Body == nil {
		return nil, biz.ErrObjectSourceUnavailable
	}
	defer response.Body.Close()
	// Read one byte past the budget so an oversized object is rejected rather
	// than silently truncated into a plausible but incomplete series.
	buffer, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, biz.ErrObjectSourceUnavailable
	}
	if int64(len(buffer)) > maxBytes {
		return nil, biz.ErrObjectSourceUnavailable
	}
	return buffer, nil
}
