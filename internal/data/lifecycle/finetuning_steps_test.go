package lifecycle_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func finetuningRuntimeFixture(t *testing.T, lifetime time.Duration, edits ...func(*biz.Admission)) (func() *pgxpool.Pool, biz.Admission, biz.RunAuthorityCandidate, biz.FinetuningStepBinding) {
	t.Helper()
	open := postgres.Prepare(t)
	snapshot := conformance.FinetuningSnapshotV1()
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = accepted.Add(lifetime)
	intent := conformance.FinetuningIntentV1()
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash := ""
	admission := biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:synthetic-fixture", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: accepted}
	for _, edit := range edits {
		edit(&admission)
	}
	admission = admissionfixture.Bind(t, admission)
	snapshot = admission.Snapshot
	pool := open()
	ctx := context.Background()
	if _, err := execution.New(pool).Accept(ctx, admission); err != nil {
		t.Fatal(err)
	}
	reserved, err := submission.New(pool).Reserve(ctx, biz.PipelineDispatchRequest{Admission: admission, Owner: biz.PipelineOwnerConfiguration{Reference: "finetuning-synthetic-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/managed-root"}})
	if err != nil || reserved.SendPermit == nil {
		t.Fatal("dispatch", err)
	}
	authority := biz.RunAuthorityCandidate{TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID, SpecHash: admission.SpecHash, AttemptID: reserved.Dispatch.AttemptID, PlanHash: reserved.Dispatch.PlanHash, RunID: "55555555-6666-4777-8888-999999999999", NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: "finetuning-fixture-workflow", WorkflowUID: "cccccccc-dddd-4eee-8fff-111111111111"}
	if _, err := submission.New(pool).BindRunAuthority(ctx, authority); err != nil {
		t.Fatal(err)
	}
	repository := lifecycle.New(pool)
	w := snapshot.Workspace
	workspace := biz.WorkspaceBinding{Mode: w.Mode, NamespaceName: authority.NamespaceName, NamespaceUID: authority.NamespaceUID, PVCName: biz.ExpectedWorkspacePVCName(admission.ExecutionID), PVCUID: "dddddddd-eeee-4fff-8111-222222222222", InputSubpath: w.InputSubpath, TrainingSubpath: w.TrainingSubpath, ReportsSubpath: w.ReportsSubpath, PublicationSubpath: w.PublicationSubpath, PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256}
	startManagedRoleV21(t, repository, authority, "prepare")
	if _, _, err := repository.RecordPrepared(ctx, authority, workspace); err != nil {
		t.Fatal(err)
	}
	training, err := reserveTrainingV21(t, repository, ctx, authority)
	if err != nil || !training.SendPermit {
		t.Fatal(err)
	}
	handle := biz.TrainingHandle{NamespaceUID: authority.NamespaceUID, PVCUID: workspace.PVCUID, TrainJobUID: "eeeeeeee-ffff-4111-8222-333333333333"}
	if _, err := repository.RecordTrainingHandle(ctx, authority, handle); err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	observation := biz.TrainingRuntimeObservation{Handle: handle, Outcome: "SUCCEEDED", WritersAbsent: true, ObservedAt: time.Now().UTC(), Resources: []biz.RuntimeResource{
		{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: authority.NamespaceName, Name: training.State.Training.Name, UID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true},
		{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: authority.NamespaceName, Name: "training-jobset", UID: "training-jobset-uid", OwnerUID: handle.TrainJobUID, APIObjectPresent: true, Terminal: true},
		{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "training-pod", UID: "training-pod-uid", OwnerUID: "training-jobset-uid", APIObjectPresent: true, Terminal: true, ExitCode: &zero},
	}}
	if _, err := repository.RecordTrainingObservation(ctx, authority, observation); err != nil {
		t.Fatal(err)
	}
	binding := biz.FinetuningStepBinding{Role: "export", SpecHash: authority.SpecHash, RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, TaskID: "export-task-id", PodName: "export-original-pod", PodUID: "99999999-aaaa-4bbb-8ccc-dddddddddddd"}
	return open, admission, authority, binding
}

func applyFinetuningStop(ctx context.Context, pool *pgxpool.Pool, admission biz.Admission) error {
	_, err := execution.New(pool).ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID, SpecHash: admission.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: admission.Actor})
	return err
}

