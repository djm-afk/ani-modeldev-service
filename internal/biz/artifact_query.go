package biz

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

var (
	ErrArtifactNotFound    = errors.New("ARTIFACT_NOT_FOUND")
	ErrDownloadUnavailable = errors.New("DOWNLOAD_UNAVAILABLE")
)

type QueryRecord struct {
	Execution Execution
	Runtime   ExecutionRuntime
}

type ArtifactQueryRepository interface {
	GetQueryRecord(context.Context, string, string) (QueryRecord, error)
	ListQueryRecords(context.Context, string, ExecutionQuery) ([]QueryRecord, error)
	CountQueryRecords(context.Context, string, ExecutionQuery) (uint64, error)
	FindPublishedArtifact(context.Context, string, string) (QueryRecord, PublishedRuntimeFile, error)
}

type ExecutionQuery struct {
	Offset   int
	Limit    int
	Compute  ComputeState
	Delivery DeliveryState
	Close    CloseState
	// Name is an already lower-cased substring. The admitted name lives in the
	// immutable intent, so matching here reads the same value the list reports
	// instead of a duplicated column that could drift from it.
	Name string
	// Kind is the frozen snapshot kind ("GENERAL_TRAINING" / "FINETUNING").
	// An empty value means "no filter", which the caller distinguishes from a
	// requested kind before reaching here.
	Kind string
}

func (query ExecutionQuery) Matches(execution Execution) bool {
	return (query.Compute == "" || query.Compute == execution.States.Compute) &&
		(query.Delivery == "" || query.Delivery == execution.States.Delivery) &&
		(query.Close == "" || query.Close == execution.States.Close) &&
		(query.Kind == "" || query.Kind == execution.Snapshot.Kind) &&
		(query.Name == "" || strings.Contains(strings.ToLower(execution.Intent.Name), query.Name))
}

type ArtifactSigner interface {
	SignDownload(context.Context, cpup01.StorageScope, cpup01.FixedObjectRef) (DownloadGrant, error)
}

// MetricsObjectReader reads one already published object version so a query can
// derive its metrics. It is a read-only observation channel: the owner supplies
// the client and connection, and the caller cannot select an endpoint or
// credential. A read failure never invalidates the execution.
type MetricsObjectReader interface {
	ReadObject(context.Context, cpup01.StorageScope, cpup01.FixedObjectRef, int64) ([]byte, error)
}

type DownloadGrant struct {
	URL       string
	ExpiresAt time.Time
}
