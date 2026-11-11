package service

import (
	"context"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The receiver registers every Dataset method, writes included, as a current
// per-request query authorization: it verifies mTLS, the exact authorized
// method and tenant-all scope before installing that access. A write handler
// that looked for a separate command delivery would therefore reject every call
// as unauthenticated. This test walks the decision path that runs before any
// use case, so a nil collection set is enough to prove identity is resolved.
func TestDatasetMethodsRequireTheExactVerifiedMethodContext(t *testing.T) {
	d := NewDataset(nil, nil, nil, nil)
	scope := GovernanceDelivery{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42", RequestID: "11111111-1111-4111-8111-111111111111"}

	writes := []struct {
		method string
		call   func(context.Context) error
	}{
		{modeldevv1.ModelDevDatasetService_CreateDataset_FullMethodName, func(ctx context.Context) error {
			_, err := d.CreateDataset(ctx, &modeldevv1.CreateDatasetRequest{IdempotencyKey: "create-1", Name: "set"})
			return err
		}},
		{modeldevv1.ModelDevDatasetService_UpdateDataset_FullMethodName, func(ctx context.Context) error {
			_, err := d.UpdateDataset(ctx, &modeldevv1.UpdateDatasetRequest{DatasetId: scope.RequestID, IdempotencyKey: "update-1", ExpectedRevision: 1})
			return err
		}},
		{modeldevv1.ModelDevDatasetService_ArchiveDataset_FullMethodName, func(ctx context.Context) error {
			_, err := d.ArchiveDataset(ctx, &modeldevv1.ArchiveDatasetRequest{DatasetId: scope.RequestID, IdempotencyKey: "archive-1", ExpectedRevision: 1})
			return err
		}},
		{modeldevv1.ModelDevDatasetService_RestoreDataset_FullMethodName, func(ctx context.Context) error {
			_, err := d.RestoreDataset(ctx, &modeldevv1.RestoreDatasetRequest{DatasetId: scope.RequestID, IdempotencyKey: "restore-1", ExpectedRevision: 1})
			return err
		}},
		{modeldevv1.ModelDevDatasetService_ImportDatasetVersion_FullMethodName, func(ctx context.Context) error {
			_, err := d.ImportDatasetVersion(ctx, &modeldevv1.ImportDatasetVersionRequest{DatasetId: scope.RequestID, IdempotencyKey: "import-1", SelectionFingerprint: "x"})
			return err
		}},
		{modeldevv1.ModelDevDatasetService_CancelDatasetImport_FullMethodName, func(ctx context.Context) error {
			_, err := d.CancelDatasetImport(ctx, &modeldevv1.CancelDatasetImportRequest{ImportTaskId: scope.RequestID, IdempotencyKey: "cancel-1"})
			return err
		}},
		{modeldevv1.ModelDevDatasetService_RetryDatasetImport_FullMethodName, func(ctx context.Context) error {
			_, err := d.RetryDatasetImport(ctx, &modeldevv1.RetryDatasetImportRequest{ImportTaskId: scope.RequestID, IdempotencyKey: "retry-1"})
			return err
		}},
	}
	for _, write := range writes {
		// No context at all is unauthenticated rather than a nil dereference.
		if err := write.call(context.Background()); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("%s accepted a context without verified access: %v", write.method, err)
		}
		// A verified context for a different method must not authorize this one.
		wrong := WithVerifiedGovernanceQuery(context.Background(), scope, modeldevv1.ModelDevDatasetService_ListDatasets_FullMethodName)
		if err := write.call(wrong); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("%s accepted another method's verified access: %v", write.method, err)
		}
		// The exact verified method passes the identity gate and reaches the use
		// case, which is unavailable here because no collection set was wired.
		if err := write.call(WithVerifiedGovernanceQuery(context.Background(), scope, write.method)); status.Code(err) == codes.Unauthenticated {
			t.Fatalf("%s did not accept its own verified access: %v", write.method, err)
		}
	}
}
