package submittest_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// This extends the same real admission/submission chain through the protected
// Begin RPC. Only external KFP/Kubernetes responses and credentials are fixtures.
func TestMainFlowManagedBeginAuthenticatesAndRecovers(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		name := "confirmed submission"
		if lostResponse {
			name = "lost CreateRun response"
		}
		t.Run(name, func(t *testing.T) { runManagedBeginFlow(t, lostResponse) })
	}
}

func runManagedBeginFlow(t *testing.T, lostResponse bool) {
	openPool := postgres.Prepare(t)
	request := dispatchRequest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	pool := openPool()
	executions, repository := execution.New(pool), submission.New(pool)
	if _, err := executions.Accept(ctx, request.Admission); err != nil {
		t.Fatal(err)
	}
	association := biz.ManagedStepAssociation{
		RunID:         "55555555-6666-4777-8888-999999999999",
		NamespaceName: request.Admission.Snapshot.Environment.NamespaceName,
		NamespaceUID:  request.Admission.Snapshot.Environment.NamespaceUID,
		WorkflowName:  "main-flow-workflow", WorkflowUID: "cccccccc-dddd-4eee-8fff-111111111111",
		PodName: "main-flow-prepare", PodUID: "dddddddd-eeee-4fff-8aaa-222222222222",
	}
	var posts atomic.Int32
	var wrongTask atomic.Bool
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-control-token" {
			t.Error("missing frozen KFP control identity")
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/apis/v2beta1/runs" {
			posts.Add(1)
			if lostResponse {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
				return
			}
		} else if r.Method != http.MethodGet || r.URL.Path != "/apis/v2beta1/runs/"+association.RunID {
			t.Error("unexpected KFP operation")
			w.WriteHeader(400)
			return
		}
		executorNodeID := "main-flow-prepare-node"
		if wrongTask.Load() {
			executorNodeID = "unrelated-node"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run_id": association.RunID, "experiment_id": "44444444-4444-4444-8444-444444444444",
			"display_name":               "md-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
			"pipeline_version_reference": map[string]string{"pipeline_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "pipeline_version_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd"},
			"runtime_config":             map[string]any{"parameters": map[string]string{"execution_id": request.Admission.ExecutionID, "spec_hash": request.Admission.SpecHash}, "pipeline_root": "s3://fixture-kfp-artifacts/frozen-submit-root"},
			"service_account":            "cpu-managed-step", "state": "RUNNING",
			"run_details": map[string]any{"task_details": []any{map[string]any{"run_id": association.RunID, "task_id": "synthetic-task-id", "display_name": "prepare", "child_tasks": []any{map[string]string{"pod_name": executorNodeID}}, "state": "RUNNING"}}},
		})
	}))
	t.Cleanup(peer.Close)
	provider := tokenProviderFunc(func(_ context.Context, tenant string, env cpup01.EnvironmentBindingSnapshot) (string, error) {
		if tenant != request.Admission.TenantID || env != request.Admission.Snapshot.Environment {
			t.Error("wrong frozen KFP scope")
		}
		return "synthetic-control-token", nil
	})
	result, err := newSubmitter(t, repository, peer, provider).Submit(ctx, request)
	if lostResponse {
		if !errors.Is(err, biz.ErrPipelineSubmissionUncertain) || result.Dispatch.State != biz.PipelineDispatchUncertain {
			t.Fatalf("uncertain preflight: %v", err)
		}
	} else if err != nil || result.Dispatch.State != biz.PipelineDispatchConfirmed {
		t.Fatalf("confirmed preflight: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatal("CreateRun was not sent exactly once")
	}
	t.Log("MAIN_FLOW: real admission and one CreateRun attempt persisted")
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	runs, err := kfp.New(kfp.Config{ConnectionRef: "kfp-managed-v1", Endpoint: peer.URL, RootCAs: roots, Timeout: 3 * time.Second}, provider)
	if err != nil {
		t.Fatal(err)
	}
	workloads, kube := mainFlowWorkloadVerifier(t, request.Admission.Snapshot.Environment, association)
	proof := runtimeproof.New(kube, runs, nil, repository)
	steps, err := biz.NewManagedSteps(repository, executions, workloads, proof)
	if err != nil {
		t.Fatal(err)
	}
	client, stop := startMainFlowStepServer(t, steps, lifecycle.New(pool), runs, repository)
	body := &modeldevv1.BeginExecutionRequest{Context: &modeldevv1.StepContext{
		Identity:    &trainingv1.ExecutionIdentity{OperationId: request.Admission.OperationID, ExecutionId: request.Admission.ExecutionID, ExecutionSpecHash: request.Admission.SpecHash},
		Association: &modeldevv1.RunAssociation{KfpRunId: association.RunID, NamespaceName: association.NamespaceName, NamespaceUid: association.NamespaceUID, WorkflowName: association.WorkflowName, WorkflowUid: association.WorkflowUID, PodName: association.PodName, PodUid: association.PodUID},
		Step:        modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE,
	}}
	callContext := func(token string) context.Context {
		return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token, "x-ani-tenant-id", request.Admission.TenantID))
	}
	for _, token := range []string{"synthetic-training-token", "synthetic-forged-token"} {
		if _, err := client.BeginExecution(callContext(token), body); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("untrusted workload was not rejected: %v", err)
		}
	}
	wrongTask.Store(true)
	if _, err := client.BeginExecution(callContext("synthetic-bound-step-token"), body); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("KFP unrelated Pod was not rejected: %v", err)
	}
	wrongTask.Store(false)
	if _, err := repository.GetRunAuthority(ctx, request.Admission.TenantID, request.Admission.ExecutionID); !errors.Is(err, biz.ErrExecutionNotFound) {
		t.Fatal("rejected calls created authority")
	}
	response, err := client.BeginExecution(callContext("synthetic-bound-step-token"), body)
	if err != nil || response.GetAuthority().GetKfpRunId() != association.RunID || response.Replayed {
		t.Fatalf("main flow did not bind authenticated Begin: %v", err)
	}
	boundAt := response.Authority.BoundAt.AsTime()
	t.Log("MAIN_FLOW: TLS Begin verified TokenReview, current objects and KFP task, then committed authority")
	stop()
	pool.Close()
	pool = openPool()
	repository, executions = submission.New(pool), execution.New(pool)
	proof = runtimeproof.New(kube, runs, nil, repository)
	steps, err = biz.NewManagedSteps(repository, executions, workloads, proof)
	if err != nil {
		t.Fatal(err)
	}
	client, _ = startMainFlowStepServer(t, steps, lifecycle.New(pool), runs, repository)
	response, err = client.BeginExecution(callContext("synthetic-bound-step-token"), body)
	if err != nil || !response.GetReplayed() || !response.GetAuthority().GetBoundAt().AsTime().Equal(boundAt) {
		t.Fatalf("restarted Begin lost original binding: %v", err)
	}
	if _, err := newSubmitter(t, repository, peer, provider).Submit(ctx, request); err != nil || posts.Load() != 1 {
		t.Fatalf("restart repeated submission: %v", err)
	}
	forged := proto.Clone(body).(*modeldevv1.BeginExecutionRequest)
	forged.Context.Association.PodUid = "eeeeeeee-ffff-4aaa-8bbb-333333333333"
	if _, err := client.BeginExecution(callContext("synthetic-bound-step-token"), forged); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("replay bypassed current Pod identity: %v", err)
	}
	t.Log("MAIN_FLOW: restarted RPC reauthenticated and replayed original authority without CreateRun")
}

