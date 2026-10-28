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
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/cleanup"
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
	"google.golang.org/protobuf/types/known/durationpb"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func operationsCall(ctx context.Context, admission biz.Admission, method string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", admission.TenantID, "x-ani-actor", "governance:user:9001", "x-ani-request-id", "99999999-1111-4222-8333-444444444444", "x-ani-authorized-method", method, "x-ani-data-scope", "tenant-all"))
}

func TestOperationsInspectRequiresCurrentAuthorizationAndProjectsDurableFacts(t *testing.T) {
	pool := postgres.Prepare(t)()
	request := dispatchRequest(t)
	repository := execution.New(pool)
	if _, err := repository.Accept(context.Background(), request.Admission); err != nil {
		t.Fatal(err)
	}
	operations := service.NewOperations(biz.NewExecutionOperations(repository))
	ctx := context.Background()
	if _, err := operations.Inspect(ctx, request.Admission.ExecutionID); status.Code(err) != codes.Unauthenticated {
		t.Fatal("unauthenticated inspect was accepted", err)
	}
	delivery := service.GovernanceDelivery{TenantID: request.Admission.TenantID, Actor: "governance:user:9001", RequestID: "99999999-1111-4222-8333-444444444444"}
	ctx = service.WithVerifiedGovernanceQuery(ctx, delivery, service.InspectExecutionOperation)
	view, err := operations.Inspect(ctx, request.Admission.ExecutionID)
	if err != nil || view.OperationID != request.Admission.OperationID || view.SpecHash != request.Admission.SpecHash || view.NamespaceUID != request.Admission.Snapshot.Environment.NamespaceUID || !view.ObservationStale {
		t.Fatalf("OPERATIONS_INSPECT_NOT_IMPLEMENTED: durable authorized inspect unavailable: %+v %v", view, err)
	}
	encoded, _ := json.Marshal(view)
	for _, secret := range []string{"credential_ref", "credential_reference", "object_key", "download_url", request.Admission.Snapshot.Input.Object.Key} {
		if secret != "" && strings.Contains(string(encoded), secret) {
			t.Fatal("inspect disclosed non-whitelisted storage data")
		}
	}
	delivery.TenantID = "99999999-aaaa-4bbb-8ccc-111111111111"
	if _, err := operations.Inspect(service.WithVerifiedGovernanceQuery(context.Background(), delivery, service.InspectExecutionOperation), request.Admission.ExecutionID); status.Code(err) != codes.NotFound {
		t.Fatal("cross-tenant inspect disclosed execution", err)
	}
}

