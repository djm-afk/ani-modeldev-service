package commandtest

import (
	"bytes"
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGovernanceAcceptExecutionCommitsAndReplaysCurrentState(t *testing.T) {
	openPool := postgres.Prepare(t)
	request, dispatchRequest := validAcceptCommand(t)
	certificates := commandtls.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	writerPool := openPool()
	address, stopServer := startCommandListener(t, execution.New(writerPool), certificates)
	firstConnection := commandConnection(t, address, commandClientTLS(certificates))
	secondConnection := commandConnection(t, address, commandClientTLS(certificates))
	clients := []modeldevv1.ModelDevCommandServiceClient{
		modeldevv1.NewModelDevCommandServiceClient(firstConnection),
		modeldevv1.NewModelDevCommandServiceClient(secondConnection),
	}

	// Prove both actual TLS connections and the existing durable command path
	// with another execution. This cannot create or close the target Admission.
	control := validCloseRequest()
	control.ResourceTenantId, control.RequestedActorId = request.ResourceTenantId, request.AdmittedActorId
	if control.Identity.ExecutionId == request.Identity.ExecutionId || control.Identity.OperationId == request.Identity.OperationId {
		t.Fatal("CPU_ACCEPT_PREFLIGHT: control and target identities must be distinct; behavior NOT_RUN")
	}
	for i, client := range clients {
		closed, err := client.ApplyCloseIntent(commandContext(ctx, control), control)
		if err != nil {
			t.Fatalf("CPU_ACCEPT_PREFLIGHT: existing Close RPC failed on TLS client %d: %s; behavior NOT_RUN", i, status.Code(err))
		}
		assertCloseResponse(t, closed, control, i == 1)
	}
	readerPool := openPool()
	reader := execution.New(readerPool)
	controlFact, err := reader.GetCloseIntent(ctx, control.ResourceTenantId, control.Identity.ExecutionId)
	if err != nil || controlFact.Generation != 1 || controlFact.SourceGeneration != control.IntentGeneration || controlFact.SpecHash != control.Identity.ExecutionSpecHash {
		t.Fatalf("CPU_ACCEPT_PREFLIGHT: control ACK lacks independent PostgreSQL visibility: %v; behavior NOT_RUN", err)
	}
	if _, err := reader.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId); !errors.Is(err, biz.ErrExecutionNotFound) {
		t.Fatalf("CPU_ACCEPT_PREFLIGHT: target Admission is not empty: %v; behavior NOT_RUN", err)
	}
	if _, err := reader.GetCloseIntent(ctx, request.ResourceTenantId, request.Identity.ExecutionId); !errors.Is(err, biz.ErrExecutionNotFound) {
		t.Fatalf("CPU_ACCEPT_PREFLIGHT: target close is not empty: %v; behavior NOT_RUN", err)
	}
	t.Log("CPU_ACCEPT_PREFLIGHT PASS: real isolated PostgreSQL, validated shared codec fixture, two actual mTLS clients, independently durable control close and empty target")

	type outcome struct {
		response *modeldevv1.AcceptExecutionResponse
		err      error
	}
	start := make(chan struct{})
	results := make(chan outcome, len(clients))
	for _, client := range clients {
		copy := proto.Clone(request).(*modeldevv1.AcceptExecutionRequest)
		go func() {
			<-start
			response, err := client.AcceptExecution(acceptCommandContext(ctx, copy), copy)
			results <- outcome{response: response, err: err}
		}()
	}
	close(start)
	var receipts []outcome
	for range clients {
		select {
		case result := <-results:
			receipts = append(receipts, result)
		case <-ctx.Done():
			t.Fatal("CPU_ACCEPT_BEHAVIOR: valid concurrent deliveries did not finish within their bounded context")
		}
	}
	if receipts[0].err != nil || receipts[1].err != nil {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: valid authenticated AcceptExecution must return durable receipts; first=%s second=%s", status.Code(receipts[0].err), status.Code(receipts[1].err))
	}
	firstAccepts, replays := 0, 0
	for _, result := range receipts {
		assertAcceptState(t, result.response, request, modeldevv1.ComputeState_COMPUTE_STATE_ACCEPTED, modeldevv1.CloseState_CLOSE_STATE_OPEN, 1)
		if result.response.Replayed {
			replays++
		} else {
			firstAccepts++
		}
	}
	if firstAccepts != 1 || replays != 1 {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: concurrent receipt outcomes first=%d replay=%d, want one each", firstAccepts, replays)
	}
	stored, err := reader.Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	if err != nil || stored.Close != nil {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: ACK preceded the original independently readable Admission: %v", err)
	}
	assertAcceptedOriginal(t, stored, dispatchRequest.Admission, 1)

	// These are existing owner interfaces, not direct SQL state updates or KFP
	// calls. Every delivery replays the original command at the new fact version.
	dispatchPool := openPool()
	dispatches := submission.New(dispatchPool)
	reserved, err := dispatches.Reserve(ctx, dispatchRequest)
	if err != nil || reserved.SendPermit == nil || reserved.Dispatch.OwnerRevision != 2 {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: original reservation did not commit once with its permit: %v", err)
	}
	assertAcceptReplay(t, ctx, clients[0], request, modeldevv1.ComputeState_COMPUTE_STATE_SUBMITTING, modeldevv1.CloseState_CLOSE_STATE_OPEN, 2)
	permit := *reserved.SendPermit
	notSentAt := reserved.Dispatch.ReservedAt.Add(time.Microsecond)
	notSent, err := dispatches.MarkSubmissionNotSent(ctx, permit, notSentAt)
	if err != nil || notSent.OwnerRevision != 3 || notSent.State != biz.PipelineDispatchNotSent || notSent.NotSentAt == nil || !notSent.NotSentAt.Equal(notSentAt) {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: original no-send fact did not commit: %v", err)
	}
	assertAcceptReplay(t, ctx, clients[1], request, modeldevv1.ComputeState_COMPUTE_STATE_SUBMISSION_NOT_SENT, modeldevv1.CloseState_CLOSE_STATE_OPEN, 3)
	uncertainAt := notSentAt.Add(time.Microsecond)
	uncertain, err := dispatches.MarkSubmissionUncertain(ctx, permit, uncertainAt)
	if err != nil || uncertain.OwnerRevision != 4 || uncertain.State != biz.PipelineDispatchUncertain || uncertain.UncertainAt == nil || !uncertain.UncertainAt.Equal(uncertainAt) {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: original uncertainty fact did not commit: %v", err)
	}
	assertAcceptReplay(t, ctx, clients[0], request, modeldevv1.ComputeState_COMPUTE_STATE_SUBMISSION_UNCERTAIN, modeldevv1.CloseState_CLOSE_STATE_OPEN, 4)
	runAt := uncertainAt.Add(time.Microsecond)
	runID := "55555555-6666-4777-8888-999999999999"
	confirmed, err := dispatches.RecordSubmissionConfirmed(ctx, permit, biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionConfirmed, RunID: runID}, runAt)
	if err != nil || confirmed.ConflictingRuns || confirmed.Dispatch.OwnerRevision != 5 || confirmed.Dispatch.State != biz.PipelineDispatchConfirmed {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: first Run observation did not commit once: %v", err)
	}
	assertAcceptReplay(t, ctx, clients[1], request, modeldevv1.ComputeState_COMPUTE_STATE_SUBMISSION_CONFIRMED, modeldevv1.CloseState_CLOSE_STATE_OPEN, 5)
	stop := &modeldevv1.ApplyCloseIntentRequest{
		Identity: proto.Clone(request.Identity).(*trainingv1.ExecutionIdentity), ResourceTenantId: request.ResourceTenantId,
		IntentGeneration: 1, Reason: modeldevv1.CloseReason_CLOSE_REASON_USER_STOP,
		RequestedAt: timestamppb.New(runAt.Add(time.Microsecond)), RequestedActorId: request.AdmittedActorId,
	}
	closed, err := clients[0].ApplyCloseIntent(commandContext(ctx, stop), stop)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: target close RPC failed: %s", status.Code(err))
	}
	assertCloseResponse(t, closed, stop, false)
	assertAcceptReplay(t, ctx, clients[1], request, modeldevv1.ComputeState_COMPUTE_STATE_SUBMISSION_CONFIRMED, modeldevv1.CloseState_CLOSE_STATE_CLOSING, 6)

	_ = firstConnection.Close()
	_ = secondConnection.Close()
	stopServer()
	writerPool.Close()
	dispatchPool.Close()
	readerPool.Close()
	reconnectedPool := openPool()
	reconnected, _ := startCommandServer(t, execution.New(reconnectedPool), certificates)
	assertAcceptReplay(t, ctx, reconnected, request, modeldevv1.ComputeState_COMPUTE_STATE_SUBMISSION_CONFIRMED, modeldevv1.CloseState_CLOSE_STATE_CLOSING, 6)
	stored, err = execution.New(openPool()).Get(ctx, request.ResourceTenantId, request.Identity.ExecutionId)
	wantClose := biz.CloseRecord{CloseIntent: biz.CloseIntent{
		TenantID: request.ResourceTenantId, ExecutionID: request.Identity.ExecutionId, OperationID: request.Identity.OperationId,
		SpecHash: request.Identity.ExecutionSpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop,
		RequestedAt: stop.RequestedAt.AsTime(), RequestedActor: request.AdmittedActorId,
	}, Generation: 1, State: biz.CloseStateClosing}
	if err != nil || !reflect.DeepEqual(stored.Close, &wantClose) {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: reconnect lost the exact original close: %v", err)
	}
	assertAcceptedOriginal(t, stored, dispatchRequest.Admission, 6)
	replayedDispatch, err := submission.New(openPool()).Reserve(ctx, dispatchRequest)
	wantDispatch := reserved.Dispatch
	wantDispatch.OwnerRevision, wantDispatch.State = 6, biz.PipelineDispatchConfirmed
	wantDispatch.NotSentAt, wantDispatch.UncertainAt = &notSentAt, &uncertainAt
	wantDispatch.ConfirmedRuns = []biz.PipelineConfirmedRun{{RunID: runID, FirstObservedAt: runAt}}
	if err != nil || replayedDispatch.SendPermit != nil || !reflect.DeepEqual(replayedDispatch.Dispatch, wantDispatch) {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: Accept deliveries replaced retained observations or granted another permit: %v", err)
	}
}

