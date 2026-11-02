package commandtest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGovernanceAcceptAfterEarlierCloseIsFirstAndReplaysAfterRestart(t *testing.T) {
	openPool := postgres.Prepare(t)
	request, original := validAcceptCommand(t, openPool())
	closeRequest, wantClose := closeBeforeAcceptRequest(request)
	certificates := commandtls.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	writer := openPool()
	client, stop := startCommandServer(t, execution.New(writer), certificates)
	closed, err := client.ApplyCloseIntent(commandContext(ctx, closeRequest), closeRequest)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_PREFLIGHT: earlier close RPC failed: %s; behavior NOT_RUN", status.Code(err))
	}
	assertCloseResponse(t, closed, closeRequest, false)
	readerPool := openPool()
	reader := execution.New(readerPool)
	persistedClose, err := reader.GetCloseIntent(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if err != nil || !reflect.DeepEqual(persistedClose, wantClose) {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_PREFLIGHT: earlier close is not independently durable: %v; behavior NOT_RUN", err)
	}
	if _, err := reader.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId); !errors.Is(err, biz.ErrExecutionNotFound) {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_PREFLIGHT: close invented an Admission: %v; behavior NOT_RUN", err)
	}
	t.Log("CPU_ACCEPT_PERSISTENCE_PREFLIGHT PASS: real PG, shared command fixture, actual mTLS close tombstone with source 41 and owner fence 1")

	first, err := client.AcceptExecution(acceptCommandContext(ctx, request), request)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: first Admission after close failed: %s", status.Code(err))
	}
	assertAcceptState(t, first, request, modeldevv1.ComputeState_COMPUTE_STATE_ACCEPTED, modeldevv1.CloseState_CLOSE_STATE_CLOSING, 2)
	if first.Replayed {
		t.Fatal("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: the close-only identity was mistaken for an existing Admission")
	}
	stored, err := reader.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if err != nil || !reflect.DeepEqual(stored.Close, &wantClose) {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: first ACK lost or changed the earlier close: %v", err)
	}
	assertAcceptedOriginal(t, stored, original.Admission, 2)
	stop()
	writer.Close()
	readerPool.Close()

	restarted, _ := startCommandServer(t, execution.New(openPool()), certificates)
	assertAcceptReplay(t, ctx, restarted, request, modeldevv1.ComputeState_COMPUTE_STATE_ACCEPTED, modeldevv1.CloseState_CLOSE_STATE_CLOSING, 2)
	replayedClose, err := restarted.ApplyCloseIntent(commandContext(ctx, closeRequest), closeRequest)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: original close failed to replay after restart: %s", status.Code(err))
	}
	assertCloseResponse(t, replayedClose, closeRequest, true)
	reconnected := execution.New(openPool())
	reloaded, err := reconnected.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if err != nil || !reflect.DeepEqual(reloaded.Close, &wantClose) {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: restart changed the original source, owner fence or close: %v", err)
	}
	assertAcceptedOriginal(t, reloaded, original.Admission, 2)
}