func TestOperationsMTLSRequiresExactMethodAndTenantScope(t *testing.T) {
	pool := postgres.Prepare(t)()
	request := dispatchRequest(t)
	repository := execution.New(pool)
	if _, err := repository.Accept(context.Background(), request.Admission); err != nil {
		t.Fatal(err)
	}
	boundary := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/"+request.Admission.Snapshot.Environment.NamespaceName {
			t.Error("reconcile/cleanup created a new Run or escaped exact namespace")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]string{"name": request.Admission.Snapshot.Environment.NamespaceName, "uid": request.Admission.Snapshot.Environment.NamespaceUID}})
	}))
	defer boundary.Close()
	kube, err := dynamic.NewForConfig(&rest.Config{Host: boundary.URL, Timeout: time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: boundary.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(boundary.Certificate())
	runs, err := kfp.New(kfp.Config{ConnectionRef: request.Admission.Snapshot.Environment.KFPConnectionRef, Endpoint: boundary.URL, RootCAs: roots, Timeout: time.Second}, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "unused-no-dispatch", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	dispatch := submission.New(pool)
	identity, err := stepidentity.New(kube, "ani-modeldev-managed-step")
	if err != nil {
		t.Fatal(err)
	}
	proof := runtimeproof.New(kube, runs, nil, dispatch)
	steps, err := biz.NewManagedSteps(dispatch, repository, identity, proof)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := biz.NewManagedRuntime(steps, lifecycle.New(pool), trainer.New(kube), proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	closer, err := biz.NewExecutionCloser(runtime, runs, proof)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := biz.NewManagedExecutionOperations(repository, closer, cleanup.New(kube, proof))
	if err != nil {
		t.Fatal(err)
	}
	certs := commandtls.New(t)
	listener, err := server.NewGovernanceServicesServer(&conf.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(5 * time.Second)}, server.CommandTLS{Certificate: certs.Server, ClientCAs: certs.Roots, GovernanceDNSName: commandtls.GovernanceDNSName}, service.NewCommand(repository), nil, nil, server.GovernanceServices{Operations: service.NewOperations(managed)})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := listener.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- listener.Start(context.Background()) }()
	connection, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: certs.Roots, ServerName: commandtls.ServerDNSName, Certificates: []tls.Certificate{certs.Governance}})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = listener.Stop(ctx)
		<-done
	})
	client := modeldevv1.NewModelDevOperationsServiceClient(connection)
	ctx := operationsCall(context.Background(), request.Admission, modeldevv1.ModelDevOperationsService_InspectExecution_FullMethodName)
	view, err := client.InspectExecution(ctx, &modeldevv1.InspectExecutionRequest{ExecutionId: request.Admission.ExecutionID})
	if err != nil || view.GetInspection().GetOperationId() != request.Admission.OperationID {
		t.Fatal("typed authenticated inspect is unavailable", err)
	}
	if _, err := client.InspectExecution(operationsCall(context.Background(), request.Admission, modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName), &modeldevv1.InspectExecutionRequest{ExecutionId: request.Admission.ExecutionID}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("other-method authorization granted inspect", err)
	}
	other := request.Admission
	other.TenantID = "99999999-aaaa-4bbb-8ccc-111111111111"
	if _, err := client.InspectExecution(operationsCall(context.Background(), other, modeldevv1.ModelDevOperationsService_InspectExecution_FullMethodName), &modeldevv1.InspectExecutionRequest{ExecutionId: request.Admission.ExecutionID}); status.Code(err) != codes.NotFound {
		t.Fatal("mTLS other tenant learned execution", err)
	}
	mutating := &modeldevv1.InspectExecutionRequest{ExecutionId: request.Admission.ExecutionID}
	mutating.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
	if _, err := client.InspectExecution(ctx, mutating); status.Code(err) != codes.InvalidArgument {
		t.Fatal("unknown desired-state field was accepted", err)
	}
	if _, err := client.PlanExecutionCleanup(operationsCall(context.Background(), request.Admission, modeldevv1.ModelDevOperationsService_PlanExecutionCleanup_FullMethodName), &modeldevv1.PlanExecutionCleanupRequest{ExecutionId: request.Admission.ExecutionID}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("unclosed execution received cleanup plan", err)
	}
	intent := biz.CloseIntent{TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID, OperationID: request.Admission.OperationID, SpecHash: request.Admission.SpecHash, Reason: biz.CloseReasonUserStop, SourceGeneration: 1, RequestedActor: "governance:user:42", RequestedAt: time.Now().UTC().Truncate(time.Microsecond)}
	if _, err := repository.ApplyCloseIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	closed, err := client.ReconcileExecution(operationsCall(context.Background(), request.Admission, modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName), &modeldevv1.ReconcileExecutionRequest{ExecutionId: request.Admission.ExecutionID})
	if err != nil || closed.GetInspection().GetCloseState() != "CLOSED" {
		t.Fatal("operations reconcile did not resume original owner close", err)
	}
	plan, err := client.PlanExecutionCleanup(operationsCall(context.Background(), request.Admission, modeldevv1.ModelDevOperationsService_PlanExecutionCleanup_FullMethodName), &modeldevv1.PlanExecutionCleanupRequest{ExecutionId: request.Admission.ExecutionID})
	if err != nil || len(plan.Targets) != 0 {
		t.Fatal("closed no-dispatch cleanup plan unavailable", err)
	}
	applyCtx := operationsCall(context.Background(), request.Admission, modeldevv1.ModelDevOperationsService_ApplyExecutionCleanup_FullMethodName)
	if _, err := client.ApplyExecutionCleanup(applyCtx, &modeldevv1.ApplyExecutionCleanupRequest{ExecutionId: request.Admission.ExecutionID, PlanSha256: strings.Repeat("0", 64)}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("cleanup accepted a plan different from preflight", err)
	}
	receipt, err := client.ApplyExecutionCleanup(applyCtx, &modeldevv1.ApplyExecutionCleanupRequest{ExecutionId: request.Admission.ExecutionID, PlanSha256: plan.PlanSha256})
	if err != nil || receipt.GetPhase() != "APPLIED" || receipt.GetActor() != "governance:user:9001" || receipt.CompletedAt == nil {
		t.Fatal("no-target cleanup did not retain authenticated audit receipt", err)
	}
	replayed, err := client.ApplyExecutionCleanup(applyCtx, &modeldevv1.ApplyExecutionCleanupRequest{ExecutionId: request.Admission.ExecutionID, PlanSha256: plan.PlanSha256})
	if err != nil || !replayed.GetStartedAt().AsTime().Equal(receipt.GetStartedAt().AsTime()) {
		t.Fatal("applied cleanup replay changed its original receipt", err)
	}
}