func validAcceptCommand(t *testing.T) (*modeldevv1.AcceptExecutionRequest, biz.PipelineDispatchRequest) {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = acceptedAt.Add(time.Hour)
	intent := cpup01.Intent{Name: "durable-command", Kind: "GENERAL_TRAINING", ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_PREFLIGHT: intent fixture invalid: %v; behavior NOT_RUN", err)
	}
	specHash := ""
	admission := biz.Admission{
		TenantID: uuid.NewString(), Actor: "governance:user:42", OperationID: uuid.NewString(), ExecutionID: uuid.NewString(),
		Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: acceptedAt,
	}
	admission = admissionfixture.Bind(t, admission)
	snapshot = admission.Snapshot
	specHash, intentHash = admission.SpecHash, admission.IntentHash
	if _, _, err := admission.CanonicalPayloads(); err != nil {
		t.Fatalf("CPU_ACCEPT_PREFLIGHT: frozen envelope invalid: %v; behavior NOT_RUN", err)
	}
	wireIntent, err := contractpb.EncodeIntent(intent)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_PREFLIGHT: shared intent codec rejected fixture: %v; behavior NOT_RUN", err)
	}
	wireSnapshot, err := contractpb.EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_PREFLIGHT: shared snapshot codec rejected fixture: %v; behavior NOT_RUN", err)
	}
	request := &modeldevv1.AcceptExecutionRequest{
		Identity:         &trainingv1.ExecutionIdentity{OperationId: admission.OperationID, ExecutionId: admission.ExecutionID, ExecutionSpecHash: specHash},
		ResourceTenantId: admission.TenantID, AdmittedActorId: admission.Actor, IntentHash: intentHash,
		Snapshot: wireSnapshot, AcceptedAt: timestamppb.New(acceptedAt), Intent: wireIntent,
	}
	// Same bounded synthetic owner configuration used by the submission tests;
	// no fixture value establishes a deployed PipelineRoot or real ENV identity.
	dispatch := biz.PipelineDispatchRequest{Admission: admission, Owner: biz.PipelineOwnerConfiguration{
		Reference: "cpu07-fixture-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root",
	}}
	if _, err := dispatch.Freeze(); err != nil {
		t.Fatalf("CPU_ACCEPT_PREFLIGHT: owner plan fixture invalid: %v; behavior NOT_RUN", err)
	}
	return request, dispatch
}

