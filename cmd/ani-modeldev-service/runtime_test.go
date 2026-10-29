package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/storagecredentials"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConfiguredRuntimeDispatchesPersistedCommandAndServesStepTLS(t *testing.T) {
	f := runtimeAppFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	observer := f.openPool()
	if err := observer.Ping(ctx); err != nil {
		t.Fatal("RUNTIME_ASSEMBLY_PREFLIGHT: actual PostgreSQL unavailable")
	}
	preflight, err := f.peer.Client().Get(f.peer.URL + "/preflight")
	if err != nil {
		t.Fatal("RUNTIME_ASSEMBLY_PREFLIGHT: actual TLS API unavailable")
	}
	_ = preflight.Body.Close()
	if preflight.StatusCode != http.StatusOK {
		t.Fatal("RUNTIME_ASSEMBLY_PREFLIGHT: actual TLS API failed")
	}
	if err := f.config.Validate(); err != nil {
		t.Fatalf("RUNTIME_ASSEMBLY_PREFLIGHT: invalid explicit configuration: %v", err)
	}
	app, err := buildApp(f.config, newRuntimeLogger(io.Discard))
	if err != nil {
		t.Fatalf("RUNTIME_ASSEMBLY: cannot assemble actual dependencies: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	t.Cleanup(func() {
		if err := app.Stop(); err != nil {
			t.Errorf("stop runtime assembly: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runtime app: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Error("runtime app did not stop")
		}
	})
	waitForHTTP(t, "http://"+f.config.Server.Admin.Addr+"/healthz")
	waitForHTTP(t, "http://"+f.config.Server.Admin.Addr+"/readyz")
	connection, err := grpc.NewClient(f.config.Server.Grpc.Addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: f.certificates.Roots, ServerName: commandtls.ServerDNSName, Certificates: []tls.Certificate{f.certificates.Governance}})))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	snapshot, err := contractpb.EncodeSnapshot(f.admission.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := contractpb.EncodeIntent(f.admission.Intent)
	if err != nil {
		t.Fatal(err)
	}
	request := &modeldevv1.AcceptExecutionRequest{
		ResourceTenantId: f.admission.TenantID, AdmittedActorId: f.admission.Actor,
		Identity: &trainingv1.ExecutionIdentity{OperationId: f.admission.OperationID, ExecutionId: f.admission.ExecutionID, ExecutionSpecHash: f.admission.SpecHash},
		Intent:   intent, IntentHash: f.admission.IntentHash, Snapshot: snapshot, AcceptedAt: timestamppb.New(f.admission.AcceptedAt),
	}
	commandContext := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", f.admission.TenantID, "x-ani-actor", f.admission.Actor, "x-ani-request-id", uuid.NewString()))
	command := modeldevv1.NewModelDevCommandServiceClient(connection)
	if _, err := command.AcceptExecution(commandContext, request); err != nil {
		t.Fatalf("runtime command admission: %v", err)
	}
	queryContext := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", f.admission.TenantID, "x-ani-actor", "governance:user:9001", "x-ani-request-id", uuid.NewString(), "x-ani-data-scope", "tenant-all", "x-ani-authorized-method", modeldevv1.ModelDevQueryService_GetExecution_FullMethodName))
	queried, err := modeldevv1.NewModelDevQueryServiceClient(connection).GetExecution(queryContext, &modeldevv1.GetExecutionRequest{ExecutionId: f.admission.ExecutionID})
	if err != nil || queried.GetExecution().GetIdentity().GetExecutionId() != f.admission.ExecutionID {
		t.Fatalf("configured runtime query not connected: %v", err)
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		dispatch, err := submission.New(observer).Get(ctx, f.admission.TenantID, f.admission.ExecutionID)
		if err == nil && dispatch.State == biz.PipelineDispatchConfirmed {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("runtime did not automatically dispatch accepted execution: %v", ctx.Err())
		case <-tick.C:
		}
	}
	if f.posts.Load() != 1 {
		t.Fatal("runtime dispatch did not issue exactly one CreateRun")
	}
	stepConnection, err := grpc.NewClient(f.config.Runtime.Step.Addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: f.certificates.Roots, ServerName: commandtls.ServerDNSName})))
	if err != nil {
		t.Fatal(err)
	}
	defer stepConnection.Close()
	callback := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", f.admission.TenantID, "authorization", "Bearer synthetic-invalid-managed-token"))
	_, err = modeldevv1.NewModelDevStepServiceClient(stepConnection).BeginExecution(callback, &modeldevv1.BeginExecutionRequest{Context: &modeldevv1.StepContext{
		Identity: request.Identity, Step: modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE,
		Association: &modeldevv1.RunAssociation{KfpRunId: "55555555-6666-4777-8888-999999999999", NamespaceName: f.admission.Snapshot.Environment.NamespaceName, NamespaceUid: f.admission.Snapshot.Environment.NamespaceUID,
			WorkflowName: "runtime-workflow", WorkflowUid: "aaaaaaaa-1111-4111-8111-bbbbbbbbbbbb", PodName: "runtime-prepare", PodUid: "cccccccc-1111-4111-8111-dddddddddddd"},
	}})
	if status.Code(err) != codes.Unauthenticated || f.reviews.Load() != 1 {
		t.Fatalf("Step TLS listener did not reach the real current TokenReview adapter: code=%s reviews=%d", status.Code(err), f.reviews.Load())
	}
	replay, err := command.AcceptExecution(commandContext, request)
	if err != nil || !replay.Replayed || replay.States.ComputeState != modeldevv1.ComputeState_COMPUTE_STATE_SUBMISSION_CONFIRMED {
		t.Fatalf("runtime admission replay lost committed dispatch: %v", err)
	}
	if f.posts.Load() != 1 {
		t.Fatal("admission replay triggered another KFP POST")
	}
	t.Log("RUNTIME_ASSEMBLY: production command -> durable admission -> automatic worker -> TLS KFP -> confirmed PostgreSQL; dedicated Step TLS -> actual TokenReview")
}

