//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc/metadata"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestMainFlowOperationBootstrapPreparesFromExecutionAndSpec(t *testing.T) {
	f, client, facts, kube, store := bootstrapFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	claim := f.stepContext("prepare")
	claim.Identity.OperationId = ""
	if _, err := client.BeginExecution(bootstrapCall(ctx, f, "synthetic-bound-prepare"), &modeldevv1.BeginExecutionRequest{Context: claim}); err != nil {
		t.Fatalf("OPERATION_BOOTSTRAP: authenticated prepare must resolve the persisted operation: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "projected-token")
	if err := os.WriteFile(tokenFile, []byte("synthetic-bound-prepare"), 0600); err != nil {
		t.Fatal(err)
	}
	runner, err := component.New(component.Config{TenantID: f.request.Admission.TenantID, Context: claim, TokenFile: tokenFile, WorkspaceDirectory: f.root, PVCName: f.workspace.PVCName}, client, kube, store)
	if err != nil {
		t.Fatalf("OPERATION_BOOTSTRAP: Runner must accept an unresolved operation: %v", err)
	}
	if err := runner.Run(ctx, "prepare"); err != nil {
		t.Fatalf("OPERATION_BOOTSTRAP: real prepare could not recover its full immutable identity: %v", err)
	}
	persisted, err := facts.GetRuntime(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || persisted.Workspace == nil || persisted.Training != nil {
		t.Fatalf("bootstrap did not persist only the prepared workspace: %+v %v", persisted, err)
	}
	downstream := f.stepContext("train-wait")
	downstream.Identity.OperationId = ""
	configuration, err := client.GetExecutionConfiguration(bootstrapCall(ctx, f, "synthetic-bound-train-wait"), &modeldevv1.GetExecutionConfigurationRequest{Context: downstream})
	if err != nil || configuration.GetAdmission().GetIdentity().GetOperationId() != f.request.Admission.OperationID || configuration.GetIdentity().GetOperationId() != f.request.Admission.OperationID {
		t.Fatalf("authorized downstream bootstrap did not return the original operation: %v", err)
	}
}

func TestMainFlowOperationBootstrapPreservesAuthorityAndWorkloadChecks(t *testing.T) {
	f, client, facts, _, _ := bootstrapFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	downstream := f.stepContext("train-wait")
	downstream.Identity.OperationId = ""
	if result, err := client.GetExecutionConfiguration(bootstrapCall(ctx, f, "synthetic-bound-train-wait"), &modeldevv1.GetExecutionConfigurationRequest{Context: downstream}); err == nil || result != nil {
		t.Fatal("execution and spec alone must not create missing Run authority")
	}
	claim := f.stepContext("prepare")
	if _, err := client.BeginExecution(bootstrapCall(ctx, f, "synthetic-bound-prepare"), &modeldevv1.BeginExecutionRequest{Context: claim}); err != nil {
		t.Fatal("authorized explicit identity preflight failed", err)
	}
	if result, err := client.GetExecutionConfiguration(bootstrapCall(ctx, f, "invalid-workload-token"), &modeldevv1.GetExecutionConfigurationRequest{Context: downstream}); err == nil || result != nil {
		t.Fatal("missing operation bypassed current workload authentication")
	}
	downstream.Identity.OperationId = "99999999-aaaa-4bbb-8ccc-666666666666"
	if result, err := client.GetExecutionConfiguration(bootstrapCall(ctx, f, "synthetic-bound-train-wait"), &modeldevv1.GetExecutionConfigurationRequest{Context: downstream}); err == nil || result != nil {
		t.Fatal("explicit wrong operation was silently replaced by the persisted operation")
	}
	state, err := facts.GetRuntime(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || state.Workspace != nil || state.Training != nil || state.Publication != nil {
		t.Fatalf("rejected bootstrap changed execution facts: %+v %v", state, err)
	}
}

func bootstrapCall(ctx context.Context, f *completeFixture, token string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token, "x-ani-tenant-id", f.request.Admission.TenantID))
}

func bootstrapFixture(t *testing.T) (*completeFixture, modeldevv1.ModelDevStepServiceClient, *lifecycle.Repository, dynamic.Interface, *s3.Client) {
	t.Helper()
	f := newCompleteFixture(t)
	pool := postgres.Prepare(t)()
	return bootstrapFixtureWithPool(t, f, pool)
}

func bootstrapFixtureWithPool(t *testing.T, f *completeFixture, pool *pgxpool.Pool, issuers ...biz.StorageCredentialIssuer) (*completeFixture, modeldevv1.ModelDevStepServiceClient, *lifecycle.Repository, dynamic.Interface, *s3.Client) {
	t.Helper()
	admissions, dispatch, facts := execution.New(pool), submission.New(pool), lifecycle.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acceptThroughCommandRPC(t, ctx, admissions, f.request.Admission)
	roots := x509.NewCertPool()
	roots.AddCert(f.kfp.Certificate())
	runs, err := kfp.New(kfp.Config{ConnectionRef: f.request.Admission.Snapshot.Environment.KFPConnectionRef, Endpoint: f.kfp.URL, RootCAs: roots, Timeout: 5 * time.Second}, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "synthetic-kfp-owner", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	submitter, err := biz.NewPipelineSubmitter(dispatch, runs, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitter.Submit(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	kube, err := dynamic.NewForConfig(&rest.Config{Host: f.kube.URL, BearerToken: "synthetic-kube-owner", Timeout: 10 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.kube.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	store := s3.New(s3.Options{Region: "us-east-1", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "synthetic-key", SecretAccessKey: "synthetic-secret"}, nil
	}), BaseEndpoint: aws.String(f.storage.URL), UsePathStyle: true, HTTPClient: f.storage.Client(), RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	return f, newFixtureRuntimeClient(t, f, pool, kube, store, runs, issuers...), facts, kube, store
}

func newFixtureRuntimeClient(t *testing.T, f *completeFixture, pool *pgxpool.Pool, kube dynamic.Interface, store *s3.Client, runs *kfp.Client, issuers ...biz.StorageCredentialIssuer) modeldevv1.ModelDevStepServiceClient {
	t.Helper()
	identity, err := stepidentity.New(kube, "ani-modeldev-managed-step")
	if err != nil {
		t.Fatal(err)
	}
	dispatch := submission.New(pool)
	proof := runtimeproof.New(kube, runs, objectstore.NewVerifier(store, f.request.Admission.Snapshot.PublicationScope.StorageConnectionID), dispatch)
	steps, err := biz.NewManagedSteps(dispatch, execution.New(pool), identity, proof)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := biz.NewManagedRuntime(steps, lifecycle.New(pool), trainer.New(kube), proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	client, stop := startMainFlowStepHandler(t, service.NewRuntimeStep(steps, managed, issuers...))
	t.Cleanup(stop)
	return client
}
