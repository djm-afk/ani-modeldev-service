//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestMainFlowOwnerStopRecoversIntentAndTerminatesActualTraining(t *testing.T) {
	f := newCompleteFixture(t)
	f.request.Admission.Snapshot.Program.ResolvedArgs = append(f.request.Admission.Snapshot.Program.ResolvedArgs, "--recipe", "slow-stop")
	var err error
	f.request.Admission.SpecHash, err = f.request.Admission.Snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	startup := os.Getenv("ANI_MODELDEV_MAINFLOW_STARTUP")
	limit := 90 * time.Second
	if startup != "" {
		limit = 6 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	open := postgres.Prepare(t)
	pool := open()
	store := s3.New(s3.Options{Region: "us-east-1", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "synthetic-key", SecretAccessKey: "synthetic-secret"}, nil
	}), BaseEndpoint: aws.String(f.storage.URL), UsePathStyle: true, HTTPClient: f.storage.Client(), RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	if startup != "" {
		resolver, selection, intent := prepareMainFlowAdmission(t, ctx, f, pool, store)
		query := startMainFlowQuery(t, open(), store, f.request.Admission, resolver)
		awaitBusinessAdmission(t, ctx, f, query, execution.New(pool), selection, intent)
	} else {
		acceptThroughCommandRPC(t, ctx, execution.New(pool), f.request.Admission)
	}
	roots := x509.NewCertPool()
	roots.AddCert(f.kfp.Certificate())
	runs, err := kfp.New(kfp.Config{ConnectionRef: f.request.Admission.Snapshot.Environment.KFPConnectionRef, Endpoint: f.kfp.URL, RootCAs: roots, Timeout: 5 * time.Second}, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "synthetic-kfp-owner", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	submitter, err := biz.NewPipelineSubmitter(submission.New(pool), runs, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	dispatchWorker, err := biz.NewDispatchWorker(submission.New(pool), submitter, biz.PipelineDispatchBinding{TenantID: f.request.Admission.TenantID, Environment: f.request.Admission.Snapshot.Environment, Owner: f.request.Owner}, 10, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := dispatchWorker.DispatchOnce(ctx); err != nil || count != 1 {
		t.Fatalf("OWNER_CLOSE_PREFLIGHT: worker did not dispatch the committed admission: count=%d %v", count, err)
	}
	kube, err := dynamic.NewForConfig(&rest.Config{Host: f.kube.URL, BearerToken: "synthetic-kube-owner", Timeout: 10 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.kube.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	assemble := func(pool *pgxpool.Pool) (*biz.ManagedSteps, *biz.ManagedRuntime, *runtimeproof.Verifier) {
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
		return steps, managed, proof
	}
	steps, managed, _ := assemble(pool)
	client, stop := startMainFlowStepHandler(t, service.NewRuntimeStep(steps, managed))
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	if _, err := client.BeginExecution(bootstrapCall(ctx, f, "synthetic-bound-prepare"), &modeldevv1.BeginExecutionRequest{Context: f.stepContext("prepare")}); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "projected-token")
	if err := os.WriteFile(tokenFile, []byte("synthetic-bound-prepare"), 0600); err != nil {
		t.Fatal(err)
	}
	prepare, err := component.New(component.Config{TenantID: f.request.Admission.TenantID, Context: f.stepContext("prepare"), TokenFile: tokenFile, WorkspaceDirectory: f.root, PVCName: f.workspace.PVCName}, client, kube, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepare.Run(ctx, "prepare"); err != nil {
		t.Fatalf("OWNER_CLOSE_PREFLIGHT: real preparation failed: %v", err)
	}
	f.finishStep("prepare")
	if err := os.MkdirAll(filepath.Join(f.root, f.workspace.TrainingSubpath), 0700); err != nil {
		t.Fatal(err)
	}
	trainCall := bootstrapCall(ctx, f, "synthetic-bound-train-wait")
	if _, err := client.EnsureTraining(trainCall, &modeldevv1.EnsureTrainingRequest{Context: f.stepContext("train-wait")}); err != nil {
		t.Fatalf("OWNER_CLOSE_PREFLIGHT: real training creation failed: %v", err)
	}
	awaitOwnerCloseOptimizerStep(t, ctx, f)
	if _, err := client.GetTrainingStatus(trainCall, &modeldevv1.GetTrainingStatusRequest{Context: f.stepContext("train-wait")}); err != nil {
		t.Fatalf("OWNER_CLOSE_PREFLIGHT: real running writer observation failed: %v", err)
	}
	before, err := lifecycle.New(pool).GetRuntime(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || before.TrainingHandle == nil || before.Observation == nil || before.Observation.WritersAbsent || before.Observation.Outcome != "RUNNING" {
		t.Fatalf("OWNER_CLOSE_PREFLIGHT: training was not observed running: %+v %v", before, err)
	}

	// Deliver through the real Governance mTLS receiver before discarding every
	// business owner and its PG pool. A CLOSING receipt does not stop the writer.
	var closeGeneration uint64
	if startup != "" {
		writeOwnerCloseSignal(t, startup, "training-started.json", map[string]any{"operation_id": f.request.Admission.OperationID, "execution_id": f.request.Admission.ExecutionID})
		closeGeneration = awaitBusinessCloseIntent(t, ctx, startup, f, execution.New(pool))
	} else {
		query := startMainFlowQuery(t, pool, store, f.request.Admission)
		connection, err := grpc.NewClient(query.address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: query.certificates.Roots, ServerName: "ani-modeldev-service", Certificates: []tls.Certificate{query.certificates.Governance}})))
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		commandCall := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", f.request.Admission.TenantID, "x-ani-actor", f.request.Admission.Actor, "x-ani-request-id", "11111111-9999-4333-8444-555555555555"))
		receipt, err := modeldevv1.NewModelDevCommandServiceClient(connection).ApplyCloseIntent(commandCall, &modeldevv1.ApplyCloseIntentRequest{Identity: f.stepContext("train-wait").Identity, ResourceTenantId: f.request.Admission.TenantID, RequestedActorId: f.request.Admission.Actor, IntentGeneration: 1, Reason: modeldevv1.CloseReason_CLOSE_REASON_USER_STOP, RequestedAt: timestamppb.New(time.Now().UTC().Truncate(time.Microsecond))})
		if err != nil || !receipt.GetDurablyRecorded() || receipt.GetCloseGeneration() == 0 {
			t.Fatalf("OWNER_CLOSE_PREFLIGHT: real close command was not committed: %v", err)
		}
		closeGeneration = receipt.CloseGeneration
	}
	stop()
	stop = nil
	pool.Close()
	pool = open()
	steps, managed, _ = assemble(pool)
	intent, err := execution.New(pool).GetCloseIntent(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || intent.Generation != closeGeneration || intent.Reason != biz.CloseReasonUserStop {
		t.Fatalf("OWNER_CLOSE_PREFLIGHT: restarted owner lost committed USER_STOP: %v", err)
	}
	select {
	case <-f.trainingDone:
		t.Fatal("OWNER_CLOSE_PREFLIGHT: writer exited before owner reconciliation")
	default:
	}
	t.Log("OWNER_CLOSE_PREFLIGHT: real PG/mTLS admission, authenticated prepare, actual optimizer step, running writer history and USER_STOP recovered after reconnect")
	stopWorker, workerDone := restartRecoveryProcess(t, ctx, f, pool)
	defer func() { stopWorker(); <-workerDone }()
	closeContext, cancelClose := context.WithTimeout(ctx, 15*time.Second)
	defer cancelClose()
	closeTicker := time.NewTicker(50 * time.Millisecond)
	defer closeTicker.Stop()
	var closed biz.ExecutionRuntime
	for {
		closed, err = lifecycle.New(pool).GetRuntime(closeContext, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
		if err != nil {
			t.Fatalf("OWNER_CLOSE_NOT_IMPLEMENTED: cannot observe automatic owner closure: %v", err)
		}
		if closed.ClosedAt != nil {
			break
		}
		select {
		case err := <-workerDone:
			t.Fatalf("OWNER_CLOSE_NOT_IMPLEMENTED: owner worker exited before CLOSED: %v", err)
		case <-closeContext.Done():
			t.Fatalf("OWNER_CLOSE_NOT_IMPLEMENTED: automatic owner did not close USER_STOP: %+v", closed)
		case <-closeTicker.C:
		}
	}
	stopWorker()
	if err := <-workerDone; err != nil {
		t.Fatalf("owner worker failed during shutdown: %v", err)
	}
	if closed.CloseGeneration != intent.Generation || closed.CloseReason != "USER_STOP" || closed.Observation == nil || !closed.Observation.WritersAbsent || closed.CloseEvidence == nil || closed.Publication != nil {
		t.Fatalf("OWNER_CLOSE_NOT_IMPLEMENTED: USER_STOP lacks durable writer-free CLOSED: %+v %v", closed, err)
	}
	select {
	case <-f.trainingDone:
	default:
		t.Fatal("owner closed while the actual training process remained active")
	}
	f.mu.Lock()
	trainingErr, runStops, trainStops := f.trainingErr, f.runStops, f.trainStops
	f.mu.Unlock()
	if trainingErr == nil || runStops == 0 || trainStops == 0 {
		t.Fatal("owner did not stop both KFP and the actual training process")
	}
	client, stop = startMainFlowStepHandler(t, service.NewRuntimeStep(steps, managed))
	if replay, err := client.EnsureTraining(bootstrapCall(ctx, f, "synthetic-bound-train-wait"), &modeldevv1.EnsureTrainingRequest{Context: f.stepContext("train-wait")}); err == nil && (!replay.GetReplayed() || replay.GetStatus().GetTrainjob().GetUid() != completeTrainUID || replay.GetStatus().GetStates().GetCloseState() != modeldevv1.CloseState_CLOSE_STATE_CLOSED) {
		t.Fatal("closed execution did not replay only its original fenced TrainJob")
	}
	f.mu.Lock()
	creates, runCreates := f.creates, f.runCreates
	f.mu.Unlock()
	if creates != 1 || runCreates != 1 {
		t.Fatalf("stop/recovery repeated creation: TrainJob=%d Run=%d", creates, runCreates)
	}
	t.Log("OWNER_CLOSE: actual slow-stop MLP terminated; all writer exits observed; recovered USER_STOP persisted CLOSED; subsequent Ensure cannot create")
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
				t.Fatal("BFF_STOP_NOT_IMPLEMENTED: no BFF CLOSED query acknowledgement")
			case <-ticker.C:
			}
		}
	}
}

func writeOwnerCloseSignal(t *testing.T, startup, name string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(startup), name)
	file, err := os.OpenFile(path+".pending", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("cannot reserve owner-close handshake")
	}
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || os.Rename(path+".pending", path) != nil {
		t.Fatal("cannot publish owner-close handshake")
	}
}

