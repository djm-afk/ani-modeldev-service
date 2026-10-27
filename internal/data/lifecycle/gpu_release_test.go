package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

type releaseReporterFunc func(context.Context, biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error)

func (f releaseReporterFunc) ReportGPUResourceReleased(ctx context.Context, event biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error) {
	return f(ctx, event)
}

func TestGPUReleaseSealCommitsOriginalOutboxWithoutACC(t *testing.T) {
	open := postgres.Prepare(t)
	ctx := context.Background()
	a := gpuReleaseAdmission(t)
	if _, err := execution.New(open()).Accept(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.New(open()).ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: a.TenantID, ExecutionID: a.ExecutionID, OperationID: a.OperationID, SpecHash: a.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: a.Actor}); err != nil {
		t.Fatal(err)
	}
	closed, err := lifecycle.New(open()).CloseUndispatched(ctx, a.TenantID, a.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := lifecycle.New(open()).SealGPURelease(ctx, a.TenantID, a.ExecutionID)
	if err != nil || sealed.GPURelease == nil || sealed.GPURelease.Outbox == nil {
		t.Fatal("owner close did not commit scope and outbox together", err)
	}
	if sealed.GPURelease.Scope.CloseOperationID != closed.CloseOperationID || sealed.GPURelease.Outbox.Event.CreateOperationID != a.OperationID || sealed.GPURelease.Outbox.Event.ResourceID != a.ExecutionID || sealed.GPURelease.Outbox.Event.Reason != "RESOURCE_RELEASED" {
		t.Fatal("owner event lost original identity or minted non-owner evidence")
	}
	durable, err := lifecycle.New(open()).GetRuntime(ctx, a.TenantID, a.ExecutionID)
	if err != nil || !reflect.DeepEqual(durable.GPURelease, sealed.GPURelease) {
		t.Fatal("scope and event did not survive repository reconnect", err)
	}
}

func closedGPURelease(t *testing.T, open func() *pgxpool.Pool) (biz.Admission, biz.ExecutionRuntime) {
	t.Helper()
	ctx := context.Background()
	a := gpuReleaseAdmission(t)
	if _, err := execution.New(open()).Accept(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.New(open()).ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: a.TenantID, ExecutionID: a.ExecutionID, OperationID: a.OperationID, SpecHash: a.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: a.Actor}); err != nil {
		t.Fatal(err)
	}
	closed, err := lifecycle.New(open()).CloseUndispatched(ctx, a.TenantID, a.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	return a, closed
}

