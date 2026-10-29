// Package conformance contains synthetic current-contract fixtures only.
package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"strings"
	"time"
)

const IntentRequest = `{"name":"CPU training","kind":"GENERAL_TRAINING","implementation_id":"builtin.mlp","compute_preset_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","dataset_version_id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","parameters":{}}`

func IntentInputV1() []byte { return []byte(IntentRequest) }
func IntentV1() cpup01.Intent {
	i, e := cpup01.ParseIntent(IntentInputV1())
	if e != nil {
		panic(e)
	}
	return i
}
func IntentCanonicalV1() []byte {
	b, _, e := cpup01.CanonicalIntent(IntentV1())
	if e != nil {
		panic(e)
	}
	return b
}
func SnapshotCanonicalV1() []byte {
	b, e := SnapshotV1().Canonical()
	if e != nil {
		panic(e)
	}
	return b
}
func sum(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }

func SnapshotV1() cpup01.Snapshot {
	i := IntentV1()
	_, hash, _ := cpup01.CanonicalIntent(i)
	parameters, _ := i.ResolveParameters()
	version := "input-version-1"
	accepted := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	timing, _ := (cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900}).Resolve(accepted, nil)
	selection := `{"schema_version":1,"preset_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","code":"cpu-small","name":"CPU small","compute_type":"cpu","cpu_millis":1000,"memory_mib":1024,"gpu":null}`
	namespaceUID := "33333333-3333-4333-8333-333333333333"
	executionID := "99999999-9999-4999-8999-999999999999"
	expected := []byte(`{"podGroupPolicy":{"volcano":{}},"template":{"spec":{"replicatedJobs":[]}}}`)
	snapshot := cpup01.Snapshot{
		SchemaVersion: cpup01.SnapshotSchemaVersion, Limits: cpup01.ContentLimits{MaxContentBytes: 17179869184}, Kind: "GENERAL_TRAINING", DeliveryMode: "SAVE_ARTIFACTS",
		IdentityRef:    cpup01.IdentityRef{TenantID: "77777777-7777-4777-8777-777777777777", Actor: "governance:user:contract-test", ExecutionID: executionID, OperationID: "88888888-8888-4888-8888-888888888888", IntentHash: hash},
		Implementation: cpup01.ImplementationSnapshot{ImplementationID: "builtin.mlp", Version: "1", ParameterContractVersion: cpup01.MLPParameterContractVersion, PipelineID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", PipelineVersionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", PipelineIRSHA256: strings.Repeat("b", 64)},
		ComputeOrigin:  cpup01.ComputeOrigin{ComputePresetID: i.ComputePresetID, SelectionDigest: sum([]byte("compute-selection-v1\n" + selection)), SelectionJSON: selection, CPUMillis: 1000, MemoryMiB: 1024, DeviceMode: "cpu"}, Timing: timing,
		RuntimePlan: cpup01.RuntimePlan{GeneratorVersion: "contract-fixture-v1", APIGroup: "trainer.kubeflow.org", Kind: "TrainingRuntime", Namespace: "cpu-contract-fixture", Name: cpup01.RuntimeName(namespaceUID, executionID), ExpectedSpec: expected, ExpectedSpecSHA256: sum(expected), DefaultingRules: "trainer-v2.1-explicit-v1", TargetJobs: []string{"node"}},
		Input:       cpup01.InputRef{InputVersionID: i.DatasetVersionID, Object: cpup01.FixedObjectRef{StorageConnectionID: "input-store-v1", Bucket: "cpu-inputs", Key: "fixed/input/data.csv", VersionID: &version, SizeBytes: 192456, SHA256: strings.Repeat("d", 64)}, Format: "CSV", SchemaVersion: "ani.cpu.csv.v1", RowCount: 1027, FeatureCount: 16, PreparedDataset: cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: 192456, SHA256: strings.Repeat("d", 64), RowCount: 1027}},
		Program:     cpup01.ProgramRef{ImageID: "img_ffffffffffffffffffffffffffffffff", ImageVersion: 1, ImageScope: cpup01.ImageScopePlatform, ImageDigest: "registry.example.test/cpu/mlp@sha256:" + strings.Repeat("e", 64), Command: []string{"python", "/opt/mlp/train.py"}, ResolvedArgs: []string{"--data", "/inputs/data.csv", "--output", "/outputs"}, ResolvedParameters: parameters},
		TrainingImage: &cpup01.TrainingImageFacts{
			RegistryAuthority: "registry.example.test",
			Platform:          cpup01.TrainingPlatform{OS: "linux", Architecture: "amd64"},
			ProtocolVersion:   cpup01.TrainingImageProtocolVersion,
			PullSecretRef:     "ani-pull-" + strings.Repeat("a", 32),
		},
		Resources:        cpup01.CPUResources{Nodes: 1, ProcessesPerNode: 1, RequestMillicpu: 1000, LimitMillicpu: 1000, RequestMemoryBytes: 1073741824, LimitMemoryBytes: 1073741824},
		Environment:      cpup01.EnvironmentBindingSnapshot{BindingID: "11111111-1111-4111-8111-111111111111", BindingDigest: strings.Repeat("f", 64), ClusterID: "22222222-2222-4222-8222-222222222222", NamespaceName: "cpu-contract-fixture", NamespaceUID: namespaceUID, KFPConnectionRef: "kfp-managed-v1", ExperimentID: "44444444-4444-4444-8444-444444444444", Identities: cpup01.RuntimeIdentityRefs{ModeldevControlIdentityRef: "modeldev-control-v1", KFPStepServiceAccount: "cpu-managed-step", TrainerServiceAccount: "cpu-training", VerifierServiceAccount: "cpu-verifier", TenantProxyIdentity: "tenant:contract-fixture"}},
		Workspace:        cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: "task-workspace", CapacityBytes: 2147483648, InputSubpath: "inputs", TrainingSubpath: "training", ReportsSubpath: "reports", PublicationSubpath: "publication", WorkSubpath: "work"},
		PublicationScope: cpup01.StorageScope{StorageConnectionID: "artifact-store-v1", Bucket: "cpu-artifacts", ApprovedPrefix: "tenant-fixed/executions", CredentialReference: "modeldev-publish-v1"},
		OutputContract:   cpup01.OutputContract{SchemaVersion: cpup01.OutputContractSchemaVersion, OutputKind: "CHECKPOINT", DeliveryMode: "SAVE_ARTIFACTS", RequiredFiles: []cpup01.RequiredOutput{{Role: "SUMMARY", RelativePath: "summary.json", InTar: true}, {Role: "CHECKPOINT", RelativePath: "model.pt", InTar: true}, {Role: "MODEL_CONFIG", RelativePath: "model_config.json", InTar: true}, {Role: "METRICS", RelativePath: "metrics.jsonl", InTar: true}}, CreateTarBundle: true}, DeadlineAt: timing.ExecutionDeadlineAt,
	}
	dataset, _ := cpup01.BuildDatasetManifest(snapshot.Input)
	datasetDigest, _ := dataset.Digest()
	snapshot.Program.ResolvedArgs = []string{"--dataset-manifest", "/inputs/dataset-manifest.json", "--expected-dataset-manifest-sha256", datasetDigest, "--work-root", "/workspace/work/train", "--output", "/outputs"}
	return snapshot
}

