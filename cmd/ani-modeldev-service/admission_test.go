package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConfiguredAdmissionResolvesReadyEnvironmentAndDurableReadyInput(t *testing.T) {
	fixture := prepareConfiguredAdmission(t)
	connection, stop := startConfiguredAdmissionApp(t, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Log("ADMISSION_STARTUP_PREFLIGHT PASS: real immutable catalogue, Ensure-sealed READY environment, recovered READY PostgreSQL input, actual buildApp mTLS listener")
	beforeRequest := proto.Clone(fixture.request)
	rpcContext := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"x-ani-tenant-id", fixture.tenantID, "x-ani-actor", "governance:user:42", "x-ani-request-id", uuid.NewString(),
	))
	response, err := finalizeConfiguredAdmission(rpcContext, modeldevv1.NewModelDevAdmissionServiceClient(connection), fixture.request)
	if err != nil {
		if status.Code(err) == codes.Unimplemented && status.Convert(err).Message() == "unknown service ani.modeldev.v1.ModelDevAdmissionService" {
			t.Fatalf("ADMISSION_STARTUP_BEHAVIOR: explicit startup materials did not register Admission on the actual listener: %v", err)
		}
		t.Fatalf("ADMISSION_STARTUP_BEHAVIOR: candidate unavailable (not the expected unregistered-service RED): %v", err)
	}
	if response == nil {
		t.Fatal("ADMISSION_STARTUP_BEHAVIOR: configured admission returned no candidate")
	}
	snapshot, err := contractpb.DecodeSnapshot(response.Snapshot)
	if err != nil {
		t.Fatalf("ADMISSION_STARTUP_BEHAVIOR: invalid wire snapshot: %v", err)
	}
	canonical, err := snapshot.Canonical()
	if err != nil || !bytes.Equal(canonical, fixture.wantCanonical) || response.ExecutionSpecHash != fixture.wantHash {
		t.Fatalf("ADMISSION_STARTUP_BEHAVIOR: startup dependencies did not produce the complete expected environment-bound snapshot and hash: %v", err)
	}
	if !proto.Equal(fixture.request, beforeRequest) {
		t.Fatal("ADMISSION_STARTUP_BEHAVIOR: startup resolution changed the caller's request")
	}
	observer := fixture.openPool()
	stored, err := input.New(observer).Get(ctx, fixture.tenantID, fixture.ready.Import.InputVersionID)
	if err != nil || !reflect.DeepEqual(stored, fixture.ready) {
		t.Fatalf("ADMISSION_STARTUP_BEHAVIOR: candidate resolution changed the durable READY input: %v", err)
	}
	var executions int
	if err := observer.QueryRow(ctx, "SELECT count(*) FROM modeldev_executions").Scan(&executions); err != nil || executions != 0 {
		t.Fatalf("ADMISSION_STARTUP_BEHAVIOR: candidate resolution persisted an execution: count=%d, error=%v", executions, err)
	}
	waitForHTTP(t, "http://"+fixture.config.Server.Admin.Addr+"/readyz")
	if countConfiguredAdmissionConnections(t, observer, fixture.applicationName) == 0 {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: no actual configured database connection; cleanup behavior NOT_RUN")
	}
	_ = connection.Close()
	stop()
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cleanupCancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for countConfiguredAdmissionConnections(t, observer, fixture.applicationName) != 0 {
		select {
		case <-tick.C:
		case <-cleanupContext.Done():
			t.Fatal("ADMISSION_STARTUP_BEHAVIOR: stopped app retained its owned runtime database connection")
		}
	}
	for _, address := range []string{fixture.config.Server.Grpc.Addr, fixture.config.Server.Admin.Addr} {
		if remaining, err := net.DialTimeout("tcp", address, 250*time.Millisecond); err == nil {
			remaining.Close()
			t.Fatal("ADMISSION_STARTUP_BEHAVIOR: listener remained reachable after app exit")
		}
	}
}