func acceptCommandContext(ctx context.Context, request *modeldevv1.AcceptExecutionRequest) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", request.ResourceTenantId, "x-ani-actor", request.AdmittedActorId, "x-ani-request-id", uuid.NewString()))
}

func assertAcceptState(t *testing.T, response *modeldevv1.AcceptExecutionResponse, request *modeldevv1.AcceptExecutionRequest, compute modeldevv1.ComputeState, closeState modeldevv1.CloseState, revision uint64) {
	t.Helper()
	wantStates := &modeldevv1.ExecutionStates{
		ComputeState: compute, DeliveryState: modeldevv1.DeliveryState_DELIVERY_STATE_PENDING,
		ResourceState: modeldevv1.ResourceState_RESOURCE_STATE_NOT_APPLICABLE, CloseState: closeState,
	}
	if response == nil || !proto.Equal(response.Identity, request.Identity) || response.Revision != revision || !proto.Equal(response.States, wantStates) {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: receipt identity/four states/revision differ; want compute=%s close=%s revision=%d", compute, closeState, revision)
	}
}

func assertAcceptReplay(t *testing.T, ctx context.Context, client modeldevv1.ModelDevCommandServiceClient, request *modeldevv1.AcceptExecutionRequest, compute modeldevv1.ComputeState, closeState modeldevv1.CloseState, revision uint64) {
	t.Helper()
	response, err := client.AcceptExecution(acceptCommandContext(ctx, request), request)
	if err != nil {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: original command replay failed: %s", status.Code(err))
	}
	assertAcceptState(t, response, request, compute, closeState, revision)
	if !response.Replayed {
		t.Fatal("CPU_ACCEPT_BEHAVIOR: original command replay reported another first acceptance")
	}
}

func assertAcceptedOriginal(t *testing.T, got biz.Execution, want biz.Admission, revision uint64) {
	t.Helper()
	wantIntent, wantSnapshot, wantErr := want.CanonicalPayloads()
	gotIntent, gotSnapshot, gotErr := got.Admission.CanonicalPayloads()
	if wantErr != nil || gotErr != nil || !bytes.Equal(gotIntent, wantIntent) || !bytes.Equal(gotSnapshot, wantSnapshot) ||
		got.TenantID != want.TenantID || got.Actor != want.Actor || got.OperationID != want.OperationID || got.ExecutionID != want.ExecutionID ||
		got.IntentHash != want.IntentHash || got.SpecHash != want.SpecHash || !got.AcceptedAt.Equal(want.AcceptedAt) || got.OwnerRevision != revision {
		t.Fatalf("CPU_ACCEPT_BEHAVIOR: durable receipt did not retain the full original Admission at revision %d", revision)
	}
}