func TestGovernanceAcceptCommitFailureHasNoACKAndPreservesEarlierClose(t *testing.T) {
	openPool := postgres.Prepare(t)
	request, original := validAcceptCommand(t, openPool())
	closeRequest, wantClose := closeBeforeAcceptRequest(request)
	certificates := commandtls.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := openPool()
	trace := &acceptRPCCommitTrace{}
	config := fixture.Config()
	config.ConnConfig.Tracer = trace
	writer, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("CPU_ACCEPT_PERSISTENCE_PREFLIGHT: traced runtime pool unavailable; behavior NOT_RUN")
	}
	t.Cleanup(writer.Close)
	if err := writer.Ping(ctx); err != nil {
		t.Fatal("CPU_ACCEPT_PERSISTENCE_PREFLIGHT: traced runtime connection unavailable; behavior NOT_RUN")
	}
	fixture.Close()
	client, stop := startCommandServer(t, execution.New(writer), certificates)
	closed, err := client.ApplyCloseIntent(commandContext(ctx, closeRequest), closeRequest)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_PREFLIGHT: earlier close RPC failed: %s; behavior NOT_RUN", status.Code(err))
	}
	assertCloseResponse(t, closed, closeRequest, false)
	reader := execution.New(openPool())
	persistedClose, err := reader.GetCloseIntent(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if err != nil || !reflect.DeepEqual(persistedClose, wantClose) {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_PREFLIGHT: earlier tombstone lacks independent visibility: %v; behavior NOT_RUN", err)
	}
	removeFault := postgres.RejectAdmissionCommit(t, writer)
	t.Log("CPU_ACCEPT_PERSISTENCE_PREFLIGHT PASS: real mTLS, prior durable close, restricted traced writer and deferred Admission COMMIT fault installed")

	delivery := acceptCommandContext(ctx, request)
	response, err := client.AcceptExecution(delivery, request)
	if !trace.failed.Load() {
		t.Fatal("CPU_ACCEPT_PERSISTENCE_PREFLIGHT: exact deferred 23514 Admission marker was not observed at COMMIT; rollback behavior NOT_RUN")
	}
	t.Log("CPU_ACCEPT_COMMIT_FAULT: real COMMIT reached 23514 injected_admission_commit_failure")
	if response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: failed COMMIT returned %s with ACK=%t", status.Code(err), response != nil)
	}
	failure := status.Convert(err)
	if failure.Message() != "command persistence unavailable" {
		t.Fatal("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: failed COMMIT exposed an unexpected transport message")
	}
	details := failure.Details()
	if len(details) != 1 {
		t.Fatal("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: failed COMMIT omitted its single safe typed detail")
	}
	detail, ok := details[0].(*modeldevv1.ErrorDetail)
	md, _ := metadata.FromOutgoingContext(delivery)
	wantDetail := &modeldevv1.ErrorDetail{
		Reason:      modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE,
		SafeMessage: "command persistence unavailable", CorrelationId: md.Get("x-ani-request-id")[0],
	}
	if !ok || !proto.Equal(detail, wantDetail) {
		t.Fatal("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: persistence error changed its safe reason/correlation or exposed extra detail")
	}
	stop()
	writer.Close()
	reconnected := execution.New(openPool())
	absent, err := reconnected.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if !errors.Is(err, biz.ErrExecutionNotFound) || !reflect.DeepEqual(absent, biz.Execution{}) {
		t.Fatal("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: failed RPC COMMIT left an Admission")
	}
	persistedClose, err = reconnected.GetCloseIntent(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if err != nil || !reflect.DeepEqual(persistedClose, wantClose) {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: failed RPC changed the original tombstone: %v", err)
	}
	removeFault()
	restarted, _ := startCommandServer(t, execution.New(openPool()), certificates)
	first, err := restarted.AcceptExecution(acceptCommandContext(ctx, request), request)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: original command failed after fault removal and restart: %s", status.Code(err))
	}
	assertAcceptState(t, first, request, modeldevv1.ComputeState_COMPUTE_STATE_ACCEPTED, modeldevv1.CloseState_CLOSE_STATE_CLOSING, 2)
	if first.Replayed {
		t.Fatal("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: rolled-back Admission was reported as already accepted")
	}
	assertAcceptReplay(t, ctx, restarted, request, modeldevv1.ComputeState_COMPUTE_STATE_ACCEPTED, modeldevv1.CloseState_CLOSE_STATE_CLOSING, 2)
	stored, err := reconnected.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if err != nil || !reflect.DeepEqual(stored.Close, &wantClose) {
		t.Fatalf("CPU_ACCEPT_PERSISTENCE_BEHAVIOR: recovered ACK lost the original close: %v", err)
	}
	assertAcceptedOriginal(t, stored, original.Admission, 2)
}

func closeBeforeAcceptRequest(request *modeldevv1.AcceptExecutionRequest) (*modeldevv1.ApplyCloseIntentRequest, biz.CloseRecord) {
	closeRequest := &modeldevv1.ApplyCloseIntentRequest{
		Identity: proto.Clone(request.Identity).(*trainingv1.ExecutionIdentity), ResourceTenantId: request.ResourceTenantId,
		IntentGeneration: 41, Reason: modeldevv1.CloseReason_CLOSE_REASON_USER_STOP,
		RequestedAt: timestamppb.New(request.AcceptedAt.AsTime()), RequestedActorId: request.AdmittedActorId,
	}
	want := biz.CloseRecord{CloseIntent: biz.CloseIntent{
		TenantID: request.ResourceTenantId, OperationID: request.Identity.OperationId, ExecutionID: request.Identity.ExecutionId,
		SpecHash: request.Identity.ExecutionSpecHash, SourceGeneration: 41, Reason: biz.CloseReasonUserStop,
		RequestedAt: request.AcceptedAt.AsTime(), RequestedActor: request.AdmittedActorId,
	}, Generation: 1, State: biz.CloseStateClosing}
	return closeRequest, want
}

// Observe only the exact test marker at real COMMIT; never retain query
// arguments or raw errors. Product assertions use RPCs and repository reads.
type acceptRPCCommitTrace struct {
	failed atomic.Bool
}

type acceptRPCCommitTraceKey struct{}

func (*acceptRPCCommitTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, acceptRPCCommitTraceKey{}, strings.EqualFold(strings.TrimSpace(data.SQL), "commit"))
}

func (trace *acceptRPCCommitTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	commit, _ := ctx.Value(acceptRPCCommitTraceKey{}).(bool)
	var fault *pgconn.PgError
	if commit && errors.As(data.Err, &fault) && fault.Code == "23514" && fault.Message == "injected_admission_commit_failure" {
		trace.failed.Store(true)
	}
}
