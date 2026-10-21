package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type registrationRecovery struct {
	result            biz.Execution
	err               error
	calls             int
	tenant, execution string
}

func (r *registrationRecovery) ResumeModelRegistration(_ context.Context, tenant, execution string) (biz.Execution, error) {
	r.calls++
	r.tenant, r.execution = tenant, execution
	return r.result, r.err
}

type registrationQuery struct {
	recordedLogQueryRepository
	calls             int
	err               error
	tenant, execution string
}

func (q *registrationQuery) GetQueryRecord(_ context.Context, tenant, execution string) (biz.QueryRecord, error) {
	q.calls++
	q.tenant, q.execution = tenant, execution
	return q.record, q.err
}

func registrationQueryFixture(t *testing.T) biz.QueryRecord {
	t.Helper()
	s := conformance.FinetuningSnapshotV1()
	hash, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	closed := s.Timing.AcceptedAt.Add(time.Hour)
	return biz.QueryRecord{
		Execution: biz.Execution{Admission: biz.Admission{TenantID: s.IdentityRef.TenantID, Actor: s.IdentityRef.Actor, OperationID: s.IdentityRef.OperationID, ExecutionID: s.IdentityRef.ExecutionID, Intent: conformance.FinetuningIntentV1(), IntentHash: s.IdentityRef.IntentHash, Snapshot: s, SpecHash: hash, AcceptedAt: s.Timing.AcceptedAt}, States: biz.ExecutionStates{Compute: biz.ComputeStateSucceeded, Delivery: biz.DeliveryStatePending, Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateClosed}},
		Runtime:   biz.ExecutionRuntime{CloseGeneration: 7, ClosedAt: &closed, FinetuningRegistration: &biz.FinetuningRegistration{Bundle: cpup01.FixedObjectRef{SizeBytes: 2048, SHA256: strings.Repeat("d", 64)}, State: biz.RegistrationStatePending, RecordedAt: closed.Add(-time.Minute), DeadlineAt: closed}},
	}
}

func registrationContext(record biz.QueryRecord, method string) context.Context {
	return WithVerifiedGovernanceQuery(context.Background(), GovernanceDelivery{TenantID: record.Execution.TenantID, Actor: record.Execution.Actor, RequestID: "registration-recovery"}, method)
}

func readyRegistration(record *biz.QueryRecord) {
	registration := record.Runtime.FinetuningRegistration
	registration.State = biz.RegistrationStateReady
	registration.Receipt = &biz.FinetuningModelReceipt{RegistrationID: "11111111-1111-4111-8111-111111111111", ModelID: "22222222-2222-4222-8222-222222222222", ModelVersionID: "33333333-3333-4333-8333-333333333333", StoragePath: "original/registration/model.tar", SizeBytes: registration.Bundle.SizeBytes, SHA256: registration.Bundle.SHA256}
}

func originalFailedHistory(record *biz.QueryRecord) {
	closed := *record.Runtime.ClosedAt
	record.Execution.States.Compute = biz.ComputeStateFailed
	record.Runtime.CloseEvidence = &biz.ManagedCloseEvidence{
		RunID: "original-kfp-run", WorkflowUID: "original-workflow-uid", ObservedAt: closed,
		OwnerTermination: &biz.ManagedOwnerTermination{
			RunState: "FAILED", RunFinishedAt: closed.Add(-2 * time.Second),
			WorkflowPhase: "Failed", WorkflowFinishedAt: closed.Add(-time.Second),
			WorkflowResourceVersion: "original-workflow-rv", WorkflowNodesSHA256: strings.Repeat("a", 64),
		},
	}
}

func TestClosedFailureHistorySurvivesReadyRegistration(t *testing.T) {
	record := registrationQueryFixture(t)
	originalFailedHistory(&record)
	readyRegistration(&record)
	view, err := executionView(record)
	if err != nil {
		t.Fatal(err)
	}
	history := view.GetHistory()
	evidence := record.Runtime.CloseEvidence
	owner := evidence.OwnerTermination
	if history == nil || history.RunId != evidence.RunID || history.WorkflowUid != evidence.WorkflowUID || !history.ObservedAt.AsTime().Equal(evidence.ObservedAt) || history.RunState != modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_FAILED || history.WorkflowPhase != modeldevv1.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_FAILED || !history.RunFinishedAt.AsTime().Equal(owner.RunFinishedAt) || !history.WorkflowFinishedAt.AsTime().Equal(owner.WorkflowFinishedAt) || history.WorkflowResourceVersion != owner.WorkflowResourceVersion || history.WorkflowNodesSha256 != owner.WorkflowNodesSHA256 {
		t.Fatal("original failed KFP/Workflow evidence was lost")
	}
	if view.States.ComputeState != modeldevv1.ComputeState_COMPUTE_STATE_FAILED || view.States.CloseState != modeldevv1.CloseState_CLOSE_STATE_CLOSED || view.ModelRegistration.State != biz.RegistrationStateReady {
		t.Fatal("registration success replaced original execution failure")
	}
}