func mainFlowWorkloadVerifier(t *testing.T, env cpup01.EnvironmentBindingSnapshot, association biz.ManagedStepAssociation) (*stepidentity.Verifier, dynamic.Interface) {
	t.Helper()
	const saUID = "ffffffff-aaaa-4bbb-8ccc-444444444444"
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer synthetic-kube-control" {
			t.Error("wrong Kubernetes client credential")
			w.WriteHeader(401)
			return
		}
		metadata := func(name, uid string, namespaced bool) map[string]any {
			m := map[string]any{"name": name, "uid": uid}
			if namespaced {
				m["namespace"] = env.NamespaceName
			}
			return m
		}
		var object map[string]any
		switch r.URL.Path {
		case "/apis/authentication.k8s.io/v1/tokenreviews":
			var request struct {
				Spec struct {
					Token     string   `json:"token"`
					Audiences []string `json:"audiences"`
				} `json:"spec"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Spec.Audiences) != 1 || request.Spec.Audiences[0] != "ani-modeldev-managed-step" {
				t.Error("invalid TokenReview request")
				w.WriteHeader(400)
				return
			}
			object = map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "TokenReview", "status": map[string]any{"authenticated": request.Spec.Token == "synthetic-bound-step-token", "audiences": []string{"ani-modeldev-managed-step"}, "user": map[string]any{"username": "system:serviceaccount:" + env.NamespaceName + ":" + env.Identities.KFPStepServiceAccount, "uid": saUID, "extra": map[string]any{"authentication.kubernetes.io/pod-name": []string{association.PodName}, "authentication.kubernetes.io/pod-uid": []string{association.PodUID}}}}}
		case "/api/v1/namespaces/" + env.NamespaceName:
			object = map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": metadata(env.NamespaceName, env.NamespaceUID, false)}
		case "/api/v1/namespaces/" + env.NamespaceName + "/serviceaccounts/" + env.Identities.KFPStepServiceAccount:
			object = map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": metadata(env.Identities.KFPStepServiceAccount, saUID, true)}
		case "/api/v1/namespaces/" + env.NamespaceName + "/pods/" + association.PodName, "/api/v1/namespaces/" + env.NamespaceName + "/pods":
			m := metadata(association.PodName, association.PodUID, true)
			m["annotations"] = map[string]any{"workflows.argoproj.io/node-id": "main-flow-prepare-node", "workflows.argoproj.io/node-name": association.WorkflowName + ".prepare.executor"}
			m["ownerReferences"] = []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": association.WorkflowName, "uid": association.WorkflowUID, "controller": true}}
			object = map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": m, "spec": map[string]string{"serviceAccountName": env.Identities.KFPStepServiceAccount}}
			if strings.HasSuffix(r.URL.Path, "/pods") {
				object = map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{}, "items": []any{object}}
			}
		case "/apis/argoproj.io/v1alpha1/namespaces/" + env.NamespaceName + "/workflows/" + association.WorkflowName:
			object = map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "metadata": metadata(association.WorkflowName, association.WorkflowUID, true), "status": map[string]any{"nodes": map[string]any{
				"main-flow-prepare-controller": map[string]any{"id": "main-flow-prepare-controller", "name": association.WorkflowName + ".prepare", "displayName": "prepare", "phase": "Running", "children": []any{"main-flow-prepare-node"}},
				"main-flow-prepare-node":       map[string]any{"id": "main-flow-prepare-node", "name": association.WorkflowName + ".prepare.executor", "displayName": "executor", "type": "Pod", "phase": "Running", "boundaryID": "main-flow-prepare-controller", "templateName": "metadata-0-2-retry-system-container-impl"},
			}}}
		default:
			t.Error("unexpected Kubernetes operation")
			w.WriteHeader(404)
			return
		}
		if !strings.Contains(r.URL.Path, "tokenreviews") && r.Method != http.MethodGet {
			t.Error("unexpected Kubernetes mutation")
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(object)
	}))
	t.Cleanup(peer.Close)
	client, err := dynamic.NewForConfig(&rest.Config{Host: peer.URL, BearerToken: "synthetic-kube-control", Timeout: 3 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := stepidentity.New(client, "ani-modeldev-managed-step")
	if err != nil {
		t.Fatal(err)
	}
	return verifier, client
}

func startMainFlowStepServer(t *testing.T, steps *biz.ManagedSteps, repository *lifecycle.Repository, runs *kfp.Client, dispatches *submission.Repository) (modeldevv1.ModelDevStepServiceClient, func()) {
	t.Helper()
	// Begin uses the real authenticated task and PostgreSQL role-window code.
	// The training/publication boundaries remain unused in this Begin-only case.
	proof := runtimeproof.New(nil, runs, nil, dispatches)
	runtime, err := biz.NewManagedRuntime(steps, repository, trainer.New(nil), proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	return startMainFlowStepHandler(t, service.NewRuntimeStep(steps, runtime))
}

func startMainFlowStepHandler(t *testing.T, handler modeldevv1.ModelDevStepServiceServer) (modeldevv1.ModelDevStepServiceClient, func()) {
	t.Helper()
	certificates := commandtls.New(t)
	listener, err := server.NewManagedStepServer(&conf.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(5 * time.Second)}, certificates.Server, handler)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := listener.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- listener.Start(context.Background()) }()
	connection, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: certificates.Roots, ServerName: commandtls.ServerDNSName})))
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = connection.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := listener.Stop(ctx); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Error("step listener did not stop")
		}
	}
	t.Cleanup(stop)
	return modeldevv1.NewModelDevStepServiceClient(connection), stop
}
