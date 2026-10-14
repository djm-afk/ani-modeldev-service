package service

import (
	"context"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (m *Management) ImportFinetuningDataset(ctx context.Context, r *modeldevv1.ImportFinetuningDatasetRequest) (*modeldevv1.ImportFinetuningDatasetResponse, error) {
	scope, err := queryScope(ctx, modeldevv1.ModelDevManagementService_ImportFinetuningDataset_FullMethodName)
	if err != nil {
		return nil, err
	}
	if r == nil || !validMaterialMessage(r) || r.Format != "JSON" || r.Object == nil || r.RequestedAt == nil || r.RequestedAt.CheckValid() != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid JSON dataset import")
	}
	version, ok := r.Object.Immutability.(*trainingv1.FixedObjectRef_VersionId)
	if !ok || version == nil {
		return nil, status.Error(codes.InvalidArgument, "fixed object version required")
	}
	object := cpup01.FixedObjectRef{StorageConnectionID: r.Object.StorageConnectionId, Bucket: r.Object.Bucket, Key: r.Object.Key, VersionID: &version.VersionId, SizeBytes: r.Object.SizeBytes, SHA256: r.Object.Sha256}
	value, err := m.materials.ImportFinetuningDataset(ctx, biz.InputImport{TenantID: scope.TenantID, RequestID: scope.RequestID, Actor: scope.Actor, InputVersionID: r.InputVersionId, RequestedAt: r.RequestedAt.AsTime(), Object: object, Format: "JSON"}, r.ReleaseId, r.ReleaseDigest)
	if err != nil {
		return nil, materialsError(err, scope.RequestID)
	}
	return &modeldevv1.ImportFinetuningDatasetResponse{InputVersion: inputVersionView(value)}, nil
}