func TestExecutionHistoryDoesNotInventMissingControllerFacts(t *testing.T) {
	record := registrationQueryFixture(t)
	view, err := executionView(record)
	if err != nil || view.History != nil {
		t.Fatal("missing persisted evidence was synthesized", err)
	}
	record.Runtime.CloseEvidence = &biz.ManagedCloseEvidence{RunID: "original-run", WorkflowUID: "original-workflow"}
	view, err = executionView(record)
	if err != nil || view.History == nil || view.History.RunState != modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_UNSPECIFIED || view.History.WorkflowPhase != modeldevv1.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_UNSPECIFIED || view.History.RunFinishedAt != nil || view.History.WorkflowFinishedAt != nil || view.History.ObservedAt != nil {
		t.Fatal("unknown terminal history became success or failure", err)
	}
}

func TestExecutionHistoryRejectsInvalidPersistedTerminalEvidence(t *testing.T) {
	for _, mutate := range []func(*biz.ManagedCloseEvidence){
		func(e *biz.ManagedCloseEvidence) { e.OwnerTermination.RunState = "RUNNING" },
		func(e *biz.ManagedCloseEvidence) { e.OwnerTermination.WorkflowPhase = "Running" },
		func(e *biz.ManagedCloseEvidence) { e.OwnerTermination.RunFinishedAt = time.Time{} },
		func(e *biz.ManagedCloseEvidence) {
			e.OwnerTermination.WorkflowFinishedAt = e.ObservedAt.Add(time.Second)
		},
		func(e *biz.ManagedCloseEvidence) { e.OwnerTermination.WorkflowResourceVersion = "" },
		func(e *biz.ManagedCloseEvidence) { e.OwnerTermination.WorkflowNodesSHA256 = "unverified" },
	} {
		record := registrationQueryFixture(t)
		originalFailedHistory(&record)
		mutate(record.Runtime.CloseEvidence)
		if _, err := executionView(record); !errors.Is(err, biz.ErrPersistence) {
			t.Fatal("invalid original history projected", err)
		}
	}
}

func TestRegistrationProjectionUsesOnlyOriginalDurableReceipt(t *testing.T) {
	for _, state := range []string{biz.RegistrationStatePending, biz.RegistrationStateUnavailable, biz.RegistrationStateReady} {
		t.Run(state, func(t *testing.T) {
			record := registrationQueryFixture(t)
			registration := record.Runtime.FinetuningRegistration
			registration.State = state
			if state == biz.RegistrationStateUnavailable {
				registration.FailureCode = biz.RegistrationUnavailableFailureCode
			}
			if state == biz.RegistrationStateReady {
				readyRegistration(&record)
			}
			view, err := executionView(record)
			if err != nil {
				t.Fatal(err)
			}
			got := view.GetModelRegistration()
			if got == nil || got.State != state || got.FailureCode != registration.FailureCode || view.CloseGeneration != 7 || !view.ObservedAt.AsTime().Equal(*record.Runtime.ClosedAt) {
				t.Fatal("original registration/closed execution projection lost")
			}
			if state == biz.RegistrationStateReady {
				if got.RegistrationId != registration.Receipt.RegistrationID || got.ModelId != registration.Receipt.ModelID || got.ModelVersionId != registration.Receipt.ModelVersionID {
					t.Fatal("READY lacks original provider identities")
				}
			} else if got.RegistrationId != "" || got.ModelId != "" || got.ModelVersionId != "" {
				t.Fatal("unverified registration projected provider receipt")
			}
		})
	}
}

func TestRegistrationProjectionRejectsUnverifiedOrChangedReadyReceipt(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*biz.QueryRecord)
	}{
		{"missing receipt", func(r *biz.QueryRecord) { r.Runtime.FinetuningRegistration.Receipt = nil }},
		{"changed bytes", func(r *biz.QueryRecord) { r.Runtime.FinetuningRegistration.Receipt.SizeBytes++ }},
		{"changed hash", func(r *biz.QueryRecord) { r.Runtime.FinetuningRegistration.Receipt.SHA256 = strings.Repeat("e", 64) }},
		{"missing hash", func(r *biz.QueryRecord) {
			r.Runtime.FinetuningRegistration.Receipt.SHA256 = ""
			r.Runtime.FinetuningRegistration.Bundle.SHA256 = ""
		}},
		{"invalid identity", func(r *biz.QueryRecord) { r.Runtime.FinetuningRegistration.Receipt.ModelVersionID = "foreign-version" }},
		{"missing original path", func(r *biz.QueryRecord) { r.Runtime.FinetuningRegistration.Receipt.StoragePath = "" }},
		{"failure plus ready", func(r *biz.QueryRecord) {
			r.Runtime.FinetuningRegistration.FailureCode = biz.RegistrationUnavailableFailureCode
		}},
		{"unknown state", func(r *biz.QueryRecord) { r.Runtime.FinetuningRegistration.State = "UNKNOWN" }},
		{"receipt before ready", func(r *biz.QueryRecord) { r.Runtime.FinetuningRegistration.State = biz.RegistrationStatePending }},
		{"other target", func(r *biz.QueryRecord) {
			target := "44444444-4444-4444-8444-444444444444"
			r.Execution.Snapshot.Finetuning.TargetModelID = &target
			r.Execution.Snapshot.Finetuning.NewModelName = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := registrationQueryFixture(t)
			readyRegistration(&record)
			test.mutate(&record)
			if _, err := executionView(record); !errors.Is(err, biz.ErrPersistence) {
				t.Fatal("invalid persisted receipt became visible", err)
			}
		})
	}
}

