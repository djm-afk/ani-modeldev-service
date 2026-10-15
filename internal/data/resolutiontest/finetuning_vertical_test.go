package resolutiontest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/input"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This replaces only the external Model read/permission boundary. It asserts
// no real Model permission, registration, Governance certificate or ENV proof.
type finetuningModelBoundary struct {
	tenant string
	base   cpup01.BaseModelRef
	calls  int
}

func (m *finetuningModelBoundary) ResolveFinetuningModelFacts(ctx context.Context, tenant string, intent cpup01.Intent, release cpup01.ReleaseDocument) (cpup01.BaseModelRef, error) {
	if err := ctx.Err(); err != nil {
		return cpup01.BaseModelRef{}, err
	}
	m.calls++
	if tenant != m.tenant || intent.BaseModelVersionID == nil || *intent.BaseModelVersionID != m.base.VersionID || release.Finetuning == nil || release.Finetuning.BaseModelVersionID != m.base.VersionID || intent.NewModelName == nil || *intent.NewModelName != "vertical-fixture" || intent.TargetModelID != nil {
		return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
	}
	return m.base, nil
}

type finetuningVerticalFixture struct {
	ctx       context.Context
	openPool  func() *pgxpool.Pool
	catalogue *catalogue.Reader
	directory string
	release   cpup01.ReleaseDocument
	digest    string
	request   biz.InputImport
	accepted  time.Time
	facts     *admissionfacts.Reader
	models    *finetuningModelBoundary
	resolver  *biz.AdmissionResolver
	materials *biz.ManagedMaterials
}

func prepareFinetuningVertical(t *testing.T, rows int) finetuningVerticalFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	openPool := postgres.Prepare(t)
	release := conformance.FinetuningReleaseV1()
	b := []cpup01.Parameter{{Name: "learning_rate", Type: "DECIMAL", Value: "0.0002"}, {Name: "max_steps", Type: "INTEGER", Value: "10"}}
	release.Program.AllowedParameterSets = append(release.Program.AllowedParameterSets, b)
	for index := range release.Program.ParameterRules {
		for _, parameter := range b {
			if release.Program.ParameterRules[index].Name == parameter.Name {
				release.Program.ParameterRules[index].AllowedValues = append(release.Program.ParameterRules[index].AllowedValues, parameter)
			}
		}
	}
	raw, err := release.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	digest := finetuningHash(raw)
	directory := t.TempDir()
	if _, err := catalogue.ImportRelease(ctx, directory, raw, digest); err != nil {
		t.Fatal(err)
	}
	data := finetuningJSON(t, rows)
	version := "fixed-json-version-v1"
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	request := biz.InputImport{Format: "JSON", TenantID: "11111111-2222-4333-8444-555555555555", RequestID: uuid.NewString(), InputVersionID: conformance.FinetuningIntentV1().DatasetVersionID, Actor: "governance:user:fixture", RequestedAt: accepted.Add(-time.Minute),
		Scope:  cpup01.StorageScope{StorageConnectionID: "json-fixture-store", Bucket: "fixture-inputs", ApprovedPrefix: "tenant/json", CredentialReference: "fixture-data-reader"},
		Object: cpup01.FixedObjectRef{StorageConnectionID: "json-fixture-store", Bucket: "fixture-inputs", Key: "tenant/json/original.json", VersionID: &version, SizeBytes: int64(len(data)), SHA256: finetuningHash(data)}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/"+request.Object.Bucket+"/"+request.Object.Key || r.URL.Query().Get("versionId") != version {
			t.Error("fixture request did not fix the approved object/version")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("x-amz-version-id", version)
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	client := s3.New(s3.Options{Region: "test", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, HTTPClient: server.Client(), Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1})
	frozen := conformance.FinetuningSnapshotV1()
	factsValue := biz.TenantAdmissionFacts{TenantID: request.TenantID, Environment: frozen.Environment, PublicationScope: frozen.PublicationScope, InputScope: request.Scope, Runtime: release.Runtime, Workspace: release.Workspace, InputFilePath: "/workspace/inputs/data/finetune_data.json", OutputDirectoryPath: "/workspace/training"}
	source := writeManagedFactsFixture(t, resolutionFixture{ctx: ctx, release: release, selection: biz.AdmissionResolutionRequest{TenantID: request.TenantID, Release: biz.AdmissionReleaseSelection{ReleaseID: release.ReleaseID, ReleaseDigest: digest}}, facts: factsValue})
	facts, err := admissionfacts.Load(ctx, []admissionfacts.FileSource{source})
	if err != nil {
		t.Fatal(err)
	}
	reader := catalogue.NewReader(directory)
	repository := input.New(openPool())
	importer := biz.NewInputImporter(repository, objectstore.NewVerifier(client, request.Object.StorageConnectionID, 32<<20))
	materials := biz.NewManagedMaterials(reader, facts, repository, importer)
	// The test supplies the already verified upstream delegation boundary. All
	// codec/import/validation/READY persistence after it is production code.
	management := service.NewManagement(materials)
	scope := service.GovernanceDelivery{TenantID: request.TenantID, Actor: request.Actor, RequestID: request.RequestID}
	importContext := service.WithVerifiedGovernanceQuery(ctx, scope, modeldevv1.ModelDevManagementService_ImportFinetuningDataset_FullMethodName)
	response, err := management.ImportFinetuningDataset(importContext, &modeldevv1.ImportFinetuningDatasetRequest{InputVersionId: request.InputVersionID, ReleaseId: release.ReleaseID, ReleaseDigest: digest, Format: "JSON", RequestedAt: timestamppb.New(request.RequestedAt), Object: &trainingv1.FixedObjectRef{StorageConnectionId: request.Object.StorageConnectionID, Bucket: request.Object.Bucket, Key: request.Object.Key, SizeBytes: request.Object.SizeBytes, Sha256: request.Object.SHA256, Immutability: &trainingv1.FixedObjectRef_VersionId{VersionId: version}}})
	if err != nil || response.InputVersion.State != modeldevv1.InputState_INPUT_STATE_READY || response.InputVersion.RowCount != uint32(rows) {
		t.Fatalf("real JSON import did not durably produce READY: %v", err)
	}
	stored, err := input.New(openPool()).Get(ctx, request.TenantID, request.InputVersionID)
	if err != nil || stored.State != biz.InputStateReady || stored.Verification != nil || stored.AlpacaVerification == nil || stored.AlpacaVerification.RowCount != uint32(rows) || stored.Import.Object.SHA256 != request.Object.SHA256 {
		t.Fatalf("new DB connection could not recover true JSON READY: %v", err)
	}
	models := &finetuningModelBoundary{tenant: request.TenantID, base: frozen.Finetuning.BaseModel}
	resolver := biz.NewFinetuningAdmissionResolver(reader, input.New(openPool()), facts, models)
	return finetuningVerticalFixture{ctx: ctx, openPool: openPool, catalogue: reader, directory: directory, release: release, digest: digest, request: request, accepted: accepted, facts: facts, models: models, resolver: resolver, materials: materials}
}

