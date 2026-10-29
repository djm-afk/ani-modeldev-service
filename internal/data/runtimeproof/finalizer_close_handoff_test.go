package runtimeproof_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestAuthenticatedFailureFinalizerDurablyHandsOffCloseWithoutNormalAuthority(t *testing.T) {
	f, pvc := capturedUnpreparedPVC(t)
	_, a := capturedFinalizerAuth(t)
	assertUnpreparedOwnerCloseVertical(t, f, pvc, a)
}

func assertUnpreparedOwnerCloseVertical(t *testing.T, f *ownerCloseFixture, pvc *unstructured.Unstructured, a biz.ManagedStepAssociation) {
	t.Helper()
	f.execution.AcceptedAt = time.Now().UTC().Truncate(time.Microsecond)
	f.execution.Snapshot.DeadlineAt = f.execution.AcceptedAt.Add(time.Hour)
	oldHash := f.execution.SpecHash
	f.execution.Admission = admissionfixture.Bind(t, f.execution.Admission)
	rawWorkflow, _ := json.Marshal(f.workflow.Object)
	rawWorkflow = []byte(strings.ReplaceAll(string(rawWorkflow), oldHash, f.execution.SpecHash))
	if err := json.Unmarshal(rawWorkflow, f.workflow); err != nil {
		t.Fatal(err)
	}
	open := postgres.Prepare(t)
	pool := open()
	ctx := context.Background()
	executions, dispatch := execution.New(pool), submission.New(pool)
	if _, err := executions.Accept(ctx, f.execution.Admission); err != nil {
		t.Fatal(err)
	}
	reserved, err := dispatch.Reserve(ctx, biz.PipelineDispatchRequest{Admission: f.execution.Admission, Owner: f.dispatch.Plan.Owner})
	if err != nil || reserved.SendPermit == nil {
		t.Fatal("original dispatch reservation", err)
	}
	at, err := dispatch.SubmissionObservationTime(ctx, *reserved.SendPermit)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := dispatch.RecordSubmissionConfirmed(ctx, *reserved.SendPermit, biz.PipelineSubmissionObservation{State: biz.PipelineSubmissionConfirmed, RunID: f.runID}, at)
	if err != nil {
		t.Fatal(err)
	}
	f.dispatch = confirmed.Dispatch
	p := f.dispatch.Plan
	f.run["experiment_id"], f.run["display_name"], f.run["service_account"] = p.Environment.ExperimentID, p.DisplayName, p.Environment.Identities.KFPStepServiceAccount
	f.run["pipeline_version_reference"] = map[string]any{"pipeline_id": p.PipelineID, "pipeline_version_id": p.PipelineVersionID}
	f.run["runtime_config"] = map[string]any{"parameters": map[string]any{"execution_id": p.ExecutionID, "spec_hash": p.SpecHash}, "pipeline_root": p.Owner.PipelineRoot}
	namespace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": a.NamespaceName, "uid": a.NamespaceUID}}}
	account := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"namespace": a.NamespaceName, "name": p.Environment.Identities.KFPStepServiceAccount, "uid": "289ad3d6-01ac-4886-8e8c-7318a1865e9f"}}}
	objects := []runtime.Object{namespace, account, f.workflow, pvc}
	for i := range f.pods {
		objects = append(objects, &f.pods[i])
	}
	kube := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList", {Group: "batch", Version: "v1", Resource: "jobs"}: "JobList", {Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}: "JobSetList", {Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}: "PodGroupList"}, objects...)
	kube.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		request := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		token, _, _ := unstructured.NestedString(request.Object, "spec", "token")
		audiences, _, _ := unstructured.NestedStringSlice(request.Object, "spec", "audiences")
		if len(audiences) != 1 || audiences[0] != "https://kubernetes.default.svc.cluster.local" {
			t.Fatal("TokenReview audience changed")
		}
		return true, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "TokenReview", "status": map[string]any{
			"authenticated": token == "external-bound-finalizer-token", "audiences": []any{audiences[0]}, "user": map[string]any{
				"username": "system:serviceaccount:" + a.NamespaceName + ":" + p.Environment.Identities.KFPStepServiceAccount,
				"uid":      string(account.GetUID()), "extra": map[string]any{"authentication.kubernetes.io/pod-name": []any{a.PodName}, "authentication.kubernetes.io/pod-uid": []any{a.PodUID}},
			}}}}, nil
	})
	identity, err := stepidentity.New(kube, "https://kubernetes.default.svc.cluster.local")
	if err != nil {
		t.Fatal(err)
	}
	facts := lifecycle.New(pool)
	proof := runtimeproof.New(kube, f.runs, nil, dispatch)
	steps, err := biz.NewManagedSteps(dispatch, executions, identity, proof)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := biz.NewManagedRuntime(steps, facts, trainer.New(kube), proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	claim := biz.BeginManagedExecutionRequest{TenantID: p.TenantID, ExecutionID: p.ExecutionID, OperationID: p.OperationID, SpecHash: p.SpecHash, Association: a}
	if _, err := managed.Close(ctx, "invalid-finalizer-token", claim, "STEP_FAILED"); !errors.Is(err, biz.ErrManagedStepUnauthorized) {
		t.Fatalf("invalid current identity accepted: %v", err)
	}
	before, err := facts.GetRuntime(ctx, p.TenantID, p.ExecutionID)
	if err != nil || before.CloseGeneration != 0 {
		t.Fatalf("failed authentication changed close fence: %+v %v", before, err)
	}
	mismatched := f.authority()
	mismatched.PlanHash = strings.Repeat("f", 64)
	if _, _, err := facts.FenceManagedFailureClose(ctx, mismatched); !errors.Is(err, biz.ErrRunAuthorityConflict) {
		t.Fatal("foreign frozen plan acquired close fence", err)
	}
	unchanged, err := facts.GetRuntime(ctx, p.TenantID, p.ExecutionID)
	if err != nil || unchanged.CloseGeneration != 0 || unchanged.CloseAuthority != nil {
		t.Fatal("mismatched candidate leaked an uncommitted close fence", err)
	}
	result, err := managed.Close(ctx, "external-bound-finalizer-token", claim, "STEP_FAILED")
	if err != nil || result.Runtime.CloseGeneration == 0 || result.Runtime.CloseRequestedAt.IsZero() || result.Runtime.ClosedAt != nil || result.Runtime.CloseAuthority == nil || result.States.Close != biz.CloseStateClosing {
		t.Fatalf("AUTHENTICATED_UNBOUND_FAILURE_CLOSE_HANDOFF_MISSING: finalizer must persist CLOSING before owner writer proof, without fabricating CLOSED: %+v err=%v", result.Runtime, err)
	}
	transport := peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13}}})
	transport = metadata.NewIncomingContext(transport, metadata.Pairs("x-ani-tenant-id", p.TenantID, "authorization", "Bearer external-bound-finalizer-token"))
	closeRequest := &modeldevv1.RequestExecutionCloseRequest{Context: &modeldevv1.StepContext{Identity: &trainingv1.ExecutionIdentity{ExecutionId: p.ExecutionID, OperationId: p.OperationID, ExecutionSpecHash: p.SpecHash}, Association: &modeldevv1.RunAssociation{KfpRunId: a.RunID, NamespaceName: a.NamespaceName, NamespaceUid: a.NamespaceUID, WorkflowName: a.WorkflowName, WorkflowUid: a.WorkflowUID, PodName: a.PodName, PodUid: a.PodUID}, Step: modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE}, Reason: modeldevv1.CloseReason_CLOSE_REASON_STEP_FAILED}
	reply, err := service.NewRuntimeStep(steps, managed).RequestExecutionClose(transport, closeRequest)
	if err != nil || reply.CloseGeneration != result.Runtime.CloseGeneration || reply.CloseState != modeldevv1.CloseState_CLOSE_STATE_CLOSING {
		t.Fatalf("CLOSE_ONLY_TRANSPORT_MUST_NOT_REQUIRE_NORMAL_BOUND_AT: %+v %v", reply, err)
	}
	tokenFile := filepath.Join(t.TempDir(), "projected-token")
	if err := os.WriteFile(tokenFile, []byte("external-bound-finalizer-token"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &ownerCloseTransport{step: service.NewRuntimeStep(steps, managed)}
	runner, err := component.New(component.Config{TenantID: p.TenantID, Context: closeRequest.Context, TokenFile: tokenFile, CloseOnly: true}, client, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, "close"); err != nil || client.calls != 1 {
		t.Fatalf("published close-only client rejected actual service wire receipt: calls=%d err=%v", client.calls, err)
	}
	if _, err := dispatch.GetRunAuthority(ctx, p.TenantID, p.ExecutionID); !errors.Is(err, biz.ErrExecutionNotFound) {
		t.Fatal("failure finalizer acquired normal step authority", err)
	}
	if _, err := steps.Begin(ctx, "external-bound-finalizer-token", claim); !errors.Is(err, biz.ErrManagedStepUnauthorized) {
		t.Fatal("failure finalizer acquired normal Begin authority", err)
	}
	pool.Close()
	pool = open()
	t.Cleanup(pool.Close)
	persisted, err := lifecycle.New(pool).GetRuntime(ctx, p.TenantID, p.ExecutionID)
	if err != nil || persisted.CloseGeneration != result.Runtime.CloseGeneration || persisted.CloseAuthority == nil || *persisted.CloseAuthority != f.authority() || persisted.Workspace != nil || persisted.Training != nil || persisted.Observation != nil || persisted.Publication != nil {
		t.Fatalf("reconnect lost pure close handoff or invented successful facts: %+v %v", persisted, err)
	}
	// Resume via the actual background owner use case, with fresh PostgreSQL
	// adapters after reconnect. KFP/Kubernetes alone are boundary substitutes.
	dispatch = submission.New(pool)
	executions = execution.New(pool)
	facts = lifecycle.New(pool)
	proof = runtimeproof.New(kube, f.runs, nil, dispatch)
	steps, err = biz.NewManagedSteps(dispatch, executions, identity, proof)
	if err != nil {
		t.Fatal(err)
	}
	managed, err = biz.NewManagedRuntime(steps, facts, trainer.New(kube), proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	closer, err := biz.NewExecutionCloser(managed, f.runs, proof, proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	pvcDeletes := 0
	kube.PrependReactor("delete", "persistentvolumeclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		request := action.(ktesting.DeleteAction).GetDeleteOptions()
		if request.Preconditions == nil || request.Preconditions.UID == nil || string(*request.Preconditions.UID) != string(pvc.GetUID()) || request.Preconditions.ResourceVersion == nil || *request.Preconditions.ResourceVersion != pvc.GetResourceVersion() {
			t.Fatal("PVC deletion lost original UID/resourceVersion preconditions")
		}
		pvcDeletes++
		return false, nil, nil
	})
	reconciled, err := closer.Reconcile(ctx, p.TenantID, p.ExecutionID)
	applied := reconciled.Runtime
	if err != nil || applied.ClosedAt == nil || applied.CloseEvidence == nil || applied.CloseEvidence.UnpreparedWorkspace == nil || applied.StepExitCleanup == nil || applied.StepExitCleanup.CompletedAt == nil || applied.ComputeCleanup == nil || applied.ComputeCleanup.Phase != "APPLIED" || pvcDeletes != 1 || applied.Workspace != nil || applied.Training != nil || applied.Observation != nil {
		t.Fatalf("OWNER_MUST_CLOSE_AND_RECLAIM_EXACT_UNPREPARED_PVC: %+v deletes=%d %v", applied, pvcDeletes, err)
	}
	targets, _, err := biz.ComputeCleanupTargets(f.execution, f.authority(), applied)
	if err != nil || len(targets) != 2 || targets[0].Kind != "PersistentVolumeClaim" || targets[0].UID != string(pvc.GetUID()) || targets[1].Kind != "Workflow" {
		t.Fatalf("exact unprepared PVC must precede owner cleanup: %+v %v", targets, err)
	}
	if _, err := kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(pvc.GetNamespace()).Get(ctx, pvc.GetName(), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("original PVC still present", err)
	}
	if err := proof.VerifyRecordedComputeWritersAbsent(ctx, f.execution, f.authority(), applied); err != nil {
		t.Fatal("durable cleanup readback", err)
	}
	pool.Close()
	pool = open()
	t.Cleanup(pool.Close)
	final, err := lifecycle.New(pool).GetRuntime(ctx, p.TenantID, p.ExecutionID)
	if err != nil || final.CloseEvidence.UnpreparedWorkspace.PVCUID != string(pvc.GetUID()) || final.ComputeCleanup.Phase != "APPLIED" || final.Workspace != nil {
		t.Fatalf("reconnect lost exact observed PVC/cleanup: %+v %v", final, err)
	}

}

// Substitute only the gRPC channel. Both the request and actual service reply
// cross protobuf bytes, while auth, admission and close use real production code.
type ownerCloseTransport struct {
	modeldevv1.ModelDevStepServiceClient
	step  *service.Step
	calls int
}

func (client *ownerCloseTransport) RequestExecutionClose(ctx context.Context, request *modeldevv1.RequestExecutionCloseRequest, _ ...grpc.CallOption) (*modeldevv1.RequestExecutionCloseResponse, error) {
	client.calls++
	raw, err := proto.Marshal(request)
	if err != nil {
		return nil, err
	}
	var received modeldevv1.RequestExecutionCloseRequest
	if err := proto.Unmarshal(raw, &received); err != nil {
		return nil, err
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	ctx = metadata.NewIncomingContext(ctx, md)
	ctx = peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13}}})
	reply, err := client.step.RequestExecutionClose(ctx, &received)
	if err != nil {
		return nil, err
	}
	raw, err = proto.Marshal(reply)
	if err != nil {
		return nil, err
	}
	var decoded modeldevv1.RequestExecutionCloseResponse
	if err := proto.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}
