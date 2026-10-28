package resolutiontest

import (
	"context"
	"errors"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"google.golang.org/grpc/codes"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"reflect"
	"testing"
	"time"
)

const admissionRPCRequestID = "abcdefab-1234-4234-8234-abcdefabcdef"

func admissionRPCContext(ctx context.Context, tenant string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", "governance:user:42", "x-ani-request-id", admissionRPCRequestID))
}
func admissionRPCRequest(t *testing.T, f resolutionFixture) *modeldevv1.PrepareAdmissionRequest {
	t.Helper()
	intent, e := contractpb.EncodeIntent(f.request.Intent)
	if e != nil {
		t.Fatal(e)
	}
	id := f.request.IdentityRef
	return &modeldevv1.PrepareAdmissionRequest{Intent: intent, IdentityRef: &modeldevv1.IdentityRef{TenantId: id.TenantID, Actor: id.Actor, ExecutionId: id.ExecutionID, OperationId: id.OperationID, IntentHash: id.IntentHash}, AcceptedAt: timestamppb.New(f.request.AcceptedAt), ComputeSelectionCanonical: []byte(f.request.ComputeOrigin.SelectionJSON), SelectionDigest: f.request.ComputeOrigin.SelectionDigest}
}
func requireRPCError(t *testing.T, response any, err error, code codes.Code) {
	t.Helper()
	if response != nil && !reflect.ValueOf(response).IsNil() {
		t.Fatal("failed RPC returned success payload")
	}
	if status.Code(err) != code {
		t.Fatalf("RPC code %v, want %v: %v", status.Code(err), code, err)
	}
}
func TestPrepareRPCRejectsAmbiguousReservedAndInconsistentWireFields(t *testing.T) {
	f := prepareResolution(t)
	client := startAdmissionClient(t, f, f.resolver)
	for _, change := range []func(*modeldevv1.PrepareAdmissionRequest){
		func(r *modeldevv1.PrepareAdmissionRequest) { r.Intent = nil }, func(r *modeldevv1.PrepareAdmissionRequest) { r.IdentityRef = nil }, func(r *modeldevv1.PrepareAdmissionRequest) { r.AcceptedAt = nil }, func(r *modeldevv1.PrepareAdmissionRequest) { r.ProtoReflect().SetUnknown([]byte{0xa0, 0x1f, 1}) }, func(r *modeldevv1.PrepareAdmissionRequest) { r.IdentityRef.ProtoReflect().SetUnknown([]byte{0x30, 1}) }, func(r *modeldevv1.PrepareAdmissionRequest) { r.AcceptedAt.ProtoReflect().SetUnknown([]byte{0x18, 1}) }, func(r *modeldevv1.PrepareAdmissionRequest) {
			r.Intent.GetMlp().ProtoReflect().SetUnknown([]byte{0x28, 1})
		}, func(r *modeldevv1.PrepareAdmissionRequest) { r.Intent.ProtoReflect().SetUnknown([]byte{0x1a, 1, 'x'}) }, func(r *modeldevv1.PrepareAdmissionRequest) { r.AcceptedAt.Nanos = 1 }, func(r *modeldevv1.PrepareAdmissionRequest) { r.AcceptedAt.Seconds = 253402300800 }, func(r *modeldevv1.PrepareAdmissionRequest) { r.AcceptedAt = timestamppb.New(time.Time{}) }, func(r *modeldevv1.PrepareAdmissionRequest) { r.IdentityRef.IntentHash = "wrong" }, func(r *modeldevv1.PrepareAdmissionRequest) { r.IdentityRef.Actor = "governance:user:other" }, func(r *modeldevv1.PrepareAdmissionRequest) { r.SelectionDigest = "wrong" }, func(r *modeldevv1.PrepareAdmissionRequest) { r.Intent.Parameters = nil },
	} {
		request := proto.Clone(admissionRPCRequest(t, f)).(*modeldevv1.PrepareAdmissionRequest)
		change(request)
		response, e := client.PrepareAdmission(admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID), request)
		requireRPCError(t, response, e, codes.InvalidArgument)
	}
	requireNoResolvedExecution(t, f)
}
func TestFinalizeRPCKeepsCandidateHashScopeAndNoReads(t *testing.T) {
	f := prepareResolution(t)
	client := startAdmissionClient(t, f, f.resolver)
	prepared, e := client.PrepareAdmission(admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID), admissionRPCRequest(t, f))
	if e != nil {
		t.Fatal(e)
	}
	before := f.factReader.reads
	valid := &modeldevv1.FinalizeAdmissionRequest{CandidateCanonical: prepared.CandidateCanonical, CandidateHash: prepared.CandidateHash}
	for _, change := range []func(*modeldevv1.FinalizeAdmissionRequest){func(r *modeldevv1.FinalizeAdmissionRequest) { r.CandidateHash = "wrong" }, func(r *modeldevv1.FinalizeAdmissionRequest) { r.CandidateCanonical = append(r.CandidateCanonical, ' ') }, func(r *modeldevv1.FinalizeAdmissionRequest) { r.GpuExecutionCanonical = []byte(`{}`) }, func(r *modeldevv1.FinalizeAdmissionRequest) { r.ProtoReflect().SetUnknown([]byte{0x28, 1}) }} {
		r := proto.Clone(valid).(*modeldevv1.FinalizeAdmissionRequest)
		change(r)
		response, e := client.FinalizeAdmission(admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID), r)
		requireRPCError(t, response, e, codes.InvalidArgument)
	}
	response, e := client.FinalizeAdmission(admissionRPCContext(f.ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), valid)
	requireRPCError(t, response, e, codes.InvalidArgument)
	if f.factReader.reads != before {
		t.Fatal("Finalize/refusal reread facts")
	}
	requireNoResolvedExecution(t, f)
}
func TestAdmissionRPCUntrustedMetadataAndListenerCapabilityBoundaries(t *testing.T) {
	f := prepareResolution(t)
	client := startAdmissionClient(t, f, f.resolver)
	for _, change := range []func(metadata.MD){func(m metadata.MD) { m.Delete("x-ani-tenant-id") }, func(m metadata.MD) { m.Append("x-ani-tenant-id", f.request.IdentityRef.TenantID) }, func(m metadata.MD) { m.Set("x-ani-tenant-id", "invalid") }, func(m metadata.MD) { m.Delete("x-ani-actor") }, func(m metadata.MD) { m.Append("x-ani-actor", "governance:user:other") }, func(m metadata.MD) { m.Set("x-ani-actor", "root") }, func(m metadata.MD) { m.Delete("x-ani-request-id") }, func(m metadata.MD) { m.Set("x-ani-request-id", "invalid") }} {
		ctx := admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID)
		md, _ := metadata.FromOutgoingContext(ctx)
		change(md)
		before := f.factReader.reads
		response, e := client.PrepareAdmission(metadata.NewOutgoingContext(f.ctx, md), admissionRPCRequest(t, f))
		if response != nil || status.Code(e) != codes.InvalidArgument && status.Code(e) != codes.Unauthenticated && status.Code(e) != codes.PermissionDenied {
			t.Fatal("untrusted metadata reached prepare", e)
		}
		if f.factReader.reads != before {
			t.Fatal("untrusted metadata read facts")
		}
	}
	connection := startAdmissionConnection(t, f, nil)
	response, e := modeldevv1.NewModelDevAdmissionServiceClient(connection).PrepareAdmission(admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID), admissionRPCRequest(t, f))
	requireRPCError(t, response, e, codes.Unimplemented)
	connection = startAdmissionConnection(t, f, service.NewAdmission(f.resolver))
	ctx := admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID)
	if _, e := healthv1.NewHealthClient(connection).Check(ctx, &healthv1.HealthCheckRequest{}); status.Code(e) != codes.PermissionDenied {
		t.Fatal("admission listener broadened health capability", e)
	}
	for _, method := range []string{modeldevv1.ModelDevQueryService_GetExecution_FullMethodName, modeldevv1.ModelDevStepService_BeginExecution_FullMethodName} {
		if e := connection.Invoke(ctx, method, &emptypb.Empty{}, &emptypb.Empty{}); status.Code(e) != codes.Unimplemented {
			t.Fatal("unrelated service registered", e)
		}
	}
	handler := service.NewAdmission(f.resolver)
	for _, ctx := range []context.Context{f.ctx, metadata.NewIncomingContext(f.ctx, metadata.Pairs("x-ani-tenant-id", f.request.IdentityRef.TenantID))} {
		response, e := handler.PrepareAdmission(ctx, admissionRPCRequest(t, f))
		requireRPCError(t, response, e, codes.Unauthenticated)
	}
	requireNoResolvedExecution(t, f)
}
func TestAdmissionRPCMapsDependencyFailureWithoutUnsafeDetails(t *testing.T) {
	f := prepareResolution(t)
	for _, test := range []struct {
		failure error
		code    codes.Code
	}{{biz.ErrAdmissionEnvironmentNotReady, codes.Unavailable}, {biz.ErrPersistence, codes.Unavailable}, {biz.ErrInputNotFound, codes.NotFound}, {biz.ErrAdmissionInputNotReady, codes.FailedPrecondition}, {biz.ErrImplementationUnavailable, codes.FailedPrecondition}} {
		f.factReader.err = test.failure
		client := startAdmissionClient(t, f, f.resolver)
		response, e := client.PrepareAdmission(admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID), admissionRPCRequest(t, f))
		requireRPCError(t, response, e, test.code)
		details := status.Convert(e).Details()
		if len(details) != 1 {
			t.Fatal("safe typed reason missing")
		}
		detail, ok := details[0].(*modeldevv1.ErrorDetail)
		if !ok || detail.CorrelationId != admissionRPCRequestID || detail.SafeMessage == "" {
			t.Fatal("safe correlation missing")
		}
	}
	f.factReader.err = nil
	requireNoResolvedExecution(t, f)
}