func TestFinetuningJSONReadyToFormalAdmissionInboxAndFrozenReplay(t *testing.T) {
	f := prepareFinetuningVertical(t, 64)
	for _, test := range []struct {
		name, parameters, rate string
		steps                  int64
	}{{"default", "{}", "0.0001", 20}, {"nondefault", `{"learning_rate":"0.0002","max_steps":10}`, "0.0002", 10}} {
		t.Run(test.name, func(t *testing.T) {
			intent := finetuningIntent(t, f, test.parameters)
			raw, hash, err := cpup01.CanonicalIntent(intent)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := contractpb.EncodeIntent(intent)
			if err != nil {
				t.Fatal(err)
			}
			context := service.WithVerifiedGovernanceDelivery(f.ctx, service.GovernanceDelivery{TenantID: f.request.TenantID, Actor: f.request.Actor, RequestID: uuid.NewString()})
			resolved, err := service.NewAdmission(f.resolver).ResolveAdmission(context, &modeldevv1.ResolveAdmissionRequest{Intent: wire, Release: &modeldevv1.AdmissionReleaseSelection{ReleaseId: f.release.ReleaseID, ReleaseDigest: f.digest, BindingGeneration: 7}, AcceptedAt: timestamppb.New(f.accepted)})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := contractpb.DecodeSnapshot(resolved.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Program.ResolvedParameters[0].Value != test.rate || snapshot.Program.ResolvedParameters[1].Value != strconv.FormatInt(test.steps, 10) || snapshot.Input.RowCount != 64 || snapshot.Input.Object.SHA256 != f.request.Object.SHA256 {
				t.Fatal("parameters or READY bytes did not enter frozen snapshot")
			}
			configuration := cpup01.FinetuningConfiguration{ParameterContractVersion: cpup01.FinetuningParameterContractVersion, Parameters: snapshot.Program.ResolvedParameters, ManagedSettings: snapshot.Finetuning.ManagedSettings}
			configurationHash, err := configuration.Digest()
			if err != nil || configurationHash != snapshot.Finetuning.ResolvedConfigurationSHA256 {
				t.Fatal("configuration digest lost typed values")
			}
			operation, id := uuid.NewString(), uuid.NewString()
			command := &modeldevv1.AcceptExecutionRequest{Identity: &trainingv1.ExecutionIdentity{OperationId: operation, ExecutionId: id, ExecutionSpecHash: resolved.ExecutionSpecHash}, ResourceTenantId: f.request.TenantID, AdmittedActorId: f.request.Actor, IntentHash: hash, Snapshot: resolved.Snapshot, AcceptedAt: timestamppb.New(f.accepted), Intent: wire}
			receipt, err := service.NewCommand(execution.New(f.openPool())).AcceptExecution(context, command)
			if err != nil || receipt.Replayed || receipt.Revision == 0 {
				t.Fatalf("formal accepted envelope did not persist inbox: %v", err)
			}
			recovered, err := execution.New(f.openPool()).Get(f.ctx, f.request.TenantID, id)
			if err != nil {
				t.Fatal(err)
			}
			storedIntent, storedSnapshot, err := recovered.CanonicalPayloads()
			if err != nil || !bytes.Equal(storedIntent, raw) {
				t.Fatal("stored inbox changed original field presence")
			}
			// A different current catalogue default/allowed set cannot rewrite the
			// original key. Delivery replay checks only the original frozen envelope.
			newRelease := conformance.FinetuningReleaseV1()
			newRelease.ReleaseID = uuid.NewString()
			newRaw, err := newRelease.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := catalogue.ImportRelease(f.ctx, f.directory, newRaw, finetuningHash(newRaw)); err != nil {
				t.Fatal(err)
			}
			replayed, err := service.NewCommand(execution.New(f.openPool())).AcceptExecution(context, command)
			if err != nil || !replayed.Replayed || replayed.Revision != receipt.Revision {
				t.Fatalf("original envelope replay failed or advanced revision: %v", err)
			}
			stored, err := execution.New(f.openPool()).Get(f.ctx, f.request.TenantID, id)
			if err != nil {
				t.Fatal(err)
			}
			_, after, err := stored.CanonicalPayloads()
			if err != nil || !bytes.Equal(after, storedSnapshot) || !stored.Snapshot.DeadlineAt.Equal(snapshot.DeadlineAt) {
				t.Fatal("new catalogue changed original snapshot/deadline")
			}
		})
	}
}

func TestFinetuningAdmissionRejectsPartialCombinationsAndNon64READYBeforeModel(t *testing.T) {
	f := prepareFinetuningVertical(t, 64)
	for _, parameters := range []string{`{"learning_rate":"0.0002"}`, `{"max_steps":10}`, `{"learning_rate":"1","max_steps":200}`} {
		intent := finetuningIntent(t, f, parameters)
		before := f.models.calls
		if _, err := f.resolver.ResolveManaged(f.ctx, biz.AdmissionResolutionRequest{TenantID: f.request.TenantID, Intent: intent, Release: biz.AdmissionReleaseSelection{ReleaseID: f.release.ReleaseID, ReleaseDigest: f.digest, BindingGeneration: 7}, AcceptedAt: f.accepted}); !errors.Is(err, cpup01.ErrInvalidArgument) || f.models.calls != before {
			t.Fatalf("unapproved combination reached Model boundary: %v", err)
		}
	}
	other := prepareFinetuningVertical(t, 63)
	intent := finetuningIntent(t, other, "{}")
	if _, err := other.resolver.ResolveManaged(other.ctx, biz.AdmissionResolutionRequest{TenantID: other.request.TenantID, Intent: intent, Release: biz.AdmissionReleaseSelection{ReleaseID: other.release.ReleaseID, ReleaseDigest: other.digest, BindingGeneration: 7}, AcceptedAt: other.accepted}); !errors.Is(err, biz.ErrAdmissionInputNotReady) || other.models.calls != 0 {
		t.Fatalf("actual 63-row READY reached computation admission: %v", err)
	}
	var count int
	if err := other.openPool().QueryRow(other.ctx, "SELECT count(*) FROM modeldev_executions").Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected input created execution inbox")
	}
}