func startConfiguredAdmissionApp(t *testing.T, fixture configuredAdmissionFixture) (*grpc.ClientConn, func()) {
	t.Helper()
	app, err := buildApp(fixture.config, newRuntimeLogger(io.Discard))
	if err != nil {
		t.Fatalf("ADMISSION_STARTUP_BEHAVIOR: explicit usable startup materials rejected: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if err := app.Stop(); err != nil {
			t.Errorf("stop configured admission app: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("configured admission app exited: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Error("configured admission app did not stop within its bound")
		}
	}
	t.Cleanup(stop)
	waitForHTTP(t, "http://"+fixture.config.Server.Admin.Addr+"/healthz")
	connection, err := grpc.NewClient(fixture.config.Server.Grpc.Addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: fixture.certificates.Roots, ServerName: commandtls.ServerDNSName,
		Certificates: []tls.Certificate{fixture.certificates.Governance},
	})))
	if err != nil {
		t.Fatalf("ADMISSION_STARTUP_PREFLIGHT: client configuration failed; behavior NOT_RUN: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection.Connect()
	for state := connection.GetState(); state != connectivity.Ready; state = connection.GetState() {
		if !connection.WaitForStateChange(ctx, state) {
			t.Fatalf("ADMISSION_STARTUP_PREFLIGHT: real mTLS channel unavailable; behavior NOT_RUN: %v", ctx.Err())
		}
	}
	t.Log("ADMISSION_STARTUP_PREFLIGHT PASS: actual buildApp/Run and mTLS command listener")
	return connection, stop
}

type configuredAdmissionFixture struct {
	config          *conf.Bootstrap
	openPool        func() *pgxpool.Pool
	certificates    commandtls.Certificates
	applicationName string
	tenantID        string
	ready           biz.InputVersion
	request         *modeldevv1.PrepareAdmissionRequest
	wantCanonical   []byte
	wantHash        string
	wantSnapshot    cpup01.Snapshot
	material        cpup01.ImplementationMaterial
}

func finalizeConfiguredAdmission(ctx context.Context, client modeldevv1.ModelDevAdmissionServiceClient, request *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error) {
	prepared, err := client.PrepareAdmission(ctx, request)
	if err != nil {
		return nil, err
	}
	return client.FinalizeAdmission(ctx, &modeldevv1.FinalizeAdmissionRequest{CandidateCanonical: prepared.CandidateCanonical, CandidateHash: prepared.CandidateHash})
}

func prepareConfiguredAdmission(t *testing.T) configuredAdmissionFixture {
	t.Helper()
	runtimeFixture := runtimeAppFixture(t)
	config, openPool, certificates := runtimeFixture.config, runtimeFixture.openPool, runtimeFixture.certificates
	databaseReference, err := os.ReadFile(config.Command.DatabaseUrlFile)
	if err != nil {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: protected database reference unavailable")
	}
	connection, err := url.Parse(string(databaseReference))
	if err != nil || connection.Query().Get("application_name") == "" {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: configured database application identity unavailable")
	}
	applicationName := connection.Query().Get("application_name")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seed := runtimeFixture.admission.Snapshot
	config.Runtime.Limits.MaxContentBytes = seed.Limits.MaxContentBytes
	directory := runtimeFixture.environment.MaterialsDirectory
	material, err := catalogue.ReadImplementationMaterial(ctx, directory, "builtin.mlp", "cpu")
	if err != nil {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: actual finite software material read failed", err)
	}
	imported := biz.InputImport{TenantID: runtimeFixture.admission.TenantID, RequestID: uuid.NewString(), InputVersionID: seed.Input.InputVersionID, Format: "CSV", Actor: "governance:user:42", RequestedAt: time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute), Scope: cpup01.StorageScope{StorageConnectionID: seed.Input.Object.StorageConnectionID, Bucket: seed.Input.Object.Bucket, ApprovedPrefix: "inputs/" + runtimeFixture.admission.TenantID}, Object: seed.Input.Object}
	writer := openPool()
	repository := input.New(writer)
	if _, err = repository.FreezeImport(ctx, imported); err != nil {
		t.Fatalf("ADMISSION_STARTUP_PREFLIGHT: actual input import failed: %v", err)
	}
	// A synthetic external object observation enters the real import persistence.
	ready, err := repository.RecordVerifiedCSV(ctx, imported, biz.VerifiedCSV{VerifiedObject: biz.VerifiedObject{Object: imported.Object, VerifiedAt: imported.RequestedAt.Add(time.Minute)}, SchemaVersion: "ani.cpu.csv.v1", RowCount: 1027, FeatureCount: 16, PreparedDataset: seed.Input.PreparedDataset})
	if err != nil || ready.State != biz.InputStateReady {
		t.Fatalf("ADMISSION_STARTUP_PREFLIGHT: actual READY persistence failed: %v", err)
	}
	writer.Close()
	observer := openPool()
	recovered, err := input.New(observer).Get(ctx, imported.TenantID, imported.InputVersionID)
	observer.Close()
	if err != nil || !reflect.DeepEqual(recovered, ready) {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: fresh connection cannot recover READY")
	}
	reader, err := admissionfacts.New(tenantenvironment.NewRepository(openPool()), runtimeFixture.environment.Platform, catalogue.NewReader(directory), seed.Environment.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := reader.ReadAdmissionFacts(ctx, imported.TenantID, "builtin.mlp")
	if err != nil {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: actual READY environment read", err)
	}
	config.Command.AdmissionResolution = &conf.AdmissionResolution{CatalogueDirectory: directory}
	rate := "0.0200"
	intent := cpup01.Intent{Name: "configured-admission", Kind: "GENERAL_TRAINING", ImplementationID: "builtin.mlp", ComputePresetID: seed.ComputeOrigin.ComputePresetID, DatasetVersionID: imported.InputVersionID, MLP: &cpup01.MLPParameters{LearningRate: &rate}}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	wireIntent, err := contractpb.EncodeIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	identity := cpup01.IdentityRef{TenantID: imported.TenantID, Actor: imported.Actor, OperationID: "88888888-8888-4888-8888-888888888888", ExecutionID: "99999999-9999-4999-8999-999999999999", IntentHash: intentHash}
	// Freeze independently read catalogue/input/environment facts and authored
	// program parameters. Runtime plan correctness has its own compiler contract tests.
	want := seed
	want.IdentityRef = identity
	want.Program.ResolvedParameters = []cpup01.Parameter{{Name: "batch_size", Type: "INTEGER", Value: "64"}, {Name: "epochs", Type: "INTEGER", Value: "3"}, {Name: "learning_rate", Type: "DECIMAL", Value: "0.02"}, {Name: "seed", Type: "INTEGER", Value: "42"}}
	want.Program.ResolvedArgs = []string{"--dataset-manifest", "/inputs/dataset-manifest.json", "--expected-dataset-manifest-sha256", "8c9f127ef9e0450d3945aee643366e253574a97c3ead985a87e3f9e3617e75ed", "--work-root", "/workspace/work/train", "--output", "/outputs", "--learning-rate", "0.02", "--epochs", "3", "--batch-size", "64", "--seed", "42"}
	want.Timing = cpup01.Timing{AcceptedAt: acceptedAt, ExecutionTimeoutSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900, ExecutionDeadlineAt: acceptedAt.Add(time.Hour)}
	want.DeadlineAt = want.Timing.ExecutionDeadlineAt
	want.Implementation = material.Implementation
	want.Environment, want.ManagedImageAccess, want.PublicationScope, want.Workspace = facts.Environment, facts.ManagedImageAccess, facts.PublicationScope, facts.Workspace
	want.DispatchOwner = facts.DispatchOwner
	want.DispatchOwner.PipelineRoot += "/" + identity.ExecutionID
	want.TrainingImage.PullSecretRef, _ = cpup01.PullSecretName(identity.TenantID, facts.Environment.ClusterID, facts.Environment.NamespaceName, want.TrainingImage.RegistryAuthority)
	want.RuntimePlan, err = biz.CompileRuntimePlan(want)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := want.Canonical()
	if err != nil {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: independent complete expectation invalid", err)
	}
	digest := sha256.Sum256(canonical)
	request := &modeldevv1.PrepareAdmissionRequest{Intent: wireIntent, IdentityRef: &modeldevv1.IdentityRef{TenantId: identity.TenantID, Actor: identity.Actor, OperationId: identity.OperationID, ExecutionId: identity.ExecutionID, IntentHash: intentHash}, AcceptedAt: timestamppb.New(acceptedAt), ComputeSelectionCanonical: []byte(seed.ComputeOrigin.SelectionJSON), SelectionDigest: seed.ComputeOrigin.SelectionDigest}
	return configuredAdmissionFixture{config: config, openPool: openPool, certificates: certificates, applicationName: applicationName, tenantID: imported.TenantID, ready: ready, request: request, wantCanonical: canonical, wantHash: hex.EncodeToString(digest[:]), wantSnapshot: want, material: material}
}

func countConfiguredAdmissionConnections(t *testing.T, observer *pgxpool.Pool, applicationName string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var count int
	if err := observer.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name = $1 AND usename = current_user AND datname = current_database()", applicationName).Scan(&count); err != nil {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: inspect exact task-owned database connection; cleanup behavior NOT_RUN")
	}
	return count
}
