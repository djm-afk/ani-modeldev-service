package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type finetuningQueryFacts struct {
	tenant    string
	byRelease map[string]biz.TenantAdmissionFacts
	digests   map[string]string
	lists     int
}

func (f *finetuningQueryFacts) ReadAdmissionFacts(ctx context.Context, tenant, id, digest string) (biz.TenantAdmissionFacts, error) {
	if err := ctx.Err(); err != nil {
		return biz.TenantAdmissionFacts{}, err
	}
	facts, ok := f.byRelease[id]
	if !ok || tenant != f.tenant || f.digests[id] != digest {
		return biz.TenantAdmissionFacts{}, biz.ErrAdmissionEnvironmentNotReady
	}
	return facts, nil
}
func (f *finetuningQueryFacts) ListReleaseSelections(ctx context.Context, tenant string) ([]biz.ManagedReleaseSelection, error) {
	f.lists++
	if tenant != f.tenant {
		return nil, biz.ErrAdmissionEnvironmentNotReady
	}
	out := []biz.ManagedReleaseSelection{}
	for id, digest := range f.digests {
		out = append(out, biz.ManagedReleaseSelection{ReleaseID: id, ReleaseDigest: digest})
	}
	return out, nil
}

func TestFinetuningPresetQueryBindsExactHistoricalReleaseAndProjectsActual20Or10(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	older := conformance.FinetuningReleaseV1()
	current := conformance.FinetuningReleaseV1()
	current.ReleaseID = uuid.NewString()
	b := []cpup01.Parameter{{Name: "learning_rate", Type: "DECIMAL", Value: "0.0002"}, {Name: "max_steps", Type: "INTEGER", Value: "10"}}
	current.Program.DefaultParameters = b
	current.Program.AllowedParameterSets = [][]cpup01.Parameter{b}
	for index := range current.Program.ParameterRules {
		for _, parameter := range b {
			if current.Program.ParameterRules[index].Name == parameter.Name {
				current.Program.ParameterRules[index].AllowedValues = []cpup01.Parameter{parameter}
			}
		}
	}
	inputScope := cpup01.StorageScope{StorageConnectionID: "secret-input-connection", Bucket: "hidden-input-bucket", ApprovedPrefix: "hidden-input-prefix", CredentialReference: "hidden-input-credential"}
	publication := cpup01.StorageScope{StorageConnectionID: "secret-artifact-connection", Bucket: "hidden-artifact-bucket", ApprovedPrefix: "hidden-artifact-prefix", CredentialReference: "hidden-artifact-credential"}
	model := conformance.FinetuningSnapshotV1().Finetuning.BaseModel
	facts := &finetuningQueryFacts{tenant: "11111111-2222-4333-8444-555555555555", byRelease: map[string]biz.TenantAdmissionFacts{}, digests: map[string]string{}}
	for _, release := range []cpup01.ReleaseDocument{older, current} {
		raw, err := release.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		digest := hex.EncodeToString(hash[:])
		if _, err := catalogue.ImportRelease(ctx, directory, raw, digest); err != nil {
			t.Fatal(err)
		}
		facts.digests[release.ReleaseID] = digest
		facts.byRelease[release.ReleaseID] = biz.TenantAdmissionFacts{TenantID: facts.tenant, Runtime: release.Runtime, Workspace: release.Workspace, InputScope: inputScope, PublicationScope: publication, InputFilePath: "/workspace/inputs/data/finetune_data.json", OutputDirectoryPath: "/workspace/training"}
	}
	query := NewQuery(nil, nil).WithMaterials(biz.NewManagedMaterials(catalogue.NewReader(directory), facts, nil, nil))
	scope := GovernanceDelivery{TenantID: facts.tenant, Actor: "governance:user:query-fixture", RequestID: uuid.NewString()}
	authorized := WithVerifiedGovernanceQuery(ctx, scope, modeldevv1.ModelDevQueryService_ListPresets_FullMethodName)
	for _, test := range []struct {
		release    cpup01.ReleaseDocument
		steps      int64
		generation uint64
	}{{current, 10, 8}, {older, 20, 7}} {
		selection := &modeldevv1.PresetReleaseSelection{PresetId: test.release.PresetID, ReleaseId: test.release.ReleaseID, ReleaseDigest: facts.digests[test.release.ReleaseID], BindingGeneration: test.generation}
		response, err := query.ListPresets(authorized, &modeldevv1.ListPresetsRequest{ReleaseSelections: []*modeldevv1.PresetReleaseSelection{selection}})
		if err != nil || len(response.Presets) != 1 {
			t.Fatalf("exact selected rules missing: %v", err)
		}
		preset := response.Presets[0]
		if preset.ActiveReleaseId != selection.ReleaseId || preset.ReleaseDigest != selection.ReleaseDigest || preset.BindingGeneration != test.generation || preset.ParameterContractVersion != cpup01.FinetuningParameterContractVersion {
			t.Fatal("query combined different Release/binding facts")
		}
		found := false
		for _, rule := range preset.ParameterRules {
			if rule.Name == "max_steps" {
				found = true
				if rule.DefaultValue.GetIntegerValue() != test.steps || len(rule.AllowedValues) != 1 || rule.AllowedValues[0].GetIntegerValue() != test.steps || rule.Editable || rule.Required {
					t.Fatal("query substituted legacy integer or wrong existence/editability rule")
				}
			}
		}
		if !found {
			t.Fatal("max_steps projection missing")
		}
		if len(preset.AllowedParameterSets) != 1 || preset.ManagedSettings.GetPerDeviceTrainBatchSize() != 1 {
			t.Fatal("safe fixed settings/complete combinations missing")
		}
		raw, err := protojson.Marshal(preset)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{inputScope.StorageConnectionID, inputScope.Bucket, inputScope.ApprovedPrefix, inputScope.CredentialReference, publication.StorageConnectionID, publication.Bucket, publication.ApprovedPrefix, publication.CredentialReference, model.Object.Key, "/opt/finetuning/finetune_entry.py", "/workspace"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("public preset leaked fixed scope or execution path %q", secret)
			}
		}
	}
	if facts.lists != 0 {
		t.Fatal("selected query read arbitrary catalogue aggregation instead of exact selection")
	}
	if _, err := query.ListPresets(ctx, &modeldevv1.ListPresetsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("query bypassed current authorization boundary")
	}
	missing := &modeldevv1.PresetReleaseSelection{PresetId: current.PresetID, ReleaseId: current.ReleaseID, ReleaseDigest: facts.digests[older.ReleaseID], BindingGeneration: 8}
	if response, err := query.ListPresets(authorized, &modeldevv1.ListPresetsRequest{ReleaseSelections: []*modeldevv1.PresetReleaseSelection{missing}}); err == nil || response != nil {
		t.Fatal("digest mismatch fell back to a historical Release")
	}
	unselected, err := query.ListPresets(authorized, &modeldevv1.ListPresetsRequest{})
	if err != nil || len(unselected.Presets) != 0 {
		t.Fatal("FINETUNING advertised without Governance exact selection")
	}
}

