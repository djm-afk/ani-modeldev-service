package biz_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
)

// The shared tenant pull Secret is ensured exactly once, at the creation
// boundary, with the frozen scope. ModelDev never writes the Secret itself and
// never receives credentials.
func TestManagedEnsureRequestsTheFrozenPullSecretScopeBeforeCreatingRuntime(t *testing.T) {
	f := newPullSecretRuntimeFixture(t)
	ensurer := &recordingPullSecretEnsurer{reference: f.pullSecretRef}
	runtime := f.runtime(t, ensurer)

	if _, err := runtime.Ensure(context.Background(), "current-authenticated-token", f.request); err != nil {
		t.Fatalf("ensure with a matching Secret reference: %v", err)
	}
	if len(ensurer.calls) != 1 {
		t.Fatalf("Secret must be ensured exactly once: %v", ensurer.calls)
	}
	call := ensurer.calls[0]
	snapshot := f.execution.Snapshot
	if call.tenant != f.execution.TenantID || call.cluster != snapshot.Environment.ClusterID ||
		call.namespace != snapshot.Environment.NamespaceName || call.namespaceUID != snapshot.Environment.NamespaceUID ||
		call.registry != snapshot.TrainingProgram.Image.RegistryAuthority {
		t.Fatalf("ensure used a scope other than the frozen one: %+v", call)
	}
	if f.trainer.created != 1 {
		t.Fatalf("runtime creation must follow the Secret: %d", f.trainer.created)
	}
	if got := f.trainer.plan.Snapshot.TrainingProgram.Image.PullSecretRef; got != f.pullSecretRef {
		t.Fatalf("created runtime lost the frozen Secret reference: %s", got)
	}
}

// A different Secret name would silently run against another tenant's Secret,
// so the answer is compared with the frozen reference and never trusted.
func TestManagedEnsureRejectsADifferentSecretReferenceWithoutCreatingRuntime(t *testing.T) {
	f := newPullSecretRuntimeFixture(t)
	ensurer := &recordingPullSecretEnsurer{reference: "ani-pull-" + strings.Repeat("0", 32)}
	runtime := f.runtime(t, ensurer)

	if _, err := runtime.Ensure(context.Background(), "current-authenticated-token", f.request); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatalf("a substituted Secret reference must fail closed: %v", err)
	}
	if f.trainer.created != 0 {
		t.Fatal("a mismatched Secret reference still created the runtime")
	}
}

// A training execution without the Resource ensurer fails closed instead of
// creating a Pod that cannot pull its image.
func TestManagedEnsureWithoutTheSecretEnsurerFailsClosed(t *testing.T) {
	f := newPullSecretRuntimeFixture(t)
	runtime := f.runtime(t, nil)

	if _, err := runtime.Ensure(context.Background(), "current-authenticated-token", f.request); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatalf("missing ensurer must fail closed: %v", err)
	}
	if f.trainer.created != 0 {
		t.Fatal("missing ensurer still created the runtime")
	}
}

// A Resource failure is not retried into a second Secret and never creates the
// workload.
func TestManagedEnsurePropagatesResourceFailureWithoutCreatingRuntime(t *testing.T) {
	f := newPullSecretRuntimeFixture(t)
	ensurer := &recordingPullSecretEnsurer{err: biz.ErrRuntimeNotReady}
	runtime := f.runtime(t, ensurer)

	if _, err := runtime.Ensure(context.Background(), "current-authenticated-token", f.request); !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatalf("Resource failure must propagate: %v", err)
	}
	if len(ensurer.calls) != 1 || f.trainer.created != 0 {
		t.Fatalf("failure must not retry or create: calls=%d created=%d", len(ensurer.calls), f.trainer.created)
	}
}

type pullSecretCall struct {
	tenant, cluster, namespace, namespaceUID, registry string
}

type recordingPullSecretEnsurer struct {
	reference string
	err       error
	calls     []pullSecretCall
}

func (e *recordingPullSecretEnsurer) EnsurePullSecret(_ context.Context, tenantID, clusterID, namespace, namespaceUID, registry string) (string, error) {
	e.calls = append(e.calls, pullSecretCall{tenant: tenantID, cluster: clusterID, namespace: namespace, namespaceUID: namespaceUID, registry: registry})
	if e.err != nil {
		return "", e.err
	}
	return e.reference, nil
}