func TestConfiguredRuntimeMissingMaterialsFailWithoutFallback(t *testing.T) {
	for _, name := range []string{"binding digest", "Kubernetes credential", "storage credential"} {
		t.Run(name, func(t *testing.T) {
			f := runtimeAppFixture(t)
			switch name {
			case "binding digest":
				f.config.Runtime.BindingSha256 = strings.Repeat("0", 64)
			case "Kubernetes credential":
				f.config.Runtime.Kubernetes.TokenFile = filepath.Join(t.TempDir(), "missing-token")
			case "storage credential":
				f.config.Runtime.ObjectStorage.CredentialsFile = filepath.Join(t.TempDir(), "missing-credentials")
			}
			if app, err := buildApp(f.config, newRuntimeLogger(io.Discard)); err == nil || app != nil {
				t.Fatal("incomplete runtime started or fell back to command-only")
			}
			if f.posts.Load() != 0 || f.reviews.Load() != 0 {
				t.Fatal("incomplete runtime emitted an outbound operation")
			}
		})
	}
}

type configuredRuntimeFixture struct {
	config         *conf.Bootstrap
	openPool       func() *pgxpool.Pool
	certificates   commandtls.Certificates
	admission      biz.Admission
	peer           *httptest.Server
	posts, reviews *atomic.Int32
}

