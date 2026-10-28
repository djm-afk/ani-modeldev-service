// Package conformance contains synthetic current-contract fixtures only.
package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// TrainingImageID is the synthetic catalogue identifier of the training image.
// It is a directory key, not a registry reference.
const TrainingImageID = "img_" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TrainingImageScope is the platform scope of the synthetic selection.
const TrainingImageScope = "platform"

// TrainingReference is the synthetic resolved reference of the selection.
const TrainingReference = "registry.example.test/cpu/mlp@sha256:" + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

// RegistryAuthority is the synthetic authoritative registry host. Consumers use
// it verbatim; it is never re-derived from the resolved reference.
const RegistryAuthority = "registry.example.test"

const IntentRequest = `{"name":"CPU training","kind":"GENERAL_TRAINING","implementation_id":"builtin.mlp","compute_preset_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","dataset_version_id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","parameters":{},"training_image_id":"img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","training_image_scope":"platform"}`

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

// TrainingImageSelectionV1 is the frozen resolution of the synthetic image.
func TrainingImageSelectionV1() cpup01.TrainingImageSelection {
	name, err := cpup01.PullSecretName("77777777-7777-4777-8777-777777777777", "22222222-2222-4222-8222-222222222222", "cpu-contract-fixture", "registry.example.test")
	if err != nil {
		panic(err)
	}
	return cpup01.TrainingImageSelection{
		ImageID:           TrainingImageID,
		Scope:             TrainingImageScope,
		RegistryAuthority: RegistryAuthority,
		Digest:            "sha256:" + strings.Repeat("e", 64),
		Reference:         TrainingReference,
		Platform:          cpup01.TrainingPlatform{OS: "linux", Architecture: "amd64"},
		ProtocolVersion:   cpup01.TrainingImageProtocolVersion,
		PullSecretRef:     name,
	}
}

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
	expected := []byte(`{"template":{"spec":{"replicatedJobs":[]}}}`)
	return cpup01.Snapshot{
		SchemaVersion: cpup01.TrainingSnapshotSchemaVersion, Kind: "GENERAL_TRAINING", DeliveryMode: "SAVE_ARTIFACTS",
		IdentityRef:    cpup01.IdentityRef{TenantID: "77777777-7777-4777-8777-777777777777", Actor: "governance:user:contract-test", ExecutionID: executionID, OperationID: "88888888-8888-4888-8888-888888888888", IntentHash: hash},
		Implementation: cpup01.ImplementationSnapshot{ImplementationID: "builtin.mlp", Version: "1", ParameterContractVersion: cpup01.MLPParameterContractVersion, PipelineID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", PipelineVersionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", PipelineIRSHA256: strings.Repeat("b", 64)},
		ComputeOrigin:  cpup01.ComputeOrigin{ComputePresetID: i.ComputePresetID, SelectionDigest: sum([]byte("compute-selection-v1\n" + selection)), SelectionJSON: selection, CPUMillis: 1000, MemoryMiB: 1024, DeviceMode: "cpu"}, Timing: timing,
		RuntimePlan:      cpup01.RuntimePlan{GeneratorVersion: "contract-fixture-v1", APIGroup: "trainer.kubeflow.org", Kind: "TrainingRuntime", Namespace: "cpu-contract-fixture", Name: cpup01.RuntimeName(namespaceUID, executionID), ExpectedSpec: expected, ExpectedSpecSHA256: sum(expected), DefaultingRules: "trainer-v2.1-explicit-v1", TargetJobs: []string{"node"}},
		Input:            cpup01.InputRef{InputVersionID: i.DatasetVersionID, Object: cpup01.FixedObjectRef{StorageConnectionID: "input-store-v1", Bucket: "cpu-inputs", Key: "fixed/input/data.csv", VersionID: &version, SizeBytes: 192456, SHA256: strings.Repeat("d", 64)}, Format: "CSV", SchemaVersion: "ani.cpu.csv.v1", RowCount: 1027, FeatureCount: 16},
		TrainingProgram:  &cpup01.TrainingProgram{Image: TrainingImageSelectionV1(), Command: []string{"python", "/opt/mlp/train.py"}, ResolvedArgs: []string{"--data", "/inputs/data.csv", "--output", "/outputs"}, ResolvedParameters: parameters},
		Resources:        cpup01.CPUResources{Nodes: 1, ProcessesPerNode: 1, RequestMillicpu: 1000, LimitMillicpu: 1000, RequestMemoryBytes: 1073741824, LimitMemoryBytes: 1073741824},
		Environment:      cpup01.EnvironmentBindingSnapshot{BindingID: "11111111-1111-4111-8111-111111111111", BindingDigest: strings.Repeat("f", 64), ClusterID: "22222222-2222-4222-8222-222222222222", NamespaceName: "cpu-contract-fixture", NamespaceUID: namespaceUID, KFPConnectionRef: "kfp-managed-v1", ExperimentID: "44444444-4444-4444-8444-444444444444", Identities: cpup01.RuntimeIdentityRefs{ModeldevControlIdentityRef: "modeldev-control-v1", KFPStepServiceAccount: "cpu-managed-step", TrainerServiceAccount: "cpu-training", VerifierServiceAccount: "cpu-verifier", TenantProxyIdentity: "tenant:contract-fixture"}},
		Workspace:        cpup01.WorkspaceContract{Mode: "EXECUTION_PVC", StorageClass: "task-workspace", CapacityBytes: 2147483648, InputSubpath: "inputs", TrainingSubpath: "training", ReportsSubpath: "reports", PublicationSubpath: "publication"},
		PublicationScope: cpup01.StorageScope{StorageConnectionID: "artifact-store-v1", Bucket: "cpu-artifacts", ApprovedPrefix: "tenant-fixed/executions", CredentialReference: "modeldev-publish-v1"},
		OutputContract:   cpup01.OutputContract{SchemaVersion: "ani.cpu.output.v1", OutputKind: "CHECKPOINT", DeliveryMode: "SAVE_ARTIFACTS", RequiredFiles: []cpup01.RequiredOutput{{Role: "SUMMARY", RelativePath: "summary.json", MaxSizeBytes: 4096}, {Role: "CHECKPOINT", RelativePath: "model.pt", MaxSizeBytes: 1048576}, {Role: "MODEL_CONFIG", RelativePath: "model_config.json", MaxSizeBytes: 4096}, {Role: "METRICS", RelativePath: "metrics.jsonl", MaxSizeBytes: 65536}}, MaxFileCount: 16, MaxTotalBytes: 2097152, CreateTarBundle: true}, DeadlineAt: timing.ExecutionDeadlineAt,
	}
}

// MaterialV3 is the general-training material fixture. It carries a catalogue
// default image instead of a pinned digest.
func MaterialV3() cpup01.ImplementationMaterial {
	s := SnapshotV1()
	return cpup01.ImplementationMaterial{SchemaVersion: cpup01.MaterialSchemaVersion, Implementation: s.Implementation, Training: &cpup01.TrainingMaterial{DefaultImage: cpup01.DefaultTrainingImage{ImageID: TrainingImageID, Scope: TrainingImageScope}, Command: s.TrainingProgram.Command, ArgsTemplate: []cpup01.MaterialArgument{{Literal: "--data"}, {Source: "INPUT_PATH"}, {Literal: "--output"}, {Source: "OUTPUT_PATH"}, {Literal: "--learning-rate"}, {Source: "LEARNING_RATE"}, {Literal: "--epochs"}, {Source: "EPOCHS"}, {Literal: "--batch-size"}, {Source: "BATCH_SIZE"}, {Literal: "--seed"}, {Source: "SEED"}}}, Workspace: s.Workspace, OutputContract: s.OutputContract}
}
