package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
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

func TestConfiguredAdmissionResolvesPinnedFilesAndDurableReadyInput(t *testing.T) {
	fixture := prepareConfiguredAdmission(t)
	connection, stop := startConfiguredAdmissionApp(t, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Log("ADMISSION_STARTUP_PREFLIGHT PASS: real catalogue and pinned facts files, recovered READY PostgreSQL input, actual buildApp mTLS listener")
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
		t.Fatalf("ADMISSION_STARTUP_BEHAVIOR: startup dependencies did not produce the complete independently expected snapshot and hash: %v", err)
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
	assertProductionNotReadyWithoutBusinessAdapters(t, fixture.config.Server.Admin.Addr)
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
	config, openPool, certificates, applicationName := commandAppFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	material, seed := conformance.MaterialV21(), conformance.SnapshotV1()
	directory := t.TempDir()
	materialBytes, err := material.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "builtin.mlp.cpu.json"), materialBytes, 0600); err != nil {
		t.Fatal(err)
	}
	loadedMaterial, err := catalogue.ReadImplementationMaterial(ctx, directory, "builtin.mlp", "cpu")
	if err != nil || !reflect.DeepEqual(loadedMaterial, material) {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: actual finite software material read failed")
	}
	imported := biz.InputImport{TenantID: "11111111-2222-4333-8444-555555555555", RequestID: uuid.NewString(), InputVersionID: seed.Input.InputVersionID, Actor: "governance:user:42", RequestedAt: time.Date(2026, 9, 30, 12, 0, 0, 123000, time.UTC), Scope: cpup01.StorageScope{StorageConnectionID: seed.Input.Object.StorageConnectionID, Bucket: seed.Input.Object.Bucket, ApprovedPrefix: "fixed/input"}, Object: seed.Input.Object}
	writer := openPool()
	repository := input.New(writer)
	if _, err = repository.FreezeImport(ctx, imported); err != nil {
		t.Fatalf("ADMISSION_STARTUP_PREFLIGHT: actual input import failed: %v", err)
	}
	// A synthetic external object observation enters the real import persistence.
	ready, err := repository.RecordVerifiedCSV(ctx, imported, biz.VerifiedCSV{VerifiedObject: biz.VerifiedObject{Object: imported.Object, VerifiedAt: imported.RequestedAt.Add(time.Minute)}, SchemaVersion: "ani.cpu.csv.v1", RowCount: 1027, FeatureCount: 16})
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
	facts := biz.TenantAdmissionFacts{TenantID: imported.TenantID, Environment: seed.Environment, InputScope: imported.Scope, PublicationScope: seed.PublicationScope, Workspace: material.Workspace, InputFilePath: "/inputs/data.csv", OutputDirectoryPath: "/outputs", Timing: cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900}}
	source := writeConfiguredAdmissionFacts(t, material, facts)
	reader, err := admissionfacts.Load(ctx, []admissionfacts.FileSource{source})
	if err != nil {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: actual pinned facts load failed", err)
	}
	loaded, err := reader.ReadAdmissionFacts(ctx, imported.TenantID, "builtin.mlp")
	if err != nil || !reflect.DeepEqual(loaded, facts) {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: pinned facts differ from authored facts")
	}
	config.Command.AdmissionResolution = &conf.AdmissionResolution{CatalogueDirectory: directory, FactsFiles: []*conf.AdmissionFactsFile{{Path: source.Path, Sha256: source.SHA256}}}
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
	acceptedAt := time.Date(2026, 9, 30, 12, 3, 0, 123000, time.UTC)
	identity := cpup01.IdentityRef{TenantID: imported.TenantID, Actor: imported.Actor, OperationID: "88888888-8888-4888-8888-888888888888", ExecutionID: "99999999-9999-4999-8999-999999999999", IntentHash: intentHash}
	// Expected facts are authored from the independent material/input facts and
	// full literal Runtime spec below. No resolver/compiler/RPC result is reused.
	want := seed
	want.IdentityRef = identity
	want.Program.ResolvedParameters = []cpup01.Parameter{{Name: "batch_size", Type: "INTEGER", Value: "64"}, {Name: "epochs", Type: "INTEGER", Value: "3"}, {Name: "learning_rate", Type: "DECIMAL", Value: "0.02"}, {Name: "seed", Type: "INTEGER", Value: "42"}}
	want.Program.ResolvedArgs = []string{"--data", "/inputs/data.csv", "--output", "/outputs", "--learning-rate", "0.02", "--epochs", "3", "--batch-size", "64", "--seed", "42"}
	want.Timing = cpup01.Timing{AcceptedAt: acceptedAt, ExecutionTimeoutSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900, ExecutionDeadlineAt: acceptedAt.Add(time.Hour)}
	want.DeadlineAt = want.Timing.ExecutionDeadlineAt
	want.RuntimePlan = independentConfiguredRuntime(t)
	canonical, err := want.Canonical()
	if err != nil {
		t.Fatal("ADMISSION_STARTUP_PREFLIGHT: independent complete expectation invalid", err)
	}
	digest := sha256.Sum256(canonical)
	request := &modeldevv1.PrepareAdmissionRequest{Intent: wireIntent, IdentityRef: &modeldevv1.IdentityRef{TenantId: identity.TenantID, Actor: identity.Actor, OperationId: identity.OperationID, ExecutionId: identity.ExecutionID, IntentHash: intentHash}, AcceptedAt: timestamppb.New(acceptedAt), ComputeSelectionCanonical: []byte(seed.ComputeOrigin.SelectionJSON), SelectionDigest: seed.ComputeOrigin.SelectionDigest}
	return configuredAdmissionFixture{config: config, openPool: openPool, certificates: certificates, applicationName: applicationName, tenantID: imported.TenantID, ready: ready, request: request, wantCanonical: canonical, wantHash: hex.EncodeToString(digest[:]), wantSnapshot: want, material: material}
}

