package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var (
	ErrSizeMismatch = errors.New("SIZE_MISMATCH")
	ErrSHAMismatch  = errors.New("SHA_MISMATCH")
)

// SourceFetcher streams the exact file a task pinned. Its Stream must be closed
// by the caller; the implementation is responsible for binding the request to
// the resolved revision and exact path.
type SourceFetcher interface {
	Stream(ctx context.Context, plan ImportPlan) (io.ReadCloser, error)
}

// ObjectUploader puts bytes at a preallocated key and returns the real version.
type ObjectUploader interface {
	Put(ctx context.Context, target PlanObject, body io.Reader, size int64) (versionID, sha256Hex string, err error)
}

// ImportOutcome is the verified result of one attempt.
type ImportOutcome struct {
	Object  biz.ImportResultObject
	Failure biz.ImportFailureCode
	Stage   biz.ImportFailureStage
}

// RunImport performs the pinned download and upload. Verification happens while
// streaming: the file is never fully buffered in memory, and the digest is over
// exactly the bytes that were uploaded.
func RunImport(ctx context.Context, plan ImportPlan, fetcher SourceFetcher, uploader ObjectUploader, workDir string) ImportOutcome {
	if fetcher == nil || uploader == nil {
		return ImportOutcome{Failure: biz.FailDependencyUnavailable, Stage: biz.StageReport}
	}
	source, err := fetcher.Stream(ctx, plan)
	if err != nil {
		return ImportOutcome{Failure: classifySourceError(err), Stage: biz.StageDownload}
	}
	defer source.Close()

	// Stage to a local file so the upload can be retried and the size checked
	// before any bytes reach object storage.
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return ImportOutcome{Failure: biz.FailResourceUnavailable, Stage: biz.StageDownload}
	}
	staged := filepath.Join(workDir, "download.json")
	// O_RDWR, not O_WRONLY: the same descriptor is rewound and re-read for the
	// upload, and a write-only handle returns nothing on read.
	file, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return ImportOutcome{Failure: biz.FailResourceUnavailable, Stage: biz.StageDownload}
	}
	defer func() {
		file.Close()
		os.Remove(staged)
	}()

	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(source, plan.ExpectedSizeBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ImportOutcome{Failure: biz.FailTimeout, Stage: biz.StageDownload}
		}
		return ImportOutcome{Failure: classifySourceError(err), Stage: biz.StageDownload}
	}
	if written != plan.ExpectedSizeBytes {
		// An over-limit or short stream is a changed source, not a disk problem.
		return ImportOutcome{Failure: biz.FailSourceChanged, Stage: biz.StageDownload}
	}
	sum := hex.EncodeToString(digest.Sum(nil))
	// A full-file SHA is only compared when the task actually has one; an
	// immutable-revision lock does not pretend to know it.
	if plan.ExpectedFileSHA256 != "" && sum != plan.ExpectedFileSHA256 {
		return ImportOutcome{Failure: biz.FailSourceChanged, Stage: biz.StageDownload}
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ImportOutcome{Failure: biz.FailResourceUnavailable, Stage: biz.StageUpload}
	}
	versionID, uploadedSum, err := uploader.Put(ctx, plan.Data, io.LimitReader(file, plan.ExpectedSizeBytes), plan.ExpectedSizeBytes)
	if err != nil {
		if ctx.Err() != nil {
			return ImportOutcome{Failure: biz.FailTimeout, Stage: biz.StageUpload}
		}
		return ImportOutcome{Failure: classifyUploadError(err), Stage: biz.StageUpload}
	}
	if versionID == "" || uploadedSum != sum {
		// The uploader must return the real version and the digest it stored.
		return ImportOutcome{Failure: biz.FailDependencyUnavailable, Stage: biz.StageUpload}
	}
	return ImportOutcome{Object: biz.ImportResultObject{
		StorageConnectionID: plan.Data.StorageConnectionID,
		Bucket:              plan.Data.Bucket,
		Key:                 plan.Data.Key,
		VersionID:           versionID,
		SizeBytes:           plan.ExpectedSizeBytes,
		SHA256:              sum,
	}}
}

func classifySourceError(err error) biz.ImportFailureCode {
	switch {
	case errors.Is(err, biz.ErrSourceNotFound):
		return biz.FailSourceNotFound
	case errors.Is(err, biz.ErrSourceAuthRequired):
		return biz.FailSourceAuthRequired
	case errors.Is(err, biz.ErrSourceChanged):
		return biz.FailSourceChanged
	default:
		return biz.FailDependencyUnavailable
	}
}

func classifyUploadError(err error) biz.ImportFailureCode {
	if isVersioningRequired(err) {
		return biz.FailStorageVersioningRequired
	}
	// An expired session is a deadline problem, not a store outage: the attempt
	// ran past the window its credentials covered.
	if errors.Is(err, errCredentialsExpired) {
		return biz.FailTimeout
	}
	return biz.FailDependencyUnavailable
}

// S3Uploader writes to the preallocated key and reports the real version.
type S3Uploader struct {
	client *s3.Client
}

func (uploader *S3Uploader) Put(ctx context.Context, target PlanObject, body io.Reader, size int64) (string, string, error) {
	if uploader == nil || uploader.client == nil || size <= 0 {
		return "", "", errors.New("importer uploader is not configured")
	}
	// The bucket's versioning is a deployment fact, not something this Pod can
	// query: the attempt session carries object-level rights only, and the parent
	// identity is denied GetBucketVersioning as well (verified in-cluster). What
	// is provable here is the PutObject result — an unversioned bucket returns no
	// VersionId, which is rejected below.
	digest := sha256.New()
	// No IfNoneMatch: the key is a preallocated per-attempt path, so a second
	// writer would be a control-plane bug, and the conditional-write header is
	// rejected by this store's STS session policy.
	response, err := uploader.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(target.Bucket), Key: aws.String(target.Key),
		Body: io.TeeReader(body, digest), ContentLength: aws.Int64(size),
	})
	if err != nil {
		return "", "", err
	}
	if response.VersionId == nil || *response.VersionId == "" {
		return "", "", errVersioningRequired{}
	}
	return *response.VersionId, hex.EncodeToString(digest.Sum(nil)), nil
}

type errVersioningRequired struct{}

func (errVersioningRequired) Error() string { return "bucket versioning required" }

func isVersioningRequired(err error) bool {
	var target errVersioningRequired
	return errors.As(err, &target)
}

// CompletedAt is the microsecond UTC instant the report records. It is audit
// only and never authorizes anything.
func CompletedAt(now time.Time) time.Time {
	return now.UTC().Truncate(time.Microsecond)
}
