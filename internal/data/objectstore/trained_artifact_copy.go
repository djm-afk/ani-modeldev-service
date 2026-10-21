package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

type TrainedArtifactCopier struct {
	source         *Verifier
	target         *http.Client
	origin, bucket string
}

// The destination is installed owner material. A provider ticket can authorize
// one original path there; it cannot turn the controller into an arbitrary URL
// client. The supplied HTTP transport must trust the target's actual CA.
func NewTrainedArtifactCopier(source *Verifier, target *http.Client, origin, bucket string) (*TrainedArtifactCopier, error) {
	u, err := url.Parse(origin)
	if source == nil || source.client == nil || target == nil || target.Timeout <= 0 || target.Timeout > 24*time.Hour ||
		err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || !bucketPattern.MatchString(bucket) {
		return nil, biz.ErrModelDependencyUnavailable
	}
	// Keep caller transport/CA and finite timeout, but always prohibit redirects.
	client := *target
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &TrainedArtifactCopier{source: source, target: &client, origin: u.Scheme + "://" + u.Host, bucket: bucket}, nil
}

func (c *TrainedArtifactCopier) CopyTrainedArtifact(ctx context.Context, execution biz.Execution, object cpup01.FixedObjectRef, grant biz.TrainedArtifactUploadGrant) error {
	// Never return transport errors containing the signed URL or headers.
	failed := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return biz.ErrModelDependencyUnavailable
	}
	if c == nil || c.source == nil || ctx == nil || execution.Snapshot.Finetuning == nil ||
		grant.Session.RegistrationID == "" || !biz.ValidStorageKey(grant.Session.StoragePath) ||
		!strings.HasPrefix(grant.Session.StoragePath, execution.TenantID+"/") || !grant.ExpiresAt.After(time.Now()) {
		return biz.ErrRuntimeConflict
	}
	u, err := url.Parse(grant.URL)
	if err != nil || u.Scheme+"://"+u.Host != c.origin || u.User != nil || u.Fragment != "" || u.RawPath != "" ||
		u.Path != "/"+c.bucket+"/"+grant.Session.StoragePath || u.RawQuery == "" {
		return biz.ErrRuntimeConflict
	}
	// Read the original immutable publication completely before any PUT. Neither
	// local training files nor a mutable latest object are a source of handover.
	if _, err := c.source.Verify(ctx, execution.Snapshot.PublicationScope, object); err != nil {
		return err
	}
	response, err := c.source.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(object.Bucket), Key: aws.String(object.Key), VersionId: object.VersionID})
	if err != nil || response == nil || response.Body == nil {
		return failed()
	}
	defer response.Body.Close()
	if response.VersionId == nil || *response.VersionId != *object.VersionID || response.ContentLength == nil || *response.ContentLength != object.SizeBytes {
		return biz.ErrObjectVerification
	}
	digest := sha256.New()
	body := &measuredReader{reader: io.TeeReader(io.LimitReader(response.Body, object.SizeBytes), digest)}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, grant.URL, body)
	if err != nil {
		return failed()
	}
	request.ContentLength = object.SizeBytes
	for name, value := range grant.Headers {
		// Authentication comes exclusively from the provider ticket. Incoming
		// bearer credentials and controller's S3 credentials never cross scopes.
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Host") || strings.ContainsAny(name+value, "\r\n") {
			return biz.ErrRuntimeConflict
		}
		request.Header.Set(name, value)
	}
	result, err := c.target.Do(request)
	if err != nil {
		return failed()
	}
	defer result.Body.Close()
	_, readErr := io.Copy(io.Discard, io.LimitReader(result.Body, 4096))
	if result.StatusCode < 200 || result.StatusCode >= 300 || readErr != nil || body.readErr != nil || body.size != object.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != object.SHA256 {
		return failed()
	}
	var extra [1]byte
	n, err := response.Body.Read(extra[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		return biz.ErrObjectVerification
	}
	// This is only a successful byte transfer. READY still requires Model's
	// independent original-key byte verification and Complete read-back.
	return nil
}