func TestFinetuningSelectedQueryRejectsConflictingAndIncompleteBinding(t *testing.T) {
	release := conformance.FinetuningReleaseV1()
	query := NewQuery(nil, nil)
	scope := GovernanceDelivery{TenantID: "11111111-2222-4333-8444-555555555555", RequestID: uuid.NewString()}
	for _, selections := range [][]*modeldevv1.PresetReleaseSelection{{nil}, {{PresetId: release.PresetID, ReleaseId: release.ReleaseID, ReleaseDigest: "invalid", BindingGeneration: 1}}, {{PresetId: release.PresetID, ReleaseId: release.ReleaseID, ReleaseDigest: conformance.ReleaseSHA256V1, BindingGeneration: 0}}} {
		if response, err := query.selectedPresets(context.Background(), scope, selections, 20, ""); status.Code(err) != codes.InvalidArgument || response != nil {
			t.Fatal("invalid explicit selector was accepted")
		}
	}
}

func TestFinetuningHistoricalExecutionProjectionKeepsOriginalPresenceAndSnapshot(t *testing.T) {
	snapshot := conformance.FinetuningSnapshotV1()
	intent := conformance.FinetuningIntentV1()
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	accepted := snapshot.DeadlineAt.Add(-time.Hour)
	execution := biz.Execution{Admission: biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:history-fixture", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: accepted}, States: biz.ExecutionStates{Compute: biz.ComputeStateAccepted, Delivery: biz.DeliveryStatePending, Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateOpen}}
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		t.Fatal(err)
	}
	record := biz.QueryRecord{Execution: execution, Runtime: biz.ExecutionRuntime{FinetuningSteps: []biz.FinetuningStepStart{{Binding: biz.FinetuningStepBinding{Role: "export"}}, {Binding: biz.FinetuningStepBinding{Role: "verify"}}}}}
	view, err := executionView(record)
	if err != nil {
		t.Fatal(err)
	}
	if view.Kind != trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING || view.ReleaseId != snapshot.Release.ReleaseID || view.ReleaseDigest != snapshot.Release.ReleaseDigest || view.Identity.ExecutionSpecHash != specHash || view.ResolvedConfigurationSha256 != snapshot.Finetuning.ResolvedConfigurationSHA256 || view.CurrentStep != modeldevv1.PipelineStep_PIPELINE_STEP_VERIFY {
		t.Fatal("historical projection lost original configuration or current compute role")
	}
	if view.ExplicitFinetuning == nil || view.ExplicitFinetuning.LearningRate != nil || view.ExplicitFinetuning.MaxSteps != nil {
		t.Fatal("omitted original fields became explicit defaults")
	}
	for _, parameter := range view.ResolvedParameters {
		if parameter.Name == "max_steps" && parameter.GetIntegerValue() != 20 {
			t.Fatal("historical resolved integer drifted")
		}
	}
	newRelease := conformance.FinetuningReleaseV1()
	newRelease.ReleaseID = uuid.NewString()
	b := []cpup01.Parameter{{Name: "learning_rate", Type: "DECIMAL", Value: "0.0002"}, {Name: "max_steps", Type: "INTEGER", Value: "10"}}
	newRelease.Program.DefaultParameters = b
	newRelease.Program.AllowedParameterSets = [][]cpup01.Parameter{b}
	for index := range newRelease.Program.ParameterRules {
		for _, parameter := range b {
			if newRelease.Program.ParameterRules[index].Name == parameter.Name {
				newRelease.Program.ParameterRules[index].AllowedValues = []cpup01.Parameter{parameter}
			}
		}
	}
	newRaw, err := newRelease.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(newRaw)
	current, err := selectedPresetView(newRelease, &modeldevv1.PresetReleaseSelection{PresetId: newRelease.PresetID, ReleaseId: newRelease.ReleaseID, ReleaseDigest: hex.EncodeToString(digest[:]), BindingGeneration: 8})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range current.ParameterRules {
		if rule.Name == "max_steps" && rule.DefaultValue.GetIntegerValue() != 10 {
			t.Fatal("new Release did not independently project its real default")
		}
	}
	again, err := executionView(record)
	if err != nil || again.ResolvedConfigurationSha256 != view.ResolvedConfigurationSha256 || again.ReleaseId != view.ReleaseId || again.ReleaseDigest != view.ReleaseDigest || !again.DeadlineAt.AsTime().Equal(snapshot.DeadlineAt) {
		t.Fatal("new Release/default changed historical execution query")
	}
	for _, parameter := range again.ResolvedParameters {
		if parameter.Name == "max_steps" && parameter.GetIntegerValue() != 20 {
			t.Fatal("current preset default rewrote historical resolved parameter")
		}
	}
	rate, steps := "0.0001", int64(20)
	record.Execution.Intent.Finetuning = &cpup01.FinetuningParameters{LearningRate: &rate, MaxSteps: &steps}
	before, hash, err := cpup01.CanonicalIntent(record.Execution.Intent)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := executionView(record)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.ExplicitFinetuning == nil || explicit.ExplicitFinetuning.LearningRate == nil || explicit.ExplicitFinetuning.MaxSteps == nil {
		t.Fatal("explicit original field presence lost")
	}
	*explicit.ExplicitFinetuning.LearningRate = "0.0002"
	*explicit.ExplicitFinetuning.MaxSteps = 10
	after, afterHash, err := cpup01.CanonicalIntent(record.Execution.Intent)
	if err != nil || hash != afterHash || !bytes.Equal(before, after) {
		t.Fatal("response pointer mutation changed original historical Intent")
	}
	recorded := accepted.Add(time.Minute)
	record.Runtime.FinetuningRegistration = &biz.FinetuningRegistration{FailureCode: "MODEL_DEPENDENCY_UNAVAILABLE", RecordedAt: recorded}
	registered, err := executionView(record)
	if err != nil || registered.CurrentStep != modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER || registered.Failure == nil || registered.Failure.Reason != modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE || !registered.ObservedAt.AsTime().Equal(recorded) {
		t.Fatal("registration dependency state was hidden or promoted to success")
	}
	encoded, err := protojson.Marshal(registered)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{snapshot.Finetuning.BaseModel.Object.Key, snapshot.Finetuning.BaseModel.Scope.CredentialReference, snapshot.PublicationScope.ApprovedPrefix, snapshot.PublicationScope.CredentialReference, "/opt/finetuning/finetune_entry.py", "/workspace"} {
		if bytes.Contains(encoded, []byte(hidden)) {
			t.Fatalf("execution view leaked hidden storage/command field %q", hidden)
		}
	}
}
