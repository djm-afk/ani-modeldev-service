package commandtest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGovernanceAcceptRejectsInvalidWireBeforePersistence(t *testing.T) {
	openPool := postgres.Prepare(t)
	client, _ := startCommandServer(t, execution.New(openPool()), commandtls.New(t))
	reader := execution.New(openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	controlTarget, _ := validAcceptCommand(t)
	proveAcceptBoundaryControl(t, ctx, client, reader, controlTarget)
	for _, test := range []struct {
		name   string
		change func(*modeldevv1.AcceptExecutionRequest)
	}{
		{"missing identity", func(r *modeldevv1.AcceptExecutionRequest) { r.Identity = nil }},
		{"missing intent", func(r *modeldevv1.AcceptExecutionRequest) { r.Intent = nil }},
		{"missing snapshot", func(r *modeldevv1.AcceptExecutionRequest) { r.Snapshot = nil }},
		{"missing accepted time", func(r *modeldevv1.AcceptExecutionRequest) { r.AcceptedAt = nil }},
		{"missing nested Runtime", func(r *modeldevv1.AcceptExecutionRequest) { r.Snapshot.RuntimePlan = nil }},
		{"missing object immutability", func(r *modeldevv1.AcceptExecutionRequest) { r.Snapshot.Input.Object.Immutability = nil }},
		{"unknown outer field", func(r *modeldevv1.AcceptExecutionRequest) { r.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01}) }},
		{"unknown identity field", func(r *modeldevv1.AcceptExecutionRequest) {
			r.Identity.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}},
		{"unknown accepted time field", func(r *modeldevv1.AcceptExecutionRequest) {
			r.AcceptedAt.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}},
		{"unknown intent field", func(r *modeldevv1.AcceptExecutionRequest) {
			r.Intent.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}},
		{"unknown nested identity refs", func(r *modeldevv1.AcceptExecutionRequest) {
			r.Snapshot.Environment.Identities.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}},
		{"unknown nested output enum", func(r *modeldevv1.AcceptExecutionRequest) {
			r.Snapshot.OutputContract.RequiredFiles[0].Role = trainingv1.FileRole(999)
		}},
		{"malformed intent hash", func(r *modeldevv1.AcceptExecutionRequest) { r.IntentHash = "untrusted-intent-hash" }},
		{"mismatched intent hash", func(r *modeldevv1.AcceptExecutionRequest) { r.IntentHash = strings.Repeat("0", 64) }},
		{"malformed spec hash", func(r *modeldevv1.AcceptExecutionRequest) { r.Identity.ExecutionSpecHash = "untrusted-spec-hash" }},
		{"mismatched spec hash", func(r *modeldevv1.AcceptExecutionRequest) { r.Identity.ExecutionSpecHash = strings.Repeat("0", 64) }},
		{"accepted time outside protobuf range", func(r *modeldevv1.AcceptExecutionRequest) { r.AcceptedAt.Seconds = 253402300800 }},
		{"accepted time invalid nanos", func(r *modeldevv1.AcceptExecutionRequest) { r.AcceptedAt.Nanos = -1 }},
		{"accepted time is zero", func(r *modeldevv1.AcceptExecutionRequest) { r.AcceptedAt = timestamppb.New(time.Time{}) }},
		{"accepted time loses microsecond precision", func(r *modeldevv1.AcceptExecutionRequest) { r.AcceptedAt.Nanos++ }},
		{"nested deadline loses microsecond precision", func(r *modeldevv1.AcceptExecutionRequest) { r.Snapshot.DeadlineAt.Nanos++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			original, _ := validAcceptCommand(t)
			body := proto.Clone(original).(*modeldevv1.AcceptExecutionRequest)
			test.change(body)
			delivery := acceptCommandContext(ctx, original)
			response, err := client.AcceptExecution(delivery, body)
			md, _ := metadata.FromOutgoingContext(delivery)
			assertAcceptBoundaryError(t, response, err, codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, md.Get("x-ani-request-id")[0])
			assertAcceptBoundaryAbsent(t, ctx, reader, original)
		})
	}
}

