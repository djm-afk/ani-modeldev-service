package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	accpb "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
)

func gpuReleaseAdmission(t *testing.T) biz.Admission {
	t.Helper()
	s := conformance.SnapshotV1()
	selection := &compute.ComputeSelection{SchemaVersion: 1, PresetId: s.ComputeOrigin.ComputePresetID, Code: "gpu-t4", Name: "T4", ComputeType: "gpu", CpuMillis: 1000, MemoryMib: 1024, Gpu: &compute.GpuSelection{ProfileId: "12121212-1212-4212-8212-121212121212", ProfileVersion: 3, SpecDigest: strings.Repeat("a", 64), DevicesPerUnit: 1, ClusterId: s.Environment.ClusterID, PoolId: "13131313-1313-4313-8313-131313131313", Mode: accpb.SupplyMode_SHARED_FIXED, ModelKey: "nvidia-t4", MemoryMibPerDevice: 2048, CoreLimitPercent: 30}}
	var err error
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := compute.CanonicalSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	s.ComputeOrigin, err = codec.DecodeComputeSelection(raw, selection.SelectionDigest)
	if err != nil {
		t.Fatal(err)
	}
	e := &accpb.GpuExecution{SchedulerName: "volcano", QueueName: "gpu-task", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "14141414-1414-4414-8414-141414141414", "accelerator.ani.io/model-key": "nvidia-t4", "accelerator.ani.io/baseline-id": "15151515-1515-4515-8515-151515151515"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "2048", "volcano.sh/vgpu-cores": "30"}}
	e.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(e)
	if err != nil {
		t.Fatal(err)
	}
	er, dr, err := codec.EncodeGPUPlanCanonical(e, &accpb.GpuDisplay{ProfileId: selection.Gpu.ProfileId, ProfileVersion: 3, ProfileName: "T4 fixture", ModelKey: "nvidia-t4", Mode: selection.Gpu.Mode})
	if err != nil {
		t.Fatal(err)
	}
	s.GPU, err = codec.DecodeGPUPlanCanonical(er, dr)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	s.DeadlineAt = now.Add(time.Hour)
	return admissionfixture.Bind(t, biz.Admission{TenantID: uuid.NewString(), Actor: "governance:user:gpu-release", OperationID: uuid.NewString(), ExecutionID: uuid.NewString(), Intent: cpup01.Intent{Name: "GPU release", Kind: "GENERAL_TRAINING", ImplementationID: s.Implementation.ImplementationID, ComputePresetID: s.ComputeOrigin.ComputePresetID, DatasetVersionID: s.Input.InputVersionID, MLP: &cpup01.MLPParameters{}}, Snapshot: s, AcceptedAt: now})
}

func releasedObservation(scope biz.GPUReleaseScope, now time.Time) biz.GPUReleaseObservation {
	return biz.GPUReleaseObservation{Ref: scope.Ref, Finding: "ALLOCATION_RELEASED", Complete: true, EvidenceID: uuid.NewString(), EvidenceDigest: strings.Repeat("c", 64), ObservedAt: now, ReceivedAt: now, ActivePodUIDs: []string{}, UncertainNodeUIDs: []string{}, Issues: []string{}}
}

type releaseObserverFunc func(context.Context, biz.GPUReleaseScope) (biz.GPUReleaseObservation, error)

func (f releaseObserverFunc) ObserveGPURelease(ctx context.Context, scope biz.GPUReleaseScope) (biz.GPUReleaseObservation, error) {
	return f(ctx, scope)
}

type releaseReporterFunc func(context.Context, biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error)

func (f releaseReporterFunc) ReportGPUResourceReleased(ctx context.Context, event biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error) {
	return f(ctx, event)
}