// Pause only an isolated test schema's real SQL INSERT/UPDATE. The production
// repository remains unchanged and retains its real row lock until COMMIT.
func finetuningCommitGate(t *testing.T, pool *pgxpool.Pool, table string) (func(), func()) {
	t.Helper()
	if table != "modeldev_execution_runtimes" && table != "modeldev_close_intents" {
		t.Fatal("invalid test gate")
	}
	ctx := context.Background()
	runtime := pool.Config().ConnConfig
	schema := runtime.RuntimeParams["search_path"]
	admin, err := pgx.ParseConfig(os.Getenv("CPU_P01_TEST_DATABASE_ADMIN_URL"))
	if err != nil || !strings.HasPrefix(schema, "cpu04_") || admin.Host != runtime.Host || admin.Port != runtime.Port || admin.Database != runtime.Database || admin.User == runtime.User {
		t.Fatal("gate requires isolated test owner")
	}
	owner, err := pgx.ConnectConfig(ctx, admin)
	if err != nil {
		t.Fatal("test gate connection", err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	digest := sha256.Sum256([]byte(schema + table))
	key := int64(binary.BigEndian.Uint64(digest[:8]) & ((1 << 63) - 1))
	function := pgx.Identifier{schema, "finetuning_commit_gate"}.Sanitize()
	qualifiedTable := pgx.Identifier{schema, table}.Sanitize()
	if _, err := owner.Exec(ctx, "CREATE FUNCTION "+function+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock("+keyDecimal(key)+"); RETURN NEW; END $$"); err != nil {
		t.Fatal("test gate function", err)
	}
	if _, err := owner.Exec(ctx, "CREATE TRIGGER finetuning_commit_gate BEFORE INSERT OR UPDATE ON "+qualifiedTable+" FOR EACH ROW EXECUTE FUNCTION "+function+"()"); err != nil {
		t.Fatal("test gate trigger", err)
	}
	if _, err := owner.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			if _, err := owner.Exec(ctx, "SELECT pg_advisory_unlock($1)", key); err != nil {
				t.Error(err)
			}
			released = true
		}
	}
	t.Cleanup(release)
	await := func() {
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var blocked bool
			if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=$1::oid AND objid=$2::oid AND NOT granted)", uint32(uint64(key)>>32), uint32(key)).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				return
			}
			select {
			case <-deadline.C:
				t.Fatal("real transaction did not reach test gate")
			case <-ticker.C:
			}
		}
	}
	return await, release
}

func keyDecimal(value int64) string { return strconv.FormatInt(value, 10) }

func TestFinetuningStepStopFirstProducesNoStartPermit(t *testing.T) {
	open, admission, authority, binding := finetuningRuntimeFixture(t, time.Hour)
	stopPool, startPool := open(), open()
	await, release := finetuningCommitGate(t, stopPool, "modeldev_close_intents")
	stopDone := make(chan error, 1)
	go func() { stopDone <- applyFinetuningStop(context.Background(), stopPool, admission) }()
	await()
	startDone := make(chan biz.FinetuningStepReservation, 1)
	startError := make(chan error, 1)
	go func() {
		r, e := lifecycle.New(startPool).ReserveFinetuningStep(context.Background(), authority, binding)
		startDone <- r
		startError <- e
	}()
	release()
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	if result, err := <-startDone, <-startError; result.SendPermit || !errors.Is(err, biz.ErrPipelineDispatchBlocked) {
		t.Fatal("stop-first granted compute", result, err)
	}
	state, err := lifecycle.New(open()).GetRuntime(context.Background(), authority.TenantID, authority.ExecutionID)
	if err != nil || len(state.FinetuningSteps) != 0 {
		t.Fatal("rejected first start persisted", err)
	}
}