func TestFinetuningJSONImportRejectsUnverifiedCallerAndWrongFormat(t *testing.T) {
	f := prepareFinetuningVertical(t, 64)
	management := service.NewManagement(f.materials)
	request := &modeldevv1.ImportFinetuningDatasetRequest{Format: "CSV"}
	if _, err := management.ImportFinetuningDataset(f.ctx, request); status.Code(err) != codes.Unauthenticated {
		t.Fatal("ordinary caller acquired JSON management surface")
	}
	ctx := service.WithVerifiedGovernanceQuery(f.ctx, service.GovernanceDelivery{TenantID: f.request.TenantID, Actor: f.request.Actor, RequestID: uuid.NewString()}, modeldevv1.ModelDevManagementService_ImportFinetuningDataset_FullMethodName)
	if _, err := management.ImportFinetuningDataset(ctx, request); status.Code(err) != codes.InvalidArgument {
		t.Fatal("non-JSON format accepted")
	}
}

func finetuningIntent(t *testing.T, f finetuningVerticalFixture, parameters string) cpup01.Intent {
	t.Helper()
	raw := `{"name":"vertical-finetuning","kind":"FINETUNING","preset_id":"` + f.release.PresetID + `","dataset_version_id":"` + f.request.InputVersionID + `","base_model_version_id":"` + f.models.base.VersionID + `","finetuning":` + parameters + `,"new_model_name":"vertical-fixture"}`
	intent, err := cpup01.ParseIntent([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return intent
}
func finetuningJSON(t *testing.T, count int) []byte {
	t.Helper()
	rows := make([]map[string]string, count)
	for i := range rows {
		rows[i] = map[string]string{"instruction": "answer two plus two", "input": "", "output": "four"}
	}
	raw, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}
func finetuningHash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
