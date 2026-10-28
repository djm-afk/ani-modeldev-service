package resolutiontest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Only Model authorization/fixed-object reads are replaced at this external
// boundary. Import, typed compilation, inbox persistence and replay are real.
type finetuningModelBoundary struct {
	tenant string
	base   cpup01.BaseModelRef
	calls  int
}

func (m *finetuningModelBoundary) ResolveFinetuningModelFacts(ctx context.Context, tenant string, intent cpup01.Intent, material cpup01.ImplementationMaterial) (cpup01.BaseModelRef, error) {
	if e := ctx.Err(); e != nil {
		return cpup01.BaseModelRef{}, e
	}
	m.calls++
	if tenant != m.tenant || intent.BaseModelVersionID == nil || *intent.BaseModelVersionID != m.base.VersionID || material.Finetuning == nil {
		return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
	}
	return m.base, nil
}

type finetuningVerticalFixture struct {
	resolutionFixture
	importedFine biz.InputImport
	models       *finetuningModelBoundary
	management   *service.Management
}

func prepareFinetuningVertical(t *testing.T, rows int) finetuningVerticalFixture {
	t.Helper()
	f := prepareResolution(t)
	intent := conformance.FinetuningIntentV1()
	intent.DatasetVersionID = uuid.NewString()
	material := conformance.FinetuningMaterialV21()
	writeMaterial(t, f.directory, material, "cpu")
	f.request.Intent = intent
	f.request.IdentityRef.OperationID = uuid.NewString()
	f.request.IdentityRef.ExecutionID = uuid.NewString()
	rehashRequest(&f.request)
	data := finetuningJSON(t, rows)
	version := "fixed-json-version-v21"
	request := biz.InputImport{Format: "JSON", TenantID: f.request.IdentityRef.TenantID, RequestID: uuid.NewString(), InputVersionID: intent.DatasetVersionID, Actor: f.request.IdentityRef.Actor, RequestedAt: time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute), Scope: f.facts.InputScope, Object: cpup01.FixedObjectRef{StorageConnectionID: f.facts.InputScope.StorageConnectionID, Bucket: f.facts.InputScope.Bucket, Key: f.facts.InputScope.ApprovedPrefix + "/" + intent.DatasetVersionID + ".json", VersionID: &version, SizeBytes: int64(len(data)), SHA256: finetuningHash(data)}}
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/"+request.Object.Bucket+"/"+request.Object.Key || r.URL.Query().Get("versionId") != version {
			t.Error("object access did not preserve original scope/version")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("x-amz-version-id", version)
		_, _ = w.Write(data)
	}))
	t.Cleanup(storage.Close)
	client := s3.New(s3.Options{Region: "test", BaseEndpoint: aws.String(storage.URL), UsePathStyle: true, HTTPClient: storage.Client(), Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1})
	f.facts.InputFilePath = "/workspace/inputs/data/finetune_data.json"
	f.facts.OutputDirectoryPath = "/workspace/training"
	reader, e := admissionfacts.Load(f.ctx, []admissionfacts.FileSource{writeManagedFactsFixture(t, f)})
	if e != nil {
		t.Fatal(e)
	}
	repository := input.New(f.openPool())
	importer := biz.NewInputImporter(repository, objectstore.NewVerifier(client, request.Object.StorageConnectionID, 32<<20))
	management := service.NewManagement(biz.NewManagedMaterials(catalogue.NewReader(f.directory), reader, repository, importer))
	scope := service.GovernanceDelivery{TenantID: request.TenantID, Actor: request.Actor, RequestID: request.RequestID}
	ctx := service.WithVerifiedGovernanceQuery(f.ctx, scope, modeldevv1.ModelDevManagementService_ImportFinetuningDataset_FullMethodName)
	imported, e := management.ImportFinetuningDataset(ctx, &modeldevv1.ImportFinetuningDatasetRequest{ImplementationId: intent.ImplementationID, InputVersionId: intent.DatasetVersionID, Format: "JSON", RequestedAt: timestamppb.New(request.RequestedAt), Object: &trainingv1.FixedObjectRef{StorageConnectionId: request.Object.StorageConnectionID, Bucket: request.Object.Bucket, Key: request.Object.Key, SizeBytes: request.Object.SizeBytes, Sha256: request.Object.SHA256, Immutability: &trainingv1.FixedObjectRef_VersionId{VersionId: version}}})
	if e != nil || imported.InputVersion.State != modeldevv1.InputState_INPUT_STATE_READY || imported.InputVersion.RowCount != uint32(rows) {
		t.Fatal("real import did not persist nonfixed READY", e)
	}
	stored, e := input.New(f.openPool()).Get(f.ctx, request.TenantID, intent.DatasetVersionID)
	if e != nil || stored.AlpacaVerification == nil || stored.AlpacaVerification.RowCount != uint32(rows) {
		t.Fatal("READY did not survive a new connection", e)
	}
	models := &finetuningModelBoundary{tenant: request.TenantID, base: conformance.FinetuningSnapshotV1().Finetuning.BaseModel}
	f.resolver = biz.NewFinetuningAdmissionResolver(catalogue.NewReader(f.directory), input.New(f.openPool()), reader, models, &fixtureImages{selection: conformance.TrainingImageSelectionV1()})
	return finetuningVerticalFixture{resolutionFixture: f, importedFine: request, models: models, management: management}
}
func TestFinetuningRealJSONReadyTypedCompileDurableInboxAndFrozenReplay(t *testing.T) {
	f := prepareFinetuningVertical(t, 71)
	lr, dropout := "0.00015", "0.1"
	steps, batch, accum, cutoff, rank, alpha := int64(301), int64(2), int64(3), int64(256), int64(13), int64(19)
	seed := uint32(123)
	f.request.Intent.Finetuning = &cpup01.FinetuningParameters{LearningRate: &lr, MaxSteps: &steps, PerDeviceTrainBatchSize: &batch, GradientAccumulationSteps: &accum, CutoffLen: &cutoff, LoraRank: &rank, LoraAlpha: &alpha, LoraDropout: &dropout, Seed: &seed}
	rehashRequest(&f.request)
	candidate, e := f.resolver.Prepare(f.ctx, f.request)
	if e != nil {
		t.Fatal(e)
	}
	resolved, e := f.resolver.Finalize(f.ctx, candidate, nil)
	if e != nil {
		t.Fatal(e)
	}
	s := resolved.Snapshot
	if len(s.ProgramParameters()) != 9 || s.Finetuning.ManagedSettings.LoraRank != 13 || s.Finetuning.ManagedSettings.LoraAlpha != 19 || s.Finetuning.ManagedSettings.DataSeed != 123 || s.Input.RowCount != 71 || s.Finetuning.ManagedSettings.PerDeviceTrainBatchSize != 2 || s.Finetuning.ManagedSettings.GradientAccumulationSteps != 3 {
		t.Fatal("full typed parameter consumption lost")
	}
	intent, e := contractpb.EncodeIntent(f.request.Intent)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := contractpb.EncodeSnapshot(s)
	if e != nil {
		t.Fatal(e)
	}
	id := f.request.IdentityRef
	command := &modeldevv1.AcceptExecutionRequest{Identity: &trainingv1.ExecutionIdentity{OperationId: id.OperationID, ExecutionId: id.ExecutionID, ExecutionSpecHash: resolved.SpecHash}, ResourceTenantId: id.TenantID, AdmittedActorId: id.Actor, IntentHash: id.IntentHash, Snapshot: snapshot, AcceptedAt: timestamppb.New(f.request.AcceptedAt), Intent: intent}
	connection := startAdmissionConnection(t, f.resolutionFixture, service.NewAdmission(f.resolver))
	client := modeldevv1.NewModelDevCommandServiceClient(connection)
	ctx := admissionRPCContext(f.ctx, id.TenantID)
	first, e := client.AcceptExecution(ctx, command)
	if e != nil || first.Replayed || first.Revision == 0 {
		t.Fatal("formal inbox did not commit", e)
	}
	again, e := client.AcceptExecution(ctx, command)
	if e != nil || !again.Replayed || again.Identity.ExecutionId != id.ExecutionID {
		t.Fatal("formal replay created another execution", e)
	}
	stored, e := execution.New(f.openPool()).Get(f.ctx, id.TenantID, id.ExecutionID)
	if e != nil || stored.SpecHash != resolved.SpecHash || len(stored.Snapshot.ProgramParameters()) != 9 {
		t.Fatal("inbox lost frozen plan", e)
	}
}
func TestFinetuningModelScopeAndCutoffRejectBeforeAccept(t *testing.T) {
	f := prepareFinetuningVertical(t, 19)
	cutoff := int64(32769)
	f.request.Intent.Finetuning.CutoffLen = &cutoff
	rehashRequest(&f.request)
	if _, e := f.resolver.Prepare(f.ctx, f.request); e == nil {
		t.Fatal("cutoff above actual material/model boundary accepted")
	}
	f.request.Intent.Finetuning.CutoffLen = nil
	f.models.tenant = "other-tenant"
	rehashRequest(&f.request)
	if _, e := f.resolver.Prepare(f.ctx, f.request); e != biz.ErrModelDependencyUnavailable {
		t.Fatal("Model authorization boundary bypassed", e)
	}
	requireNoResolvedExecution(t, f.resolutionFixture)
}
func TestFinetuningImportRejectsUnverifiedCallerAndWrongImplementation(t *testing.T) {
	f := prepareFinetuningVertical(t, 7)
	request := &modeldevv1.ImportFinetuningDatasetRequest{ImplementationId: "builtin.mlp", InputVersionId: uuid.NewString(), Format: "JSON"}
	if _, e := f.management.ImportFinetuningDataset(f.ctx, request); status.Code(e) != codes.Unauthenticated {
		t.Fatal("unverified importer accepted", e)
	}
	ctx := service.WithVerifiedGovernanceQuery(f.ctx, service.GovernanceDelivery{TenantID: f.request.IdentityRef.TenantID, Actor: f.request.IdentityRef.Actor, RequestID: uuid.NewString()}, modeldevv1.ModelDevManagementService_ImportFinetuningDataset_FullMethodName)
	if _, e := f.management.ImportFinetuningDataset(ctx, request); status.Code(e) != codes.InvalidArgument {
		t.Fatal("wrong import contract accepted", e)
	}
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
