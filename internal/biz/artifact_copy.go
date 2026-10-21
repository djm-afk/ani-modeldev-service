package biz

import (
	"context"
	"errors"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// ErrUploadUnavailable means the controlled copy could not be started or
// completed. It is a real observation about the attempt and never proves that
// no bytes were written: only the provider receipt can settle that.
var ErrUploadUnavailable = errors.New("UPLOAD_UNAVAILABLE")

// UploadTicket is the provider-issued controlled-copy ticket. The URL, required
// headers and the storage target all come from Model; ModelDev only honours
// them and never selects an endpoint, bucket, key or version of its own.
type UploadTicket struct {
	URL         string
	StoragePath string
	Headers     map[string]string
	SizeBytes   int64
	SHA256      string
	ExpiresAt   time.Time
}

// UploadReceipt is the observed result of one controlled copy. It reports the
// provider ticket's storage target rather than a location ModelDev invents.
type UploadReceipt struct {
	StoragePath    string
	SizeBytes      int64
	ChecksumSHA256 string
}

// ArtifactCopyUploader copies one exact, already-verified published object
// version into a provider-issued ticket. It owns only byte transport: it never
// decides whether a copy is permitted, and it never retries by itself, because
// an unresolved attempt is a durable dispatch fact rather than a local retry.
type ArtifactCopyUploader interface {
	Upload(context.Context, cpup01.StorageScope, cpup01.FixedObjectRef, UploadTicket) (UploadReceipt, error)
}