func awaitBusinessCloseIntent(t *testing.T, ctx context.Context, startup string, f *completeFixture, repository biz.ExecutionRepository) uint64 {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		intent, err := repository.GetCloseIntent(ctx, f.request.Admission.TenantID, f.request.Admission.ExecutionID)
		if err == nil {
			if intent.OperationID != f.request.Admission.OperationID || intent.SpecHash != f.request.Admission.SpecHash || intent.Reason != biz.CloseReasonUserStop || intent.Generation == 0 || intent.SourceGeneration != 1 {
				t.Fatal("BFF Stop delivery changed the original execution or USER_STOP identity")
			}
			t.Log("BFF_STOP_DELIVERED: real BFF stop command committed in ModelDev")
			return intent.Generation
		}
		if !errors.Is(err, biz.ErrExecutionNotFound) {
			t.Fatalf("BFF Stop receipt read failed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(startup), "stop")); err == nil {
			t.Fatal("BFF_STOP_NOT_IMPLEMENTED: BFF stopped before close intent delivery")
		}
		select {
		case <-f.trainingDone:
			t.Fatal("BFF_STOP_NOT_IMPLEMENTED: actual training exited before BFF Stop delivery")
		case <-ctx.Done():
			t.Fatal("BFF_STOP_NOT_IMPLEMENTED: no durable close intent from BFF")
		case <-ticker.C:
		}
	}
}

func awaitOwnerCloseOptimizerStep(t *testing.T, ctx context.Context, f *completeFixture) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, _ := os.ReadFile(filepath.Join(f.root, f.workspace.TrainingSubpath, "metrics.jsonl"))
		for _, line := range strings.Split(string(data), "\n") {
			var metric struct {
				Schema string `json:"schema"`
				Step   int    `json:"step"`
				Name   string `json:"name"`
			}
			if json.Unmarshal([]byte(line), &metric) == nil && metric.Schema == "ani.metric.v1" && metric.Name == "train.loss" && metric.Step > 0 {
				return
			}
		}
		select {
		case <-f.trainingDone:
			f.mu.Lock()
			trainingErr, output := f.trainingErr, string(f.trainingLog)
			f.mu.Unlock()
			t.Fatalf("OWNER_CLOSE_PREFLIGHT: actual training exited before a real optimizer step: %v: %s", trainingErr, output)
		case <-ctx.Done():
			t.Fatal("OWNER_CLOSE_PREFLIGHT: no actual optimizer step observed")
		case <-ticker.C:
		}
	}
}