func MaterialV21() cpup01.ImplementationMaterial {
	s := SnapshotV1()
	return cpup01.ImplementationMaterial{SchemaVersion: cpup01.MaterialSchemaVersion, Implementation: s.Implementation, Training: &cpup01.TrainingMaterial{DefaultImage: cpup01.DefaultTrainingImage{ImageID: s.Program.ImageID, Scope: s.Program.ImageScope}, Command: s.Program.Command, ArgsTemplate: []cpup01.MaterialArgument{{Literal: "--dataset-manifest"}, {Literal: "/inputs/dataset-manifest.json"}, {Literal: "--expected-dataset-manifest-sha256"}, {Source: "DATASET_MANIFEST_SHA256"}, {Literal: "--work-root"}, {Literal: "/workspace/work/train"}, {Literal: "--output"}, {Source: "OUTPUT_PATH"}, {Literal: "--learning-rate"}, {Source: "LEARNING_RATE"}, {Literal: "--epochs"}, {Source: "EPOCHS"}, {Literal: "--batch-size"}, {Source: "BATCH_SIZE"}, {Literal: "--seed"}, {Source: "SEED"}}}, Workspace: s.Workspace, ComponentResources: ComponentResourceFixtures("GENERAL_TRAINING"), OutputContract: s.OutputContract}
}

// ComponentResourceFixtures are synthetic explicit deployment facts, not defaults.
func ComponentResourceFixtures(kind string) map[string]cpup01.ComponentResources {
	result := map[string]cpup01.ComponentResources{}
	for _, role := range cpup01.ComponentRoles(kind) {
		result[role] = cpup01.ComponentResources{CPURequest: "100m", CPULimit: "500m", MemoryRequest: "128Mi", MemoryLimit: "512Mi", EphemeralStorageRequest: "16Mi", EphemeralStorageLimit: "128Mi"}
	}
	return result
}