type pullSecretRuntimeFixture struct {
	execution     biz.Execution
	request       biz.BeginManagedExecutionRequest
	state         biz.ExecutionRuntime
	pullSecretRef string
	trainer       *recordingTrainingRuntime
}

func newPullSecretRuntimeFixture(t *testing.T) *pullSecretRuntimeFixture {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = accepted.Add(time.Hour)
	admission := admissionfixture.Bind(t, biz.Admission{
		TenantID: snapshot.IdentityRef.TenantID, OperationID: snapshot.IdentityRef.OperationID,
		ExecutionID: snapshot.IdentityRef.ExecutionID, Actor: snapshot.IdentityRef.Actor,
		Intent: conformance.IntentV1(), Snapshot: snapshot, AcceptedAt: accepted,
	})
	execution := biz.Execution{Admission: admission}
	workspace := biz.WorkspaceBinding{
		Mode: snapshot.Workspace.Mode, NamespaceName: snapshot.Environment.NamespaceName,
		NamespaceUID: snapshot.Environment.NamespaceUID, PVCName: "md-" + admission.ExecutionID,
		PVCUID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", InputSubpath: snapshot.Workspace.InputSubpath,
		TrainingSubpath: snapshot.Workspace.TrainingSubpath, ReportsSubpath: snapshot.Workspace.ReportsSubpath,
		PublicationSubpath: snapshot.Workspace.PublicationSubpath,
		PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256,
	}
	return &pullSecretRuntimeFixture{
		execution: execution,
		request: biz.BeginManagedExecutionRequest{
			TenantID: admission.TenantID, ExecutionID: admission.ExecutionID, OperationID: admission.OperationID,
			SpecHash: admission.SpecHash,
			Association: biz.ManagedStepAssociation{
				RunID: "55555555-5555-4555-8555-555555555555", NamespaceName: snapshot.Environment.NamespaceName,
				NamespaceUID: snapshot.Environment.NamespaceUID, WorkflowName: "pull-secret-flow",
				WorkflowUID: "66666666-6666-4666-8666-666666666666", PodName: "pull-secret-train-wait",
				PodUID: "77777777-7777-4777-8777-777777777777",
			},
		},
		state: biz.ExecutionRuntime{
			Workspace:      &workspace,
			TrainingWindow: &biz.TrainingBudgetTiming{AdmittedAt: accepted, DeadlineAt: accepted.Add(time.Hour)},
			RuntimeTemplate: &biz.RuntimeCreation{
				Name: snapshot.RuntimePlan.Name, NamespaceUID: snapshot.Environment.NamespaceUID,
				ExpectedSpecSHA256: snapshot.RuntimePlan.ExpectedSpecSHA256,
			},
		},
		pullSecretRef: snapshot.TrainingProgram.Image.PullSecretRef,
		trainer:       &recordingTrainingRuntime{},
	}
}

func (f *pullSecretRuntimeFixture) runtime(t *testing.T, ensurer biz.TrainingImageEnsurer) *biz.ManagedRuntime {
	t.Helper()
	snapshot := f.execution.Snapshot
	plan := biz.PipelineDispatchPlan{
		TenantID: f.execution.TenantID, ExecutionID: f.execution.ExecutionID,
		OperationID: f.execution.OperationID, SpecHash: f.execution.SpecHash,
		Environment: snapshot.Environment,
	}
	dispatch := biz.PipelineDispatch{Plan: plan, AttemptID: "attempt", PlanHash: strings.Repeat("a", 64)}
	a := f.request.Association
	authority := biz.RunAuthority{RunAuthorityCandidate: biz.RunAuthorityCandidate{
		TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, OperationID: plan.OperationID,
		SpecHash: plan.SpecHash, AttemptID: dispatch.AttemptID, PlanHash: dispatch.PlanHash,
		RunID: a.RunID, NamespaceName: a.NamespaceName, NamespaceUID: a.NamespaceUID,
		WorkflowName: a.WorkflowName, WorkflowUID: a.WorkflowUID,
	}}
	calls := []string{}
	steps, err := biz.NewManagedSteps(
		&taskIdentityAuthority{dispatch: dispatch, authority: authority},
		&pullSecretExecutions{execution: f.execution},
		&taskIdentityWorkload{calls: &calls},
		&taskIdentityLegacyRuns{calls: &calls},
	)
	if err != nil {
		t.Fatal(err)
	}
	store := &pullSecretRuntimeStore{state: f.state, trainer: f.trainer}
	runtime, err := biz.NewManagedRuntime(steps, store, f.trainer, &pullSecretPreparedProof{}, &taskIdentityUnused{})
	if err != nil {
		t.Fatal(err)
	}
	return runtime.WithPullSecrets(ensurer)
}