func TestGovernanceAcceptBodyCannotReplaceVerifiedTenantOrActor(t *testing.T) {
	openPool := postgres.Prepare(t)
	client, _ := startCommandServer(t, execution.New(openPool()), commandtls.New(t))
	reader := execution.New(openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	controlTarget, _ := validAcceptCommand(t)
	proveAcceptBoundaryControl(t, ctx, client, reader, controlTarget)
	for _, field := range []string{"tenant", "actor"} {
		t.Run(field, func(t *testing.T) {
			original, _ := validAcceptCommand(t)
			body := proto.Clone(original).(*modeldevv1.AcceptExecutionRequest)
			if field == "tenant" {
				body.ResourceTenantId = uuid.NewString()
			} else {
				body.AdmittedActorId = "governance:user:43"
			}
			// The body remains a valid immutable command: only its authority
			// differs. InvalidArgument or Unimplemented cannot satisfy this test.
			freezeAcceptBoundaryBody(t, body)
			requireValidAcceptBoundaryBody(t, body)
			delivery := acceptCommandContext(ctx, original)
			response, err := client.AcceptExecution(delivery, body)
			md, _ := metadata.FromOutgoingContext(delivery)
			assertAcceptBoundaryError(t, response, err, codes.PermissionDenied, modeldevv1.ErrorReason_ERROR_REASON_FORBIDDEN, md.Get("x-ani-request-id")[0])
			assertAcceptBoundaryAbsent(t, ctx, reader, original)
			assertAcceptBoundaryAbsent(t, ctx, reader, body)
		})
	}
}

func TestGovernanceAcceptConflictsRetainTheOriginalAdmission(t *testing.T) {
	openPool := postgres.Prepare(t)
	writer := execution.New(openPool())
	client, _ := startCommandServer(t, writer, commandtls.New(t))
	reader := execution.New(openPool())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, dispatch := validAcceptCommand(t)
	proveAcceptBoundaryControl(t, ctx, client, reader, request)
	// The existing public repository provides the original before testing
	// the RPC conflict. A missing Accept RPC cannot masquerade as bad setup.
	accepted, err := writer.Accept(ctx, dispatch.Admission)
	if err != nil || accepted.Replayed || accepted.Close != nil {
		t.Fatalf("CPU_ACCEPT_BOUNDARY_PREFLIGHT: original repository Admission failed: %v; behavior NOT_RUN", err)
	}
	stored, err := reader.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_BOUNDARY_PREFLIGHT: original Admission not independently visible: %v; behavior NOT_RUN", err)
	}
	assertAcceptedOriginal(t, stored, dispatch.Admission, 1)
	for _, field := range []string{"actor", "accepted time", "intent with matching digest"} {
		t.Run(field, func(t *testing.T) {
			body := proto.Clone(request).(*modeldevv1.AcceptExecutionRequest)
			switch field {
			case "actor":
				body.AdmittedActorId = "governance:user:43"
			case "accepted time":
				body.AcceptedAt = timestamppb.New(body.AcceptedAt.AsTime().Add(time.Microsecond))
			case "intent with matching digest":
				body.Intent.Name = "another-valid-intent"
				intent, decodeErr := contractpb.DecodeIntent(body.Intent)
				if decodeErr != nil {
					t.Fatal("CPU_ACCEPT_BOUNDARY_PREFLIGHT: conflict intent fixture invalid; behavior NOT_RUN")
				}
				_, digest, digestErr := cpup01.CanonicalIntent(intent)
				if digestErr != nil {
					t.Fatal("CPU_ACCEPT_BOUNDARY_PREFLIGHT: conflict intent digest unavailable; behavior NOT_RUN")
				}
				body.IntentHash = digest
			}
			freezeAcceptBoundaryBody(t, body)
			requireValidAcceptBoundaryBody(t, body)
			delivery := acceptCommandContext(ctx, body)
			response, err := client.AcceptExecution(delivery, body)
			md, _ := metadata.FromOutgoingContext(delivery)
			assertAcceptBoundaryError(t, response, err, codes.AlreadyExists, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, md.Get("x-ani-request-id")[0])
			after, err := reader.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
			if err != nil || !reflect.DeepEqual(after, stored) {
				t.Fatalf("conflicting RPC changed the complete original Admission or revision: %v", err)
			}
		})
	}
}

func TestGovernanceAcceptRequiresVerifiedContextAndNonNilBody(t *testing.T) {
	openPool := postgres.Prepare(t)
	repository := execution.New(openPool())
	client, _ := startCommandServer(t, repository, commandtls.New(t))
	reader := execution.New(openPool())
	handler := service.NewCommand(repository)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, _ := validAcceptCommand(t)
	proveAcceptBoundaryControl(t, ctx, client, reader, request)
	md, _ := metadata.FromOutgoingContext(acceptCommandContext(ctx, request))
	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{"no delivery context", ctx},
		{"raw incoming metadata", metadata.NewIncomingContext(ctx, md)},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := handler.AcceptExecution(test.ctx, request)
			assertAcceptBoundaryError(t, response, err, codes.Unauthenticated, modeldevv1.ErrorReason_ERROR_REASON_UNAUTHENTICATED, "")
			assertAcceptBoundaryAbsent(t, ctx, reader, request)
		})
	}
	// A nil Go pointer cannot be preserved over protobuf transport. Only this
	// nil-body case uses the trusted context that the real server supplies.
	correlation := uuid.NewString()
	verified := service.WithVerifiedGovernanceDelivery(ctx, service.GovernanceDelivery{TenantID: request.ResourceTenantId, Actor: request.AdmittedActorId, RequestID: correlation})
	response, err := handler.AcceptExecution(verified, nil)
	assertAcceptBoundaryError(t, response, err, codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, correlation)
	assertAcceptBoundaryAbsent(t, ctx, reader, request)
}

