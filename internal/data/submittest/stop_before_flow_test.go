//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"crypto/tls"
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
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestMainFlowStopBeforeAdmissionClosesLateAdmissionWithoutCreating(t *testing.T) {
	f := newCompleteFixture(t)
	startup := os.Getenv("ANI_MODELDEV_MAINFLOW_STARTUP")
	limit := 30 * time.Second
	if startup != "" {
		limit = 6 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	open := postgres.Prepare(t)
	pool := open()
	var generation uint64
	if startup != "" {
		store := s3.New(s3.Options{Region: "us-east-1", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic-key", SecretAccessKey: "synthetic-secret"}, nil
		}), BaseEndpoint: aws.String(f.storage.URL), UsePathStyle: true, HTTPClient: f.storage.Client(), RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
		resolver, selection, intent := prepareMainFlowAdmission(t, ctx, f, pool, store)
		query := startMainFlowQuery(t, open(), store, f.request.Admission, resolver)
		awaitBusinessAdmission(t, ctx, f, query, execution.New(pool), selection, intent)
		close, err := execution.New(pool).GetCloseIntent(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
		if err != nil || close.OperationID != f.request.Admission.OperationID || close.SpecHash != f.request.Admission.SpecHash || close.Generation != 1 || close.SourceGeneration != 1 || close.Reason != biz.CloseReasonUserStop {
			t.Fatalf("BFF_STOP_BEFORE_NOT_IMPLEMENTED: late admission has no original stop tombstone: %+v %v", close, err)
		}
		generation = close.Generation
	} else {
		generation = applyOwnerStop(t, ctx, f, pool)
		pool.Close()
		pool = open()
		acceptThroughCommandRPC(t, ctx, execution.New(pool), f.request.Admission)
	}
	pool.Close()
	pool = open()
	worker, runs := assembleRecoveryOwner(t, f, pool)
	submitter, err := biz.NewPipelineSubmitter(submission.New(pool), runs, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := submitter.Submit(ctx, f.request); err == nil {
		t.Fatal("stopped late admission granted a KFP creation permit")
	}
	batch, err := worker.ReconcileOnce(ctx)
	closed, readErr := lifecycle.New(pool).GetRuntime(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || readErr != nil || batch.Closed != 1 || closed.ClosedAt == nil || closed.CloseGeneration != generation || closed.CloseReason != "USER_STOP" || closed.CloseEvidence == nil || !closed.CloseEvidence.NoDispatch {
		t.Fatalf("STOP_BEFORE_CLOSE_NOT_IMPLEMENTED: recovered late admission was not durably closed: batch=%+v runtime=%+v err=%v read=%v", batch, closed, err, readErr)
	}
	visible, err := execution.New(pool).Get(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || visible.States.Close != biz.CloseState("CLOSED") || visible.States.Resource != biz.ResourceStateNotApplicable {
		t.Fatalf("closed tombstone is not visible: %+v %v", visible, err)
	}
	f.mu.Lock()
	creates, runCreates := f.creates, f.runCreates
	f.mu.Unlock()
	if creates != 0 || runCreates != 0 {
		t.Fatalf("stop-before-admission created compute: runs=%d training=%d", runCreates, creates)
	}
	// Repeating the late command and a fresh owner must preserve the closure.
	acceptThroughCommandRPC(t, ctx, execution.New(pool), f.request.Admission)
	pool.Close()
	pool = open()
	worker, _ = assembleRecoveryOwner(t, f, pool)
	if batch, err := worker.ReconcileOnce(ctx); err != nil || batch.Examined != 0 {
		t.Fatalf("closed late admission was reopened: %+v %v", batch, err)
	}
	t.Log("STOP_BEFORE_CLOSE: mTLS tombstone survived reconnect, late admission closed with zero KFP/TrainJob creation")
	if startup != "" {
		writeOwnerCloseSignal(t, startup, "closed.json", map[string]any{"operation_id": f.request.Admission.OperationID, "execution_id": f.request.Admission.ExecutionID, "close_generation": closed.CloseGeneration})
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(filepath.Join(filepath.Dir(startup), "stop")); err == nil {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("BFF_STOP_BEFORE_NOT_IMPLEMENTED: BFF did not acknowledge CLOSED query")
			case <-ticker.C:
			}
		}
	}
}

func applyOwnerStop(t *testing.T, ctx context.Context, f *completeFixture, pool *pgxpool.Pool) uint64 {
	t.Helper()
	query := startMainFlowQuery(t, pool, s3.New(s3.Options{Region: "us-east-1"}), f.request.Admission)
	connection, err := grpc.NewClient(query.address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: query.certificates.Roots, ServerName: "ani-modeldev-service", Certificates: []tls.Certificate{query.certificates.Governance}})))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	call := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", f.request.Admission.TenantID, "x-ani-actor", f.request.Admission.Actor, "x-ani-request-id", "11111111-9999-4333-8444-555555555555"))
	receipt, err := modeldevv1.NewModelDevCommandServiceClient(connection).ApplyCloseIntent(call, &modeldevv1.ApplyCloseIntentRequest{Identity: f.stepContext("prepare").Identity, ResourceTenantId: f.request.Admission.TenantID, RequestedActorId: f.request.Admission.Actor, IntentGeneration: 1, Reason: modeldevv1.CloseReason_CLOSE_REASON_USER_STOP, RequestedAt: timestamppb.New(time.Now().UTC().Truncate(time.Microsecond))})
	if err != nil || !receipt.GetDurablyRecorded() || receipt.GetCloseGeneration() == 0 {
		t.Fatalf("close command was not committed: %v", err)
	}
	return receipt.CloseGeneration
}

func assembleRecoveryOwner(t *testing.T, f *completeFixture, pool *pgxpool.Pool) (*biz.CloseWorker, *kfp.Client) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(f.kfp.Certificate())
	runs, err := kfp.New(kfp.Config{ConnectionRef: f.request.Admission.Snapshot.Environment.KFPConnectionRef, Endpoint: f.kfp.URL, RootCAs: roots, Timeout: 5 * time.Second}, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "synthetic-kfp-owner", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	kube, err := dynamic.NewForConfig(&rest.Config{Host: f.kube.URL, BearerToken: "synthetic-kube-owner", Timeout: 10 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.kube.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := stepidentity.New(kube, "ani-modeldev-managed-step")
	if err != nil {
		t.Fatal(err)
	}
	dispatch := submission.New(pool)
	training := trainer.New(kube).WithTrainingLogs(newMainFlowTrainingLogReader(t, f))
	proof := runtimeproof.New(kube, runs, objectstore.NewVerifier(s3.New(s3.Options{Region: "us-east-1"}), f.request.Admission.Snapshot.PublicationScope.StorageConnectionID), dispatch).WithTrainingRuntime(training)
	steps, err := biz.NewManagedSteps(dispatch, execution.New(pool), identity, proof)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := biz.NewManagedRuntime(steps, lifecycle.New(pool), training, proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	closer, err := biz.NewExecutionCloser(managed, runs, proof, proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := biz.NewCloseWorker(lifecycle.New(pool), closer, biz.PipelineDispatchBinding{TenantID: f.request.Admission.TenantID, Environment: f.request.Admission.Snapshot.Environment, Owner: f.request.Owner}, 10, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return worker, runs
}