func TestOperationsCleanupDurablyAuditsAndBlocksUnresolvedReplay(t *testing.T) {
	open := postgres.Prepare(t)
	pool := open()
	request := dispatchRequest(t)
	repository := execution.New(pool)
	ctx := context.Background()
	if _, err := repository.Accept(ctx, request.Admission); err != nil {
		t.Fatal(err)
	}
	intent := biz.CloseIntent{TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID, OperationID: request.Admission.OperationID, SpecHash: request.Admission.SpecHash, Reason: biz.CloseReasonUserStop, SourceGeneration: 1, RequestedActor: "governance:user:42", RequestedAt: time.Now().UTC().Truncate(time.Microsecond)}
	if _, err := repository.ApplyCloseIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.New(pool).CloseUndispatched(ctx, intent.TenantID, intent.ExecutionID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/"+request.Admission.Snapshot.Environment.NamespaceName {
			t.Error("cleanup escaped no-dispatch namespace preflight")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]string{"name": request.Admission.Snapshot.Environment.NamespaceName, "uid": request.Admission.Snapshot.Environment.NamespaceUID}})
	}))
	defer server.Close()
	kube, err := dynamic.NewForConfig(&rest.Config{Host: server.URL, Timeout: time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	operations, err := biz.NewManagedExecutionOperations(repository, &biz.ExecutionCloser{}, cleanup.New(kube, nil))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := operations.PlanCleanup(ctx, intent.TenantID, intent.ExecutionID)
	if err != nil || len(plan.Targets) != 0 {
		t.Fatal("closed no-dispatch retention plan unavailable", err)
	}
	reserved, replay, err := repository.ReserveCleanup(ctx, plan, "governance:user:9001")
	if err != nil || replay || reserved.Phase != "STARTED" {
		t.Fatalf("CLEANUP_AUDIT_NOT_IMPLEMENTED: pre-effect reservation unavailable: %+v %v", reserved, err)
	}
	pool.Close()
	reopened := open()
	reader := execution.New(reopened)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	runs, err := kfp.New(kfp.Config{ConnectionRef: request.Admission.Snapshot.Environment.KFPConnectionRef, Endpoint: server.URL, RootCAs: roots, Timeout: time.Second}, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "unused-no-dispatch", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	dispatch := submission.New(reopened)
	identity, err := stepidentity.New(kube, "ani-modeldev-managed-step")
	if err != nil {
		t.Fatal(err)
	}
	proof := runtimeproof.New(kube, runs, nil, dispatch)
	steps, err := biz.NewManagedSteps(dispatch, reader, identity, proof)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := biz.NewManagedRuntime(steps, lifecycle.New(reopened), trainer.New(kube), proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	closer, err := biz.NewExecutionCloser(runtime, runs, proof)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := biz.NewManagedExecutionOperations(reader, closer, cleanup.New(kube, proof))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ApplyCleanup(ctx, intent.TenantID, intent.ExecutionID, plan.PlanHash, "governance:user:9002"); !errors.Is(err, biz.ErrCleanupUncertain) {
		t.Fatal("restart resent an unresolved cleanup", err)
	}
	view, err := restarted.Inspect(ctx, intent.TenantID, intent.ExecutionID)
	if err != nil || view.CleanupPhase != "STARTED" || view.CleanupPlanHash != plan.PlanHash {
		t.Fatal("durable uncertain cleanup audit not inspectable", err)
	}
	resolved, err := restarted.Reconcile(ctx, intent.TenantID, intent.ExecutionID)
	if err != nil || resolved.CleanupPhase != "RECONCILED" || resolved.CleanupPlanHash != plan.PlanHash {
		t.Fatalf("CLEANUP_RECOVERY_NOT_IMPLEMENTED: read-only reconciliation did not resolve the original STARTED audit: %+v %v", resolved, err)
	}
	resolvedReplay, err := restarted.ApplyCleanup(ctx, intent.TenantID, intent.ExecutionID, plan.PlanHash, "governance:user:9002")
	if err != nil || resolvedReplay.Phase != "RECONCILED" || resolvedReplay.Actor != reserved.Actor || !resolvedReplay.StartedAt.Equal(reserved.StartedAt) || !resolvedReplay.CompletedAt.IsZero() {
		t.Fatal("resolved Apply replay resent cleanup or replaced its original audit", err)
	}
}