type waitingAdmissionFacts struct {
	delegate biz.AdmissionFactsReader
	entered  chan struct{}
	exited   chan error
}

func (r waitingAdmissionFacts) ReadAdmissionFacts(ctx context.Context, tenant, id string) (biz.TenantAdmissionFacts, error) {
	if _, e := r.delegate.ReadAdmissionFacts(ctx, tenant, id); e != nil {
		return biz.TenantAdmissionFacts{}, e
	}
	close(r.entered)
	<-ctx.Done()
	r.exited <- ctx.Err()
	return biz.TenantAdmissionFacts{}, ctx.Err()
}
func TestAdmissionRPCCancellationReachesActualFactsRead(t *testing.T) {
	f := prepareResolution(t)
	waiting := waitingAdmissionFacts{delegate: f.factReader.reader, entered: make(chan struct{}), exited: make(chan error, 1)}
	client := startAdmissionClient(t, f, biz.NewAdmissionResolver(f.materials, f.inputs, waiting, &fixtureImages{selection: f.selection}))
	ctx, cancel := context.WithCancel(admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID))
	defer cancel()
	result := make(chan error, 1)
	go func() { _, e := client.PrepareAdmission(ctx, admissionRPCRequest(t, f)); result <- e }()
	select {
	case <-waiting.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("RPC did not reach read")
	}
	cancel()
	select {
	case e := <-result:
		if status.Code(e) != codes.Canceled {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RPC cancellation hung")
	}
	select {
	case e := <-waiting.exited:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not observe cancel")
	}
	requireNoResolvedExecution(t, f)
}