func proveAcceptBoundaryControl(t *testing.T, ctx context.Context, client modeldevv1.ModelDevCommandServiceClient, reader *execution.Repository, target *modeldevv1.AcceptExecutionRequest) {
	t.Helper()
	control := validCloseRequest()
	control.ResourceTenantId, control.RequestedActorId = target.ResourceTenantId, target.AdmittedActorId
	if control.Identity.ExecutionId == target.Identity.ExecutionId || control.Identity.OperationId == target.Identity.OperationId {
		t.Fatal("CPU_ACCEPT_BOUNDARY_PREFLIGHT: control must not touch target identity; behavior NOT_RUN")
	}
	response, err := client.ApplyCloseIntent(commandContext(ctx, control), control)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_BOUNDARY_PREFLIGHT: actual mTLS Close control failed: %s; behavior NOT_RUN", status.Code(err))
	}
	assertCloseResponse(t, response, control, false)
	stored, err := reader.GetCloseIntent(ctx, control.ResourceTenantId, control.Identity.ExecutionId)
	want := biz.CloseRecord{CloseIntent: biz.CloseIntent{
		TenantID: control.ResourceTenantId, ExecutionID: control.Identity.ExecutionId, OperationID: control.Identity.OperationId,
		SpecHash: control.Identity.ExecutionSpecHash, SourceGeneration: control.IntentGeneration, Reason: biz.CloseReasonUserStop,
		RequestedAt: control.RequestedAt.AsTime(), RequestedActor: control.RequestedActorId,
	}, Generation: 1, State: biz.CloseStateClosing}
	if err != nil || !reflect.DeepEqual(stored, want) {
		t.Fatalf("CPU_ACCEPT_BOUNDARY_PREFLIGHT: control lacks independent original PostgreSQL fact: %v; behavior NOT_RUN", err)
	}
	assertAcceptBoundaryAbsent(t, ctx, reader, target)
	t.Log("CPU_ACCEPT_BOUNDARY_PREFLIGHT PASS: real mTLS Close control and independent PostgreSQL original, target has no Admission or close")
}

func requireValidAcceptBoundaryBody(t *testing.T, request *modeldevv1.AcceptExecutionRequest) {
	t.Helper()
	intent, intentErr := contractpb.DecodeIntent(request.Intent)
	snapshot, snapshotErr := contractpb.DecodeSnapshot(request.Snapshot)
	admission := biz.Admission{
		TenantID: request.ResourceTenantId, Actor: request.AdmittedActorId, OperationID: request.Identity.OperationId,
		ExecutionID: request.Identity.ExecutionId, Intent: intent, IntentHash: request.IntentHash,
		Snapshot: snapshot, SpecHash: request.Identity.ExecutionSpecHash, AcceptedAt: request.AcceptedAt.AsTime(),
	}
	if _, _, err := admission.CanonicalPayloads(); intentErr != nil || snapshotErr != nil || err != nil {
		t.Fatal("CPU_ACCEPT_BOUNDARY_PREFLIGHT: denial fixture is not a valid frozen command; behavior NOT_RUN")
	}
}

func assertAcceptBoundaryAbsent(t *testing.T, ctx context.Context, reader *execution.Repository, request *modeldevv1.AcceptExecutionRequest) {
	t.Helper()
	got, err := reader.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if !errors.Is(err, biz.ErrExecutionNotFound) || !reflect.DeepEqual(got, biz.Execution{}) {
		t.Fatalf("rejected Accept left a target Admission or failed independent read: %v", err)
	}
	closed, err := reader.GetCloseIntent(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if !errors.Is(err, biz.ErrExecutionNotFound) || !reflect.DeepEqual(closed, biz.CloseRecord{}) {
		t.Fatalf("rejected Accept left a target close or failed independent read: %v", err)
	}
}

func assertAcceptBoundaryError(t *testing.T, response *modeldevv1.AcceptExecutionResponse, err error, code codes.Code, reason modeldevv1.ErrorReason, correlation string) {
	t.Helper()
	if response != nil || status.Code(err) != code {
		t.Errorf("Accept boundary returned code=%s ACK=%t, want code=%s without ACK", status.Code(err), response != nil, code)
	}
	if err == nil {
		return
	}
	failure := status.Convert(err)
	details := failure.Details()
	if len(details) != 1 {
		t.Error("Accept boundary must return one typed error detail")
		return
	}
	detail, ok := details[0].(*modeldevv1.ErrorDetail)
	if !ok || detail.Reason != reason || detail.CorrelationId != correlation || detail.SafeMessage == "" || detail.SafeMessage != failure.Message() {
		t.Error("Accept boundary lost its exact reason, verified correlation or safe message")
	}
}

func freezeAcceptBoundaryBody(t *testing.T, r *modeldevv1.AcceptExecutionRequest) {
	t.Helper()
	intent, err := contractpb.DecodeIntent(r.Intent)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := contractpb.DecodeSnapshot(r.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	a := admissionfixture.Bind(t, biz.Admission{TenantID: r.ResourceTenantId, Actor: r.AdmittedActorId, OperationID: r.Identity.OperationId, ExecutionID: r.Identity.ExecutionId, Intent: intent, Snapshot: snapshot, AcceptedAt: r.AcceptedAt.AsTime()})
	r.Snapshot, err = contractpb.EncodeSnapshot(a.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	r.Identity.ExecutionSpecHash, r.IntentHash = a.SpecHash, a.IntentHash
}