// Persistence is the real restricted PostgreSQL repository, including the
// original admission, Stop fence, no-dispatch proof, seal and release outbox.
// ACC and Gov answers enter only at their explicit external boundaries.
func TestGPUReleaseNoDispatchSealUnknownOutboxAndACKSurviveReconnect(t *testing.T) {
	open := postgres.Prepare(t)
	ctx := context.Background()
	a := gpuReleaseAdmission(t)
	pool := open()
	accepted, err := execution.New(pool).Accept(ctx, a)
	if err != nil {
		t.Fatal("GPU admission", err)
	}
	repository := lifecycle.New(pool)
	if _, err := repository.SealGPURelease(ctx, a.TenantID, a.ExecutionID); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatal("unclosed execution sealed", err)
	}
	if _, err := execution.New(pool).ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: a.TenantID, ExecutionID: a.ExecutionID, OperationID: a.OperationID, SpecHash: a.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: a.Actor}); err != nil {
		t.Fatal(err)
	}
	closed, err := repository.CloseUndispatched(ctx, a.TenantID, a.ExecutionID)
	if err != nil || closed.CloseOperationID == a.OperationID || closed.CloseOperationID == "" {
		t.Fatal("independent durable close UUID", err)
	}
	sealed, err := lifecycle.New(open()).SealGPURelease(ctx, a.TenantID, a.ExecutionID)
	if err != nil || sealed.GPURelease == nil || !sealed.GPURelease.NoDispatch || len(sealed.GPURelease.Scope.Pods) != 0 || sealed.GPURelease.Scope.SealedAt.Before(*closed.ClosedAt) {
		t.Fatal("post-close GPU scope not sealed", err)
	}
	original := *sealed.GPURelease
	again, err := lifecycle.New(open()).SealGPURelease(ctx, a.TenantID, a.ExecutionID)
	if err != nil || again.OwnerRevision != sealed.OwnerRevision || !reflect.DeepEqual(again.GPURelease, sealed.GPURelease) {
		t.Fatal("seal retry changed original request", err)
	}
	unknown := biz.GPUReleaseObservation{Ref: original.Scope.Ref, Finding: "RELEASE_UNKNOWN", EvidenceID: uuid.NewString(), EvidenceDigest: strings.Repeat("a", 64), Issues: []string{"SOURCE_UNKNOWN"}, UncertainNodeUIDs: []string{"node-without-sources"}, ActivePodUIDs: []string{}}
	state, err := lifecycle.New(open()).RecordGPUReleaseObservation(ctx, a.TenantID, a.ExecutionID, original.ScopeSHA256, unknown)
	if err != nil || state.GPURelease.Observation == nil || !state.GPURelease.Observation.ObservedAt.IsZero() || state.GPURelease.Outbox != nil {
		t.Fatal("missing sources were not retained as unknown", err)
	}
	binding := biz.PipelineDispatchBinding{TenantID: a.TenantID, Environment: a.Snapshot.Environment, Owner: biz.PipelineOwnerConfiguration{Reference: "gpu-release-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/gpu-release"}}
	pending, err := lifecycle.New(open()).ListPendingClosures(ctx, binding, "", 10)
	if err != nil || len(pending) != 1 || pending[0] != a.ExecutionID {
		t.Fatal("restart scan lost unknown GPU release", pending, err)
	}
	bad := releasedObservation(original.Scope, time.Now().UTC())
	bad.Ref.CreateOperationID = uuid.NewString()
	if _, err := repository.RecordGPUReleaseObservation(ctx, a.TenantID, a.ExecutionID, original.ScopeSHA256, bad); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("foreign original ref accepted", err)
	}
	positive := releasedObservation(original.Scope, time.Now().UTC().Add(-time.Millisecond))
	lostACK := errors.New("Gov applied release; transport lost ACK")
	observer := releaseObserverFunc(func(ctx context.Context, scope biz.GPUReleaseScope) (biz.GPUReleaseObservation, error) {
		durable, e := lifecycle.New(open()).GetRuntime(ctx, a.TenantID, a.ExecutionID)
		if e != nil || !reflect.DeepEqual(durable.GPURelease.Scope, scope) || scope.CloseOperationID != closed.CloseOperationID {
			t.Fatal("ACC called before original scope commit", e)
		}
		return positive, nil
	})
	reporter := releaseReporterFunc(func(ctx context.Context, event biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error) {
		durable, e := lifecycle.New(open()).GetRuntime(ctx, a.TenantID, a.ExecutionID)
		if e != nil || durable.GPURelease.Outbox == nil || durable.GPURelease.Outbox.Event != event {
			t.Fatal("Gov called before complete evidence/outbox commit", e)
		}
		return biz.GPUReleaseACK{}, lostACK
	})
	reconciler, err := biz.NewGPUReleaseReconciler(lifecycle.New(open()), observer, reporter)
	if err != nil {
		t.Fatal(err)
	}
	state, err = reconciler.Reconcile(ctx, a.TenantID, a.ExecutionID)
	if !errors.Is(err, lostACK) || state.GPURelease.Outbox == nil || state.GPURelease.Outbox.ACK != nil {
		t.Fatal("positive complete evidence did not commit release outbox before lost ACK", err)
	}
	outbox := *state.GPURelease.Outbox
	eventBytes, _ := json.Marshal(outbox.Event)
	var fields map[string]any
	_ = json.Unmarshal(eventBytes, &fields)
	if len(fields) != 4 || outbox.Event.CreateOperationID != a.OperationID || outbox.Event.ResourceID != a.ExecutionID || outbox.Event.Reason != "RESOURCE_RELEASED" {
		t.Fatal("Gov event lost its four-field original identity", string(eventBytes))
	}
	retried, err := lifecycle.New(open()).RecordGPUReleaseObservation(ctx, a.TenantID, a.ExecutionID, original.ScopeSHA256, positive)
	if err != nil || !reflect.DeepEqual(retried.GPURelease.Outbox, &outbox) {
		t.Fatal("lost ACK retry minted another event", err)
	}
	ack := biz.GPUReleaseACK{ReleaseEventID: outbox.Event.ReleaseEventID, CreateOperationID: a.OperationID, ResourceID: a.ExecutionID, Status: "ALREADY_APPLIED"}
	badACK := ack
	badACK.ResourceID = uuid.NewString()
	if _, err := repository.RecordGPUReleaseACK(ctx, a.TenantID, a.ExecutionID, outbox.Event.ReleaseEventID, badACK); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("foreign ACK accepted", err)
	}
	resume, err := biz.NewGPUReleaseReconciler(lifecycle.New(open()), releaseObserverFunc(func(context.Context, biz.GPUReleaseScope) (biz.GPUReleaseObservation, error) {
		t.Fatal("durable positive outbox caused another ACC observation")
		return biz.GPUReleaseObservation{}, biz.ErrPersistence
	}), releaseReporterFunc(func(_ context.Context, event biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error) {
		if event != outbox.Event {
			t.Fatal("lost ACK retry changed original Gov event")
		}
		return ack, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := resume.Reconcile(ctx, a.TenantID, a.ExecutionID)
	if err != nil || delivered.GPURelease.Outbox.DeliveredAt == nil {
		t.Fatal("durable Gov ACK missing", err)
	}
	replay, err := lifecycle.New(open()).RecordGPUReleaseACK(ctx, a.TenantID, a.ExecutionID, outbox.Event.ReleaseEventID, ack)
	if err != nil || replay.OwnerRevision != delivered.OwnerRevision || !reflect.DeepEqual(replay.GPURelease, delivered.GPURelease) {
		t.Fatal("ACK replay rewrote durable facts", err)
	}
	pending, err = lifecycle.New(open()).ListPendingClosures(ctx, binding, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatal("acknowledged GPU no-dispatch release remained pending", pending, err)
	}
	if lifecycle.ValidGPUReleaseFacts(accepted.Execution, replay, time.Now().UTC()) != nil {
		t.Fatal("persisted release failed original-admission read validation")
	}
	if _, err := repository.GetRuntime(ctx, uuid.NewString(), a.ExecutionID); !errors.Is(err, biz.ErrExecutionNotFound) {
		t.Fatal("cross-tenant release read", err)
	}
	// The independent SQL anchors prevent an attacker from replacing the whole
	// self-consistent JSON scope and recomputing its JSON hash.
	if _, err := pool.Exec(ctx, "UPDATE modeldev_execution_runtimes SET facts=jsonb_set(facts,'{GPURelease,scope,close_operation_id}',to_jsonb($3::text)) WHERE tenant_id=$1::uuid AND execution_id=$2::uuid", a.TenantID, a.ExecutionID, uuid.NewString()); err == nil {
		t.Fatal("SQL allowed original scope mutation")
	}
	if _, err := pool.Exec(ctx, "UPDATE modeldev_execution_runtimes SET gpu_release_sealed_at=gpu_release_sealed_at+interval '1 second' WHERE tenant_id=$1::uuid AND execution_id=$2::uuid", a.TenantID, a.ExecutionID); err == nil {
		t.Fatal("SQL allowed seal renewal")
	}
	if _, err := pool.Exec(ctx, "UPDATE modeldev_execution_runtimes SET facts=jsonb_set(facts,'{GPURelease,outbox,event,release_event_id}',to_jsonb($3::text)) WHERE tenant_id=$1::uuid AND execution_id=$2::uuid", a.TenantID, a.ExecutionID, uuid.NewString()); err == nil {
		t.Fatal("SQL allowed release-event replacement")
	}
	if _, err := pool.Exec(ctx, "UPDATE modeldev_execution_runtimes SET facts=jsonb_set(facts,'{GPURelease,outbox,ack}','null'::jsonb) WHERE tenant_id=$1::uuid AND execution_id=$2::uuid", a.TenantID, a.ExecutionID); err == nil {
		t.Fatal("SQL allowed acknowledged release to become pending again")
	}
	t.Log("GPU_RELEASE_PERSISTENCE PASS: real original admission/Stop/no-dispatch seal, missing sources stay unknown, four-field outbox survives reconnect and lost ACK, immutable SQL scope/event anchors")
}

func TestGPUReleaseReadValidationRejectsForgedOriginalScopeAndEvidence(t *testing.T) {
	a := gpuReleaseAdmission(t)
	execution := biz.Execution{Admission: a}
	now := time.Now().UTC()
	closed := now.Add(-time.Minute)
	scope := biz.GPUReleaseScope{Ref: biz.GPUReleaseRef{TenantID: a.TenantID, OwnerService: biz.GPUOwnerService, ResourceID: a.ExecutionID, CreateOperationID: a.OperationID}, CloseOperationID: uuid.NewString(), ClusterID: a.Snapshot.Environment.ClusterID, SealedAt: closed.Add(time.Second), Pods: []biz.GPUReleasePodRef{}}
	hash, err := biz.GPUReleaseScopeSHA256(scope)
	if err != nil {
		t.Fatal(err)
	}
	base := biz.ExecutionRuntime{CloseGeneration: 1, CloseOperationID: scope.CloseOperationID, CloseReason: "USER_STOP", ClosedAt: &closed, CloseEvidence: &biz.ManagedCloseEvidence{NoDispatch: true}, GPURelease: &biz.GPUReleaseState{Scope: scope, ScopeSHA256: hash, NoDispatch: true}}
	if err := lifecycle.ValidGPUReleaseFacts(execution, base, now); err != nil {
		t.Fatal("valid isolated scope", err)
	}
	for name, mutate := range map[string]func(*biz.ExecutionRuntime){
		"create-is-close": func(s *biz.ExecutionRuntime) {
			s.CloseOperationID = a.OperationID
			s.GPURelease.Scope.CloseOperationID = a.OperationID
		},
		"foreign-ref":  func(s *biz.ExecutionRuntime) { s.GPURelease.Scope.Ref.TenantID = uuid.NewString() },
		"renewed-seal": func(s *biz.ExecutionRuntime) { s.GPURelease.Scope.SealedAt = now.Add(time.Second) },
		"invented-pod": func(s *biz.ExecutionRuntime) {
			s.GPURelease.Scope.Pods = append(s.GPURelease.Scope.Pods, biz.GPUReleasePodRef{ClusterID: scope.ClusterID, Namespace: a.Snapshot.Environment.NamespaceName, Name: "made-up", UID: "anticipated", ContainerName: "node"})
		},
		"forged-positive": func(s *biz.ExecutionRuntime) {
			v := releasedObservation(scope, now)
			v.Issues = []string{"SOURCE_UNKNOWN"}
			s.GPURelease.Observation = &v
		},
		"unknown-outbox": func(s *biz.ExecutionRuntime) {
			s.GPURelease.Observation = &biz.GPUReleaseObservation{Ref: scope.Ref, Finding: "RELEASE_UNKNOWN", EvidenceID: uuid.NewString(), EvidenceDigest: strings.Repeat("a", 64)}
			s.GPURelease.Outbox = &biz.GPUReleaseOutbox{Event: biz.GPUResourceReleasedEvent{ReleaseEventID: uuid.NewString(), CreateOperationID: a.OperationID, ResourceID: a.ExecutionID, Reason: "RESOURCE_RELEASED"}, CreatedAt: now}
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var changed biz.ExecutionRuntime
			_ = json.Unmarshal(raw, &changed)
			mutate(&changed)
			changed.GPURelease.ScopeSHA256, _ = biz.GPUReleaseScopeSHA256(changed.GPURelease.Scope)
			if lifecycle.ValidGPUReleaseFacts(execution, changed, now) == nil {
				t.Fatal("forged persisted facts accepted")
			}
		})
	}
}

// This focused projection test follows readRuntime's separate committed-close
// and cleanup graph validation. Historical replacements remain in the scope.
func TestGPUReleaseScopeRetainsEveryOriginalAndReplacementPodUID(t *testing.T) {
	a := gpuReleaseAdmission(t)
	execution := biz.Execution{Admission: a}
	now := time.Now().UTC()
	closed := now.Add(-time.Minute)
	cleaned := closed.Add(time.Second)
	sealed := cleaned.Add(time.Second)
	zero := int32(0)
	handle := biz.TrainingHandle{NamespaceUID: a.Snapshot.Environment.NamespaceUID, TrainJobUID: "original-train", PVCUID: "original-workspace"}
	state := biz.ExecutionRuntime{CloseGeneration: 1, CloseOperationID: uuid.NewString(), CloseReason: "USER_STOP", ClosedAt: &closed, CloseEvidence: &biz.ManagedCloseEvidence{}, Training: &biz.TrainingPlan{}, TrainingHandle: &handle, Observation: &biz.TrainingRuntimeObservation{Handle: handle, WritersAbsent: true, Resources: []biz.RuntimeResource{{APIVersion: "v1", Kind: "Pod", Namespace: a.Snapshot.Environment.NamespaceName, Name: "worker-node", UID: "z-replacement-uid", Terminal: true, ExitCode: &zero}, {APIVersion: "v1", Kind: "Pod", Namespace: a.Snapshot.Environment.NamespaceName, Name: "worker-node", UID: "a-original-uid", Terminal: true, ExitCode: &zero}}}, ComputeCleanup: &biz.ComputeCleanupReceipt{CleanupReceipt: biz.CleanupReceipt{Phase: "APPLIED", CompletedAt: cleaned}}, StepExitCleanup: &biz.StepExitCleanupReceipt{CompletedAt: &cleaned}}
	scope := biz.GPUReleaseScope{Ref: biz.GPUReleaseRef{TenantID: a.TenantID, OwnerService: biz.GPUOwnerService, ResourceID: a.ExecutionID, CreateOperationID: a.OperationID}, CloseOperationID: state.CloseOperationID, ClusterID: a.Snapshot.Environment.ClusterID, SealedAt: sealed, Pods: []biz.GPUReleasePodRef{{ClusterID: a.Snapshot.Environment.ClusterID, Namespace: a.Snapshot.Environment.NamespaceName, Name: "worker-node", UID: "a-original-uid", ContainerName: "node"}, {ClusterID: a.Snapshot.Environment.ClusterID, Namespace: a.Snapshot.Environment.NamespaceName, Name: "worker-node", UID: "z-replacement-uid", ContainerName: "node"}}}
	hash, err := biz.GPUReleaseScopeSHA256(scope)
	if err != nil {
		t.Fatal(err)
	}
	state.GPURelease = &biz.GPUReleaseState{Scope: scope, ScopeSHA256: hash}
	if lifecycle.ValidGPUReleaseFacts(execution, state, now) != nil {
		t.Fatal("full original Pod history rejected")
	}
	state.GPURelease.Scope.Pods = state.GPURelease.Scope.Pods[1:]
	state.GPURelease.ScopeSHA256, _ = biz.GPUReleaseScopeSHA256(state.GPURelease.Scope)
	if lifecycle.ValidGPUReleaseFacts(execution, state, now) == nil {
		t.Fatal("omitting original replaced Pod became a complete scope")
	}
}