type pullSecretExecutions struct {
	biz.ExecutionRepository
	execution biz.Execution
}

func (p *pullSecretExecutions) Get(context.Context, string, string) (biz.Execution, error) {
	return p.execution, nil
}

type pullSecretRuntimeStore struct {
	biz.ExecutionRuntimeRepository
	state   biz.ExecutionRuntime
	trainer *recordingTrainingRuntime
}

func (p *pullSecretRuntimeStore) GetRuntime(context.Context, string, string) (biz.ExecutionRuntime, error) {
	return p.state, nil
}

func (p *pullSecretRuntimeStore) ReserveRuntime(context.Context, biz.RunAuthorityCandidate) (biz.TrainingReservation, error) {
	return biz.TrainingReservation{State: p.state, SendPermit: true}, nil
}

func (p *pullSecretRuntimeStore) RecordRuntimeBinding(_ context.Context, _ biz.RunAuthorityCandidate, binding biz.RuntimeTemplateBinding) (biz.ExecutionRuntime, error) {
	p.state.RuntimeTemplate.Binding = &binding
	return p.state, nil
}

// ReserveTraining returns an already bound handle so the test stops at the
// creation boundary it is asserting, without a second TrainJob POST.
func (p *pullSecretRuntimeStore) ReserveTraining(context.Context, biz.RunAuthorityCandidate) (biz.TrainingReservation, error) {
	handle := biz.TrainingHandle{
		NamespaceUID: p.state.Workspace.NamespaceUID, TrainJobUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		PVCUID: p.state.Workspace.PVCUID,
	}
	plan := p.trainer.plan
	p.state.Training, p.state.TrainingHandle = &plan, &handle
	return biz.TrainingReservation{State: p.state, SendPermit: true}, nil
}

type pullSecretPreparedProof struct{}

func (*pullSecretPreparedProof) VerifyPrepared(context.Context, biz.Execution, biz.ManagedStepAssociation, biz.WorkspaceBinding) error {
	return nil
}

func (*pullSecretPreparedProof) VerifyPreparedCompletion(context.Context, biz.Execution, biz.ManagedStepAssociation, biz.WorkspaceBinding) error {
	return nil
}

type recordingTrainingRuntime struct {
	created int
	plan    biz.TrainingPlan
}

func (r *recordingTrainingRuntime) CreateRuntime(_ context.Context, plan biz.TrainingPlan) (biz.RuntimeTemplateBinding, error) {
	r.created++
	r.plan = plan
	return biz.RuntimeTemplateBinding{
		NamespaceUID: plan.Workspace.NamespaceUID, Name: plan.Snapshot.RuntimePlan.Name,
		UID: "99999999-9999-4999-8999-999999999999",
		ObservedSpecSHA256: plan.Snapshot.RuntimePlan.ExpectedSpecSHA256,
	}, nil
}

func (*recordingTrainingRuntime) FindRuntime(context.Context, biz.TrainingPlan) (biz.RuntimeTemplateBinding, error) {
	return biz.RuntimeTemplateBinding{}, biz.ErrTrainingNotFound
}

func (*recordingTrainingRuntime) CreateTraining(context.Context, biz.TrainingPlan) (biz.TrainingHandle, error) {
	return biz.TrainingHandle{}, biz.ErrTrainingNotFound
}

func (*recordingTrainingRuntime) FindTraining(context.Context, biz.TrainingPlan) (biz.TrainingHandle, error) {
	return biz.TrainingHandle{}, biz.ErrTrainingNotFound
}

func (*recordingTrainingRuntime) ObserveTraining(context.Context, biz.TrainingPlan, biz.TrainingHandle, []biz.RuntimeResource) (biz.TrainingRuntimeObservation, error) {
	return biz.TrainingRuntimeObservation{}, biz.ErrTrainingNotFound
}

func (*recordingTrainingRuntime) StopTraining(context.Context, biz.TrainingPlan, biz.TrainingHandle) error {
	return biz.ErrTrainingNotFound
}

var _ = cpup01.TrainingImageProtocolVersion
