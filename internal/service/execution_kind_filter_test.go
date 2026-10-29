package service

import (
	"context"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type kindFilterQueryRepository struct{ seen biz.ExecutionQuery }

func (kindFilterQueryRepository) GetQueryRecord(context.Context, string, string) (biz.QueryRecord, error) {
	return biz.QueryRecord{}, biz.ErrExecutionNotFound
}

func (r *kindFilterQueryRepository) ListQueryRecords(_ context.Context, _ string, filter biz.ExecutionQuery) ([]biz.QueryRecord, error) {
	r.seen = filter
	return nil, nil
}

func (*kindFilterQueryRepository) CountQueryRecords(context.Context, string, biz.ExecutionQuery) (uint64, error) {
	return 0, nil
}

func (kindFilterQueryRepository) FindPublishedArtifact(context.Context, string, string) (biz.QueryRecord, biz.PublishedRuntimeFile, error) {
	return biz.QueryRecord{}, biz.PublishedRuntimeFile{}, biz.ErrArtifactNotFound
}

func kindFilterContext() context.Context {
	return WithVerifiedGovernanceQuery(context.Background(), GovernanceDelivery{TenantID: "9086ce3b-bd19-47c3-8467-190607f633db", RequestID: "kind-filter-test"}, modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName)
}

// An absent kind keeps the list unfiltered; a specified kind reaches the
// repository as the frozen snapshot vocabulary. UNSPECIFIED is the enum
// placeholder and must be rejected instead of widening the page.
func TestListExecutionsKindFilter(t *testing.T) {
	finetuning := trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING
	repository := &kindFilterQueryRepository{}
	if _, err := NewQuery(repository, nil).ListExecutions(kindFilterContext(), &modeldevv1.ListExecutionsRequest{}); err != nil {
		t.Fatalf("absent kind: %v", err)
	}
	if repository.seen.Kind != "" {
		t.Fatalf("absent kind must not filter, got %q", repository.seen.Kind)
	}
	if _, err := NewQuery(repository, nil).ListExecutions(kindFilterContext(), &modeldevv1.ListExecutionsRequest{Kind: &finetuning}); err != nil {
		t.Fatalf("finetuning kind: %v", err)
	}
	if repository.seen.Kind != "FINETUNING" {
		t.Fatalf("kind filter = %q, want FINETUNING", repository.seen.Kind)
	}
	unspecified := trainingv1.ExecutionKind_EXECUTION_KIND_UNSPECIFIED
	if _, err := NewQuery(repository, nil).ListExecutions(kindFilterContext(), &modeldevv1.ListExecutionsRequest{Kind: &unspecified}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UNSPECIFIED kind error = %v, want InvalidArgument", err)
	}
}