func TestResumeRegistrationReturnsSameFullHistoricalQueryProjection(t *testing.T) {
	record := registrationQueryFixture(t)
	originalFailedHistory(&record)
	readyRegistration(&record)
	recovery := &registrationRecovery{result: record.Execution}
	queries := &registrationQuery{recordedLogQueryRepository: recordedLogQueryRepository{record: record}}
	operations := NewOperations(nil).WithRegistrationRecovery(recovery, queries)
	response, err := operations.ResumeModelRegistration(registrationContext(record, modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName), &modeldevv1.ResumeModelRegistrationRequest{ExecutionId: record.Execution.ExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	want, err := executionView(record)
	if err != nil || !proto.Equal(response.Execution, want) || response.Execution.ModelRegistration.ModelVersionId == "" || response.Execution.ExplicitFinetuning == nil || len(response.Execution.ResolvedParameters) != 9 || response.Execution.GetHistory().GetRunState() != modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_FAILED {
		t.Fatal("resume returned partial execution or synthesized receipt", err)
	}
	if recovery.calls != 1 || queries.calls != 1 || recovery.tenant != record.Execution.TenantID || queries.tenant != record.Execution.TenantID || recovery.execution != record.Execution.ExecutionID || queries.execution != record.Execution.ExecutionID {
		t.Fatal("recovery escaped delegated tenant/execution")
	}
}

func TestResumeRegistrationRequiresCurrentExactAuthorityAndCompleteDependencies(t *testing.T) {
	record := registrationQueryFixture(t)
	request := &modeldevv1.ResumeModelRegistrationRequest{ExecutionId: record.Execution.ExecutionID}
	for _, test := range []struct {
		name         string
		ctx          context.Context
		unknown      bool
		dependencies int
		code         codes.Code
	}{
		{"missing authority", context.Background(), false, 2, codes.Unauthenticated},
		{"other method", registrationContext(record, modeldevv1.ModelDevQueryService_GetExecution_FullMethodName), false, 2, codes.Unauthenticated},
		{"unknown override", registrationContext(record, modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName), true, 2, codes.InvalidArgument},
		{"no recovery", registrationContext(record, modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName), false, 0, codes.Unavailable},
		{"no query repository", registrationContext(record, modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName), false, 1, codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			recovery := &registrationRecovery{result: record.Execution}
			queries := &registrationQuery{recordedLogQueryRepository: recordedLogQueryRepository{record: record}}
			operations := NewOperations(nil)
			if test.dependencies == 1 {
				operations.WithRegistrationRecovery(recovery, nil)
			} else if test.dependencies == 2 {
				operations.WithRegistrationRecovery(recovery, queries)
			}
			copy := proto.Clone(request).(*modeldevv1.ResumeModelRegistrationRequest)
			if test.unknown {
				copy.ProtoReflect().SetUnknown([]byte{0x12, 0})
			}
			if _, err := operations.ResumeModelRegistration(test.ctx, copy); status.Code(err) != test.code || recovery.calls != 0 || queries.calls != 0 {
				t.Fatal("recovery ran before authority/shape/dependency checks", err)
			}
		})
	}
}

func TestResumeRegistrationRejectsForeignOrChangedQueryRecord(t *testing.T) {
	for _, field := range []string{"tenant", "execution", "operation", "spec"} {
		t.Run(field, func(t *testing.T) {
			record := registrationQueryFixture(t)
			recovery := &registrationRecovery{result: record.Execution}
			queries := &registrationQuery{recordedLogQueryRepository: recordedLogQueryRepository{record: record}}
			switch field {
			case "tenant":
				queries.record.Execution.TenantID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "execution":
				queries.record.Execution.ExecutionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "operation":
				queries.record.Execution.OperationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "spec":
				queries.record.Execution.SpecHash = strings.Repeat("e", 64)
			}
			_, err := NewOperations(nil).WithRegistrationRecovery(recovery, queries).ResumeModelRegistration(registrationContext(record, modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName), &modeldevv1.ResumeModelRegistrationRequest{ExecutionId: record.Execution.ExecutionID})
			if status.Code(err) != codes.Unavailable || recovery.calls != 1 || queries.calls != 1 {
				t.Fatal("foreign query record returned after recovery", err)
			}
		})
	}
}