func independentConfiguredRuntime(t *testing.T) cpup01.RuntimePlan {
	t.Helper()
	const literal = `{"mlPolicy":{"numNodes":1},"podGroupPolicy":{"volcano":{}},"template":{"metadata":{"annotations":{"scheduling.volcano.sh/queue-name":"default"}},"spec":{"failurePolicy":{"maxRestarts":0,"restartStrategy":"Recreate"},"replicatedJobs":[{"groupName":"default","name":"node","replicas":1,"template":{"metadata":{"labels":{"trainer.kubeflow.org/trainjob-ancestor-step":"trainer"}},"spec":{"activeDeadlineSeconds":900,"backoffLimit":0,"completionMode":"Indexed","completions":1,"parallelism":1,"template":{"metadata":{"finalizers":["modeldev.ani.io/training-exit-evidence"]},"spec":{"automountServiceAccountToken":false,"containers":[{"args":["--data","/inputs/data.csv","--output","/outputs","--learning-rate","0.02","--epochs","3","--batch-size","64","--seed","42"],"command":["python","/opt/mlp/train.py"],"image":"registry.example.test/cpu/mlp@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","imagePullPolicy":"IfNotPresent","name":"node","resources":{"limits":{"cpu":"1","memory":"1Gi"},"requests":{"cpu":"1","memory":"1Gi"}},"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true},"volumeMounts":[{"mountPath":"/inputs","name":"input","readOnly":true,"subPath":"inputs"},{"mountPath":"/outputs","name":"output","readOnly":false,"subPath":"training"},{"mountPath":"/tmp","name":"tmp","readOnly":false}]}],"restartPolicy":"Never","schedulerName":"volcano","securityContext":{"fsGroup":10001,"runAsGroup":10001,"runAsNonRoot":true,"runAsUser":10001,"seccompProfile":{"type":"RuntimeDefault"}},"serviceAccountName":"cpu-training","terminationGracePeriodSeconds":5,"volumes":[{"name":"input","persistentVolumeClaim":{"claimName":"ani-kfp-workspace-99999999-9999-4999-8999-999999999999"}},{"name":"output","persistentVolumeClaim":{"claimName":"ani-kfp-workspace-99999999-9999-4999-8999-999999999999"}},{"emptyDir":{"sizeLimit":"4Gi"},"name":"tmp"}]}}}}}]}}}`
	var object any
	if err := json.Unmarshal([]byte(literal), &object); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return cpup01.RuntimePlan{GeneratorVersion: "ani.modeldev.runtime.v1", APIGroup: "trainer.kubeflow.org", Kind: "TrainingRuntime", Namespace: "cpu-contract-fixture", Name: cpup01.RuntimeName("33333333-3333-4333-8333-333333333333", "99999999-9999-4999-8999-999999999999"), ExpectedSpec: raw, ExpectedSpecSHA256: hex.EncodeToString(digest[:]), DefaultingRules: "trainer-2.1.0-jobset-explicit-v1", TargetJobs: []string{"node"}}
}

func writeConfiguredAdmissionFacts(t *testing.T, material cpup01.ImplementationMaterial, facts biz.TenantAdmissionFacts) admissionfacts.FileSource {
	t.Helper()
	type evidence struct {
		Reference string `json:"reference"`
		SHA256    string `json:"sha256"`
	}
	document := struct {
		SchemaVersion       string                            `json:"schema_version"`
		ResourceTenantID    string                            `json:"resource_tenant_id"`
		ImplementationID    string                            `json:"implementation_id"`
		Environment         cpup01.EnvironmentBindingSnapshot `json:"environment"`
		InputScope          cpup01.StorageScope               `json:"input_scope"`
		PublicationScope    cpup01.StorageScope               `json:"publication_scope"`
		Timing              cpup01.TimingConfiguration        `json:"timing"`
		Workspace           cpup01.WorkspaceContract          `json:"workspace"`
		InputFilePath       string                            `json:"input_file_path"`
		OutputDirectoryPath string                            `json:"output_directory_path"`
		EnvironmentEvidence evidence                          `json:"environment_evidence"`
		ApplicationEvidence evidence                          `json:"application_evidence"`
	}{SchemaVersion: "ani.modeldev.managed-admission-facts.v2.1", ResourceTenantID: facts.TenantID, ImplementationID: material.Implementation.ImplementationID, Environment: facts.Environment, InputScope: facts.InputScope, PublicationScope: facts.PublicationScope, Timing: facts.Timing, Workspace: facts.Workspace, InputFilePath: facts.InputFilePath, OutputDirectoryPath: facts.OutputDirectoryPath, EnvironmentEvidence: evidence{Reference: "module-fixture:startup-environment", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, ApplicationEvidence: evidence{Reference: "module-fixture:startup-application", SHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "managed-admission-facts.json")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return admissionfacts.FileSource{Path: file, SHA256: hex.EncodeToString(digest[:])}
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