// Fail the actual COMMIT, after the real owner lock/revision/anchor writes.
// This fault exists only in this test's random, versioned PostgreSQL schema.
func rejectGPUReleaseCommit(t *testing.T, pool *pgxpool.Pool) func() {
	t.Helper()
	runtime := pool.Config().ConnConfig
	schema := runtime.RuntimeParams["search_path"]
	admin, err := pgx.ParseConfig(os.Getenv("CPU_P01_TEST_DATABASE_ADMIN_URL"))
	if err != nil || !strings.HasPrefix(schema, "cpu04_") || admin.Host != runtime.Host || admin.Port != runtime.Port || admin.Database != runtime.Database || admin.User == runtime.User {
		t.Fatal("GPU release fault requires isolated test migration role")
	}
	ctx := context.Background()
	owner, err := pgx.ConnectConfig(ctx, admin)
	if err != nil {
		t.Fatal("GPU release fault migration connection failed")
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	function := pgx.Identifier{schema, "reject_gpu_release_commit"}.Sanitize()
	table := pgx.Identifier{schema, "modeldev_execution_runtimes"}.Sanitize()
	if _, err := owner.Exec(ctx, "CREATE FUNCTION "+function+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected GPU release commit failure' USING ERRCODE='23514'; END $$"); err != nil {
		t.Fatal("GPU release fault function setup failed")
	}
	if _, err := owner.Exec(ctx, "CREATE CONSTRAINT TRIGGER reject_gpu_release_commit AFTER UPDATE ON "+table+" DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION "+function+"()"); err != nil {
		t.Fatal("GPU release fault trigger setup failed")
	}
	removed := false
	remove := func() {
		t.Helper()
		if !removed {
			if _, err := owner.Exec(context.Background(), "DROP TRIGGER reject_gpu_release_commit ON "+table+"; DROP FUNCTION "+function+"()"); err != nil {
				t.Error("GPU release fault cleanup failed")
			}
			removed = true
		}
	}
	t.Cleanup(remove)
	return remove
}

func TestGPUReleaseFailedCommitLeavesNoScopeOrEventAndDoesNotDeliver(t *testing.T) {
	open := postgres.Prepare(t)
	ctx := context.Background()
	a, closed := closedGPURelease(t, open)
	repository := lifecycle.New(open())
	remove := rejectGPUReleaseCommit(t, open())
	reconciler, err := biz.NewGPUReleaseReconciler(repository, releaseReporterFunc(func(context.Context, biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error) {
		t.Fatal("uncommitted event reached Gov")
		return biz.GPUReleaseACK{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, a.TenantID, a.ExecutionID); !errors.Is(err, biz.ErrPersistence) {
		t.Fatal("real failed COMMIT was not reported", err)
	}
	durable, err := lifecycle.New(open()).GetRuntime(ctx, a.TenantID, a.ExecutionID)
	if err != nil || durable.GPURelease != nil || durable.OwnerRevision != closed.OwnerRevision {
		t.Fatal("failed scope+event commit left partial state or advanced revision", err)
	}
	remove()
	sealed, err := repository.SealGPURelease(ctx, a.TenantID, a.ExecutionID)
	if err != nil || sealed.GPURelease == nil || sealed.GPURelease.Outbox == nil {
		t.Fatal("normal retry could not commit owner event after fault removal", err)
	}
}

func TestGPUReleaseConcurrentSealReusesOneCommittedEvent(t *testing.T) {
	open := postgres.Prepare(t)
	a, closed := closedGPURelease(t, open)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repository := lifecycle.New(open())
	type result struct {
		state biz.ExecutionRuntime
		err   error
	}
	const workers = 6
	start := make(chan struct{})
	results := make(chan result, workers)
	for range workers {
		go func() {
			<-start
			state, err := repository.SealGPURelease(ctx, a.TenantID, a.ExecutionID)
			results <- result{state: state, err: err}
		}()
	}
	close(start)
	var original *biz.GPUReleaseState
	for range workers {
		got := <-results
		if got.err != nil || got.state.GPURelease == nil || got.state.GPURelease.Outbox == nil {
			t.Fatal("concurrent owner seal failed", got.err)
		}
		if original == nil {
			original = got.state.GPURelease
		}
		if !reflect.DeepEqual(got.state.GPURelease, original) || got.state.OwnerRevision != closed.OwnerRevision+1 {
			t.Fatal("concurrent seal minted another event or advanced the owner again")
		}
	}
	durable, err := lifecycle.New(open()).GetRuntime(ctx, a.TenantID, a.ExecutionID)
	if err != nil || !reflect.DeepEqual(durable.GPURelease, original) {
		t.Fatal("concurrent calls did not retain one original committed event", err)
	}
}

func TestGPUReleaseScopeOnlyIsRejectedWithoutConversion(t *testing.T) {
	open := postgres.Prepare(t)
	ctx := context.Background()
	a, closed := closedGPURelease(t, open)
	sealed := time.Now().UTC().Truncate(time.Microsecond)
	scope := biz.GPUReleaseScope{Ref: biz.GPUReleaseRef{TenantID: a.TenantID, OwnerService: biz.GPUOwnerService, ResourceID: a.ExecutionID, CreateOperationID: a.OperationID}, CloseOperationID: closed.CloseOperationID, ClusterID: a.Snapshot.Environment.ClusterID, SealedAt: sealed, Pods: []biz.GPUReleasePodRef{}}
	hash, err := biz.GPUReleaseScopeSHA256(scope)
	if err != nil {
		t.Fatal(err)
	}
	closed.GPURelease = &biz.GPUReleaseState{Scope: scope, ScopeSHA256: hash, NoDispatch: true}
	raw, err := json.Marshal(closed)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the incompatible historical shape only at the isolated DB boundary.
	// The product exposes no method for creating or repairing scope-only facts.
	if _, err := open().Exec(ctx, "UPDATE modeldev_execution_runtimes SET facts=$3::jsonb,gpu_release_sealed_at=$4,gpu_release_scope_sha256=$5 WHERE tenant_id=$1::uuid AND execution_id=$2::uuid", a.TenantID, a.ExecutionID, raw, sealed, hash); err != nil {
		t.Fatal("scope-only fixture setup failed", err)
	}
	repository := lifecycle.New(open())
	if _, err := repository.GetRuntime(ctx, a.TenantID, a.ExecutionID); !errors.Is(err, biz.ErrPersistence) {
		t.Fatal("scope-only was accepted by the runtime reader", err)
	}
	if _, err := repository.SealGPURelease(ctx, a.TenantID, a.ExecutionID); !errors.Is(err, biz.ErrPersistence) || !strings.Contains(err.Error(), a.ExecutionID) {
		t.Fatal("scope-only was converted or lacked execution error context", err)
	}
}

// Persistence is the real restricted PostgreSQL repository, including the
// original admission, Stop fence, no-dispatch proof, seal and release outbox.
// Gov answers enter only at its explicit external boundary; ACC is independent.
func TestGPUReleaseNoDispatchOutboxAndACKSurviveReconnect(t *testing.T) {
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
	if original.Outbox == nil {
		t.Fatal("owner close did not persist an event without ACC evidence")
	}
	binding := biz.PipelineDispatchBinding{TenantID: a.TenantID, Environment: a.Snapshot.Environment, Owner: biz.PipelineOwnerConfiguration{Reference: "gpu-release-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/gpu-release"}}
	pending, err := lifecycle.New(open()).ListPendingClosures(ctx, binding, "", 10)
	if err != nil || len(pending) != 1 || pending[0] != a.ExecutionID {
		t.Fatal("restart scan lost pending GPU release", pending, err)
	}
	lostACK := errors.New("Gov applied release; transport lost ACK")
	reporter := releaseReporterFunc(func(ctx context.Context, event biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error) {
		durable, e := lifecycle.New(open()).GetRuntime(ctx, a.TenantID, a.ExecutionID)
		if e != nil || durable.GPURelease.Outbox == nil || durable.GPURelease.Outbox.Event != event {
			t.Fatal("Gov called before owner scope/outbox commit", e)
		}
		return biz.GPUReleaseACK{}, lostACK
	})
	reconciler, err := biz.NewGPUReleaseReconciler(lifecycle.New(open()), reporter)
	if err != nil {
		t.Fatal(err)
	}
	state, err := reconciler.Reconcile(ctx, a.TenantID, a.ExecutionID)
	if !errors.Is(err, lostACK) || state.GPURelease.Outbox == nil || state.GPURelease.Outbox.ACK != nil {
		t.Fatal("owner close did not commit release outbox before lost ACK", err)
	}
	outbox := *state.GPURelease.Outbox
	eventBytes, _ := json.Marshal(outbox.Event)
	var fields map[string]any
	_ = json.Unmarshal(eventBytes, &fields)
	if len(fields) != 4 || outbox.Event.CreateOperationID != a.OperationID || outbox.Event.ResourceID != a.ExecutionID || outbox.Event.Reason != "RESOURCE_RELEASED" {
		t.Fatal("Gov event lost its four-field original identity", string(eventBytes))
	}
	retried, err := lifecycle.New(open()).SealGPURelease(ctx, a.TenantID, a.ExecutionID)
	if err != nil || !reflect.DeepEqual(retried.GPURelease.Outbox, &outbox) {
		t.Fatal("lost ACK retry minted another event", err)
	}
	ack := biz.GPUReleaseACK{ReleaseEventID: outbox.Event.ReleaseEventID, CreateOperationID: a.OperationID, ResourceID: a.ExecutionID, Status: "ALREADY_APPLIED"}
	badACK := ack
	badACK.ResourceID = uuid.NewString()
	if _, err := repository.RecordGPUReleaseACK(ctx, a.TenantID, a.ExecutionID, outbox.Event.ReleaseEventID, badACK); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("foreign ACK accepted", err)
	}
	resume, err := biz.NewGPUReleaseReconciler(lifecycle.New(open()), releaseReporterFunc(func(_ context.Context, event biz.GPUResourceReleasedEvent) (biz.GPUReleaseACK, error) {
		if event != outbox.Event {
			t.Fatal("lost ACK retry changed original Gov event")
		}
		return ack, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	removeACKFault := rejectGPUReleaseCommit(t, open())
	if _, err := resume.Reconcile(ctx, a.TenantID, a.ExecutionID); !errors.Is(err, biz.ErrPersistence) {
		t.Fatal("real ACK commit failure was not reported", err)
	}
	stillPending, err := lifecycle.New(open()).GetRuntime(ctx, a.TenantID, a.ExecutionID)
	if err != nil || stillPending.GPURelease.Outbox.Event != outbox.Event || stillPending.GPURelease.Outbox.ACK != nil || stillPending.GPURelease.Outbox.DeliveredAt != nil {
		t.Fatal("failed ACK persistence changed event or marked delivered", err)
	}
	removeACKFault()
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
	t.Log("GPU_RELEASE_PERSISTENCE PASS: original admission/Stop/no-dispatch scope+outbox, no ACC dependency, four-field event survives reconnect and lost ACK, immutable SQL scope/event anchors")
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
	base.GPURelease.Outbox = &biz.GPUReleaseOutbox{Event: biz.GPUResourceReleasedEvent{ReleaseEventID: uuid.NewString(), CreateOperationID: a.OperationID, ResourceID: a.ExecutionID, Reason: "RESOURCE_RELEASED"}, CreatedAt: scope.SealedAt}
	if err := lifecycle.ValidGPUReleaseFacts(execution, base, now); err != nil {
		t.Fatal("valid isolated scope", err)
	}
	// Facts persisted before the ACC client removal may still carry the retired
	// diagnostic observation. It is ignored rather than reinterpreted as release
	// authority, so the original owner event stays valid.
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	facts, ok := legacy["GPURelease"].(map[string]any)
	if !ok {
		t.Fatal("GPU release facts not addressable")
	}
	facts["observation"] = map[string]any{"finding": "RELEASE_UNKNOWN", "evidence_id": uuid.NewString(), "issues": []string{"SOURCE_UNKNOWN"}}
	legacyRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var tolerant biz.ExecutionRuntime
	if err := json.Unmarshal(legacyRaw, &tolerant); err != nil {
		t.Fatal(err)
	}
	if lifecycle.ValidGPUReleaseFacts(execution, tolerant, now) != nil || tolerant.GPURelease.Outbox == nil {
		t.Fatal("retired observation blocked or altered the committed owner event")
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
		"scope-only": func(s *biz.ExecutionRuntime) { s.GPURelease.Outbox = nil },
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
	state.GPURelease.Outbox = &biz.GPUReleaseOutbox{Event: biz.GPUResourceReleasedEvent{ReleaseEventID: uuid.NewString(), CreateOperationID: a.OperationID, ResourceID: a.ExecutionID, Reason: "RESOURCE_RELEASED"}, CreatedAt: scope.SealedAt}
	if lifecycle.ValidGPUReleaseFacts(execution, state, now) != nil {
		t.Fatal("full original Pod history rejected")
	}
	state.GPURelease.Scope.Pods = state.GPURelease.Scope.Pods[1:]
	state.GPURelease.ScopeSHA256, _ = biz.GPUReleaseScopeSHA256(state.GPURelease.Scope)
	if lifecycle.ValidGPUReleaseFacts(execution, state, now) == nil {
		t.Fatal("omitting original replaced Pod became a complete scope")
	}
}
