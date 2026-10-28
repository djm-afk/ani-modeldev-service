package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProductionInputQueryUsesCurrentDelegation(t *testing.T) {
	fixture := prepareConfiguredAdmission(t)
	connection, _ := startConfiguredAdmissionApp(t, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := modeldevv1.NewModelDevQueryServiceClient(connection)
	rpc := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", fixture.tenantID, "x-ani-actor", "governance:user:42", "x-ani-request-id", uuid.NewString(), "x-ani-data-scope", "tenant-all", "x-ani-authorized-method", modeldevv1.ModelDevQueryService_GetInputVersion_FullMethodName))
	input, err := client.GetInputVersion(rpc, &modeldevv1.GetInputVersionRequest{InputVersionId: fixture.ready.Import.InputVersionID})
	if err != nil || input.GetInputVersion().GetState() != modeldevv1.InputState_INPUT_STATE_READY {
		t.Fatalf("PRODUCTION_INPUT_QUERY: durable READY unavailable through current delegated query: %v", err)
	}
	if _, err := client.GetInputVersion(ctx, &modeldevv1.GetInputVersionRequest{InputVersionId: fixture.ready.Import.InputVersionID}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthorized query = %v", err)
	}
	other := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "governance:user:42", "x-ani-request-id", uuid.NewString(), "x-ani-data-scope", "tenant-all", "x-ani-authorized-method", modeldevv1.ModelDevQueryService_GetInputVersion_FullMethodName))
	if _, err := client.GetInputVersion(other, &modeldevv1.GetInputVersionRequest{InputVersionId: fixture.ready.Import.InputVersionID}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant query = %v", err)
	}
	implementations := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", fixture.tenantID, "x-ani-actor", "governance:user:42", "x-ani-request-id", uuid.NewString(), "x-ani-data-scope", "tenant-all", "x-ani-authorized-method", modeldevv1.ModelDevQueryService_ListImplementations_FullMethodName))
	mode := "cpu"
	list, err := client.ListImplementations(implementations, &modeldevv1.ListImplementationsRequest{DeviceMode: &mode})
	if err != nil || len(list.GetImplementations()) != 2 || list.Implementations[0].ImplementationId != "builtin.mlp" || !list.Implementations[0].Available || len(list.Implementations[0].ParameterRules) != 4 || list.Implementations[1].Available {
		t.Fatalf("installed software availability and typed rules projection diverged: %v", err)
	}
	management := modeldevv1.NewModelDevManagementServiceClient(connection)
	importScope := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", fixture.tenantID, "x-ani-actor", "governance:user:42", "x-ani-request-id", uuid.NewString(), "x-ani-data-scope", "tenant-all", "x-ani-authorized-method", modeldevv1.ModelDevManagementService_ImportCSV_FullMethodName))
	original := fixture.ready.Import
	request := &modeldevv1.ImportCSVRequest{ImplementationId: "builtin.sft_lora", InputVersionId: original.InputVersionID, RequestedAt: timestamppb.New(original.RequestedAt), Object: &trainingv1.FixedObjectRef{StorageConnectionId: original.Object.StorageConnectionID, Bucket: original.Object.Bucket, Key: original.Object.Key, SizeBytes: original.Object.SizeBytes, Sha256: original.Object.SHA256, Immutability: &trainingv1.FixedObjectRef_VersionId{VersionId: *original.Object.VersionID}}}
	if _, err := management.ImportCSV(importScope, request); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("wrong implementation accepted CSV contract: %v", err)
	}
	if _, err := management.ImportCSV(rpc, request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("query delegation authorized management: %v", err)
	}
}