func TestFinetuningStepStartFirstSurvivesStopAndReplayWithoutPermit(t *testing.T) {
	open, admission, authority, binding := finetuningRuntimeFixture(t, time.Hour)
	startPool, stopPool := open(), open()
	await, release := finetuningCommitGate(t, startPool, "modeldev_execution_runtimes")
	startDone := make(chan biz.FinetuningStepReservation, 1)
	startError := make(chan error, 1)
	go func() {
		r, e := lifecycle.New(startPool).ReserveFinetuningStep(context.Background(), authority, binding)
		startDone <- r
		startError <- e
	}()
	await()
	stopDone := make(chan error, 1)
	go func() { stopDone <- applyFinetuningStop(context.Background(), stopPool, admission) }()
	release()
	first, err := <-startDone, <-startError
	if err != nil || !first.SendPermit {
		t.Fatal("start-first lost original permit", err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	replay, err := lifecycle.New(open()).ReserveFinetuningStep(context.Background(), authority, binding)
	if err != nil || replay.SendPermit || !replay.Start.AdmittedAt.Equal(first.Start.AdmittedAt) || !replay.Start.DeadlineAt.Equal(first.Start.DeadlineAt) || replay.State.CloseGeneration == 0 {
		t.Fatal("replay renewed in-flight computation", err)
	}
	wrong := binding
	wrong.PodUID = "aaaaaaaa-aaaa-4bbb-8ccc-dddddddddddd"
	if _, err := lifecycle.New(open()).ReserveFinetuningStep(context.Background(), authority, wrong); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("another Pod acquired original start", err)
	}
	wrong = binding
	wrong.TaskID = "another-task"
	if _, err := lifecycle.New(open()).ReserveFinetuningStep(context.Background(), authority, wrong); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("another Task acquired original start", err)
	}
}