func runtimeAppFixture(t *testing.T) configuredRuntimeFixture {
	t.Helper()
	config, openPool, certificates, _ := commandAppFixture(t)
	snapshot := conformance.SnapshotV1()
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = now.Add(time.Hour)
	intent := conformance.IntentV1()
	intent.Name = "configured-runtime"
	admission := admissionfixture.Bind(t, biz.Admission{TenantID: uuid.NewString(), Actor: "governance:user:42", OperationID: uuid.NewString(), ExecutionID: uuid.NewString(), Intent: intent, Snapshot: snapshot, AcceptedAt: now})
	snapshot = admission.Snapshot
	posts, reviews := &atomic.Int32{}, &atomic.Int32{}
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/preflight":
			w.WriteHeader(http.StatusOK)
		case "/apis/v2beta1/runs":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-kfp-owner" {
				t.Error("runtime KFP identity/operation mismatch")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			posts.Add(1)
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body["run_id"] = "55555555-6666-4777-8888-999999999999"
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		case "/apis/authentication.k8s.io/v1/tokenreviews":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-kube-owner" {
				t.Error("runtime Kubernetes control identity mismatch")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			reviews.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"apiVersion":"authentication.k8s.io/v1","kind":"TokenReview","status":{"authenticated":false}}`)
		default:
			t.Errorf("unexpected runtime API operation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(peer.Close)
	directory := t.TempDir()
	write := func(name string, data []byte) string {
		filename := filepath.Join(directory, name)
		if err := os.WriteFile(filename, data, 0600); err != nil {
			t.Fatal(err)
		}
		return filename
	}
	caFile := write("external-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}))
	binding := biz.PipelineDispatchBinding{TenantID: admission.TenantID, Environment: snapshot.Environment, Owner: biz.PipelineOwnerConfiguration{Reference: "runtime-owner-v1", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://runtime-pipeline/tenant-root"}}
	bindingBytes, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest := sha256.Sum256(bindingBytes)
	stepAddress := reserveAddress(t)
	for stepAddress == config.Server.Grpc.Addr || stepAddress == config.Server.Admin.Addr {
		stepAddress = reserveAddress(t)
	}
	storageBytes, err := json.Marshal(storageMaterial{AccessKeyID: "synthetic-key", SecretAccessKey: "synthetic-secret", STSBindings: []storagecredentials.Binding{
		{TenantID: admission.TenantID, Purpose: "prepare", ConnectionID: snapshot.Input.Object.StorageConnectionID, Bucket: snapshot.Input.Object.Bucket, Prefix: "fixed/input"},
		{TenantID: admission.TenantID, Purpose: "publish", ConnectionID: snapshot.PublicationScope.StorageConnectionID, Bucket: snapshot.PublicationScope.Bucket, Prefix: snapshot.PublicationScope.ApprovedPrefix},
	}})
	if err != nil {
		t.Fatal(err)
	}
	config.Runtime = &conf.ManagedRuntime{
		Step: &conf.Server_GRPC{Network: "tcp", Addr: stepAddress, Timeout: durationpb.New(3 * time.Second)}, CertificateFile: config.Command.CertificateFile, PrivateKeyFile: config.Command.PrivateKeyFile,
		Kubernetes:    &conf.HTTPSWorkloadConnection{Endpoint: peer.URL, CaFile: caFile, TokenFile: write("kube-token", []byte("synthetic-kube-owner"))},
		Pipeline:      &conf.HTTPSWorkloadConnection{Endpoint: peer.URL, CaFile: caFile, TokenFile: write("pipeline-token", []byte("synthetic-kfp-owner"))},
		Limits:        &conf.RuntimeLimits{MaxContentBytes: 64 << 20},
		ObjectStorage: &conf.ObjectStorageConnection{Endpoint: peer.URL, CaFile: caFile, Region: "us-east-1", CredentialsFile: write("storage-credentials", storageBytes), ConnectionId: snapshot.PublicationScope.StorageConnectionID},
		BindingFile:   write("binding.json", bindingBytes), BindingSha256: hex.EncodeToString(bindingDigest[:]), TokenAudience: "ani-modeldev-managed-step", ApiTimeout: durationpb.New(3 * time.Second), DispatchInterval: durationpb.New(100 * time.Millisecond), DispatchBatchSize: 4,
	}
	return configuredRuntimeFixture{config: config, openPool: openPool, certificates: certificates, admission: admission, peer: peer, posts: posts, reviews: reviews}
}
