package input_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workspace"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// Only immutable material/configuration and remote S3 are fixture boundaries.
// Import, both PG repositories, Prepare/Finalize, canonical compilation and the
// real workspace transformation all execute their normal business code.
type preparedVerticalMaterial struct{ value cpup01.ImplementationMaterial }

func (material preparedVerticalMaterial) ReadImplementationMaterial(context.Context, string, string) (cpup01.ImplementationMaterial, error) {
	return material.value, nil
}

type preparedVerticalFacts struct{ value biz.TenantAdmissionFacts }

// preparedVerticalImages is the catalogue answer for the synthetic fixture.
type preparedVerticalImages struct{}

func (preparedVerticalImages) ResolveTrainingImage(_ context.Context, _ string, id, scope string, platform cpup01.TrainingPlatform) (biz.TrainingImageResolution, error) {
	digest := "sha256:" + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	return biz.TrainingImageResolution{ImageID: id, Scope: scope, ImageVersion: 1, Reference: "registry.example.test/cpu/mlp@" + digest, Digest: digest, RegistryAuthority: "registry.example.test", Platform: platform}, nil
}

func (preparedVerticalImages) EnsurePullSecret(_ context.Context, tenant, cluster, namespace, _, registry string) (string, error) {
	return cpup01.PullSecretName(tenant, cluster, namespace, registry)
}

func (facts preparedVerticalFacts) ReadAdmissionFacts(context.Context, string, string) (biz.TenantAdmissionFacts, error) {
	return facts.value, nil
}

func TestRealImportPreparedFactsAndAcceptedExecutionDriveSingleReadPrepare(t *testing.T) {
	open := postgres.Prepare(t)
	request, contents := managedImportFixture()
	var reads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+request.Object.Bucket+"/"+request.Object.Key || r.URL.Query().Get("versionId") != *request.Object.VersionID {
			t.Error("source identity changed")
			w.WriteHeader(403)
			return
		}
		reads.Add(1)
		w.Header().Set("x-amz-version-id", *request.Object.VersionID)
		_, _ = io.WriteString(w, contents)
	}))
	defer server.Close()
	client := s3.New(s3.Options{Region: "test", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, HTTPClient: server.Client(), Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1})
	limits := cpup01.ContentLimits{MaxContentBytes: request.Object.SizeBytes}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ready, err := biz.NewInputImporter(input.New(open()), objectstore.NewVerifier(client, request.Scope.StorageConnectionID), limits, time.Minute).ImportCSV(ctx, request)
	if err != nil || ready.Verification == nil {
		t.Fatalf("real import failed: %+v %v", ready, err)
	}
	snapshot, material, intent := conformance.SnapshotV1(), conformance.MaterialV21(), conformance.IntentV1()
	intent.DatasetVersionID = request.InputVersionID
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	identity := snapshot.IdentityRef
	identity.TenantID, identity.Actor, identity.IntentHash = request.TenantID, request.Actor, intentHash
	facts := biz.TenantAdmissionFacts{TenantID: request.TenantID, Environment: snapshot.Environment, PublicationScope: snapshot.PublicationScope, InputScope: request.Scope, Workspace: material.Workspace, InputFilePath: "/inputs/data.csv", OutputDirectoryPath: "/outputs", Timing: cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900}, TargetPlatform: cpup01.TrainingPlatform{OS: "linux", Architecture: "amd64"}}
	resolver := biz.NewAdmissionResolver(preparedVerticalMaterial{material}, input.New(open()), preparedVerticalFacts{facts}, preparedVerticalImages{}, limits)
	candidate, err := resolver.Prepare(ctx, biz.PrepareAdmissionInput{IdentityRef: identity, Intent: intent, ComputeOrigin: snapshot.ComputeOrigin, AcceptedAt: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil || candidate.Snapshot.Input.PreparedDataset != ready.Verification.PreparedDataset {
		t.Fatalf("Prepare lost durable input observations: %+v %v", candidate.Snapshot.Input, err)
	}
	resolved, err := resolver.Finalize(ctx, candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	admitted := biz.Admission{TenantID: identity.TenantID, Actor: identity.Actor, OperationID: identity.OperationID, ExecutionID: identity.ExecutionID, Intent: intent, IntentHash: intentHash, Snapshot: resolved.Snapshot, SpecHash: resolved.SpecHash, AcceptedAt: resolved.Snapshot.Timing.AcceptedAt}
	if _, err := execution.New(open()).Accept(ctx, admitted); err != nil {
		t.Fatal("accepted execution was not committed", err)
	}
	persisted, err := execution.New(open()).Get(ctx, identity.TenantID, identity.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	contract := persisted.Snapshot.Workspace
	binding := biz.WorkspaceBinding{Mode: contract.Mode, NamespaceName: persisted.Snapshot.Environment.NamespaceName, NamespaceUID: persisted.Snapshot.Environment.NamespaceUID, PVCName: "isolated-workspace", PVCUID: "55555555-5555-4555-8555-555555555555", InputSubpath: contract.InputSubpath, TrainingSubpath: contract.TrainingSubpath, ReportsSubpath: contract.ReportsSubpath, PublicationSubpath: contract.PublicationSubpath, WorkSubpath: contract.WorkSubpath}
	root := t.TempDir()
	if _, err := workspace.PrepareInput(ctx, client, persisted, binding, root); err != nil {
		t.Fatal("persisted execution could not prepare its source", err)
	}
	if reads.Load() != 2 {
		t.Fatalf("import and prepare require one fixed GET each; observed %d", reads.Load())
	}
	actual, err := os.ReadFile(filepath.Join(root, "inputs/data.csv"))
	if err != nil || string(actual) != contents {
		t.Fatal("prepared bytes differ from imported source", err)
	}
	manifest, err := cpup01.BuildDatasetManifest(persisted.Snapshot.Input)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	actualManifest, err := os.ReadFile(filepath.Join(root, "inputs/dataset-manifest.json"))
	if err != nil || !bytes.Equal(actualManifest, expected) {
		t.Fatal("prepared manifest differs from persisted snapshot", err)
	}
}