func TestFinetuningStepBudgetAndResultAreOriginalAcrossReconnect(t *testing.T) {
	open, admission, authority, binding := finetuningRuntimeFixture(t, 2*time.Second)
	first, err := lifecycle.New(open()).ReserveFinetuningStep(context.Background(), authority, binding)
	if err != nil || !first.SendPermit {
		t.Fatal(err)
	}
	if first.Start.DeadlineAt.After(first.Start.AdmittedAt.Add(300 * time.Second)) {
		t.Fatal("step budget exceeded")
	}
	if !first.Start.DeadlineAt.Equal(admission.Snapshot.DeadlineAt) {
		t.Fatal("step budget did not retain original execution deadline")
	}
	var now time.Time
	clockPool := open()
	if err := clockPool.QueryRow(context.Background(), "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	report := biz.FinetuningStepResult{StartedAt: first.Start.AdmittedAt, FinishedAt: now.UTC(), ExitCode: 0, ManifestRelativePath: "reports/export/merged_manifest.json", ManifestBytes: 128, ManifestSHA256: strings.Repeat("a", 64)}
	state, replayed, err := lifecycle.New(open()).RecordFinetuningStepResult(context.Background(), authority, binding, report)
	if err != nil || replayed || state.FinetuningSteps[0].Result == nil {
		t.Fatal(err)
	}
	for {
		if err := clockPool.QueryRow(context.Background(), "SELECT clock_timestamp()").Scan(&now); err != nil {
			t.Fatal(err)
		}
		if !now.Before(first.Start.DeadlineAt) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	replay, err := lifecycle.New(open()).ReserveFinetuningStep(context.Background(), authority, binding)
	if err != nil || replay.SendPermit || !replay.Start.DeadlineAt.Equal(first.Start.DeadlineAt) || replay.Start.Result == nil {
		t.Fatal("expired original start renewed", err)
	}
	verify := binding
	verify.Role = "verify"
	verify.TaskID = "verify-task-id"
	verify.PodName = "verify-original-pod"
	verify.PodUID = "88888888-aaaa-4bbb-8ccc-dddddddddddd"
	if result, err := lifecycle.New(open()).ReserveFinetuningStep(context.Background(), authority, verify); result.SendPermit || !errors.Is(err, biz.ErrPipelineDispatchBlocked) {
		t.Fatal("deadline allowed new compute", err)
	}
	if _, replayed, err := lifecycle.New(open()).RecordFinetuningStepResult(context.Background(), authority, binding, report); err != nil || !replayed {
		t.Fatal("original report replay drifted", err)
	}
}

func TestFinetuningStepMultiOwnerGrantsEachRoleExactlyOnce(t *testing.T) {
	open, admission, authority, binding := finetuningRuntimeFixture(t, time.Hour)
	ctx := context.Background()
	startTogether := make(chan struct{})
	results := make(chan biz.FinetuningStepReservation, 4)
	errorsFound := make(chan error, 4)
	var owners sync.WaitGroup
	for range 4 {
		pool := open()
		owners.Add(1)
		go func() {
			defer owners.Done()
			<-startTogether
			r, e := lifecycle.New(pool).ReserveFinetuningStep(ctx, authority, binding)
			results <- r
			errorsFound <- e
		}()
	}
	close(startTogether)
	owners.Wait()
	var original biz.FinetuningStepStart
	permits := 0
	for range 4 {
		if err := <-errorsFound; err != nil {
			t.Fatal(err)
		}
		r := <-results
		if r.SendPermit {
			permits++
			original = r.Start
		}
	}
	if permits != 1 {
		t.Fatal("multiple owners granted", permits)
	}
	want := original.AdmittedAt.Add(time.Duration(admission.Snapshot.Timing.StepTimeoutSeconds) * time.Second)
	if !original.DeadlineAt.Equal(want) {
		t.Fatal("step budget was not frozen from DB admission time")
	}
	var now time.Time
	if err := open().QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	report := biz.FinetuningStepResult{StartedAt: original.AdmittedAt, FinishedAt: now.UTC(), ExitCode: 0, ManifestRelativePath: "reports/export/merged_manifest.json", ManifestBytes: 128, ManifestSHA256: strings.Repeat("a", 64)}
	if _, _, err := lifecycle.New(open()).RecordFinetuningStepResult(ctx, authority, binding, report); err != nil {
		t.Fatal(err)
	}
	verify := binding
	verify.Role = "verify"
	verify.TaskID = "verify-task-id"
	verify.PodName = "verify-original-pod"
	verify.PodUID = "88888888-aaaa-4bbb-8ccc-dddddddddddd"
	first, err := lifecycle.New(open()).ReserveFinetuningStep(ctx, authority, verify)
	if err != nil || !first.SendPermit || len(first.State.FinetuningSteps) != 2 {
		t.Fatal("verify first start", err)
	}
	replay, err := lifecycle.New(open()).ReserveFinetuningStep(ctx, authority, verify)
	if err != nil || replay.SendPermit || !replay.Start.AdmittedAt.Equal(first.Start.AdmittedAt) {
		t.Fatal("verify replay", err)
	}
	report.ManifestSHA256 = strings.Repeat("b", 64)
	if _, _, err := lifecycle.New(open()).RecordFinetuningStepResult(ctx, authority, binding, report); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("another result replaced original", err)
	}
}

func TestFinetuningStepUnknownStartRequiresOriginalPodExitBeforeClosed(t *testing.T) {
	open, admission, authority, binding := finetuningRuntimeFixture(t, time.Hour)
	ctx := context.Background()
	repository := lifecycle.New(open())
	first, err := repository.ReserveFinetuningStep(ctx, authority, binding)
	if err != nil || !first.SendPermit {
		t.Fatal(err)
	}
	if err := applyFinetuningStop(ctx, open(), admission); err != nil {
		t.Fatal(err)
	}
	state, err := repository.GetRuntime(ctx, authority.TenantID, authority.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: time.Now().UTC()}
	for _, role := range []string{"prepare", "train-wait", "export", "verify", "collect", "publish", "register"} {
		evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "completed-" + role, UID: "completed-" + role + "-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
	}
	if _, err := repository.ConfirmRuntimeClosed(ctx, authority, state.CloseGeneration, *state.Observation, evidence); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatal("unresolved admitted Pod incorrectly CLOSED", err)
	}
	evidence.Resources[2].Name = binding.PodName
	evidence.Resources[2].UID = binding.PodUID
	closed, err := repository.ConfirmRuntimeClosed(ctx, authority, state.CloseGeneration, *state.Observation, evidence)
	if err != nil || closed.ClosedAt == nil || closed.FinetuningSteps[0].Result != nil {
		t.Fatal("actual original entrance termination did not close unknown spawn", err)
	}
	if reloaded, err := lifecycle.New(open()).GetRuntime(ctx, authority.TenantID, authority.ExecutionID); err != nil || reloaded.ClosedAt == nil {
		t.Fatal("restart lost original closure", err)
	}
}
