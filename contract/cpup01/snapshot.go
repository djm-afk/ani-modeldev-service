package cpup01

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const SnapshotSchemaVersion = "ani.modeldev.execution-spec.v3"

// Snapshot freezes trusted candidate identity and the entire executable plan.
// Every resource UID created after admission belongs to runtime facts only.
type Snapshot struct {
	SchemaVersion    string                     `json:"schema_version"`
	Kind             string                     `json:"kind"`
	DeliveryMode     string                     `json:"delivery_mode"`
	IdentityRef      IdentityRef                `json:"identity_ref"`
	Implementation   ImplementationSnapshot     `json:"implementation"`
	ComputeOrigin    ComputeOrigin              `json:"compute_origin"`
	Timing           Timing                     `json:"timing"`
	RuntimePlan      RuntimePlan                `json:"runtime_plan"`
	Input            InputRef                   `json:"input"`
	Program          ProgramRef                 `json:"program"`
	Resources        CPUResources               `json:"resources"`
	Environment      EnvironmentBindingSnapshot `json:"environment"`
	Workspace        WorkspaceContract          `json:"workspace"`
	PublicationScope StorageScope               `json:"publication_scope"`
	OutputContract   OutputContract             `json:"output_contract"`
	Limits           ContentLimits              `json:"limits"`
	DeadlineAt       time.Time                  `json:"deadline_at"`
	Finetuning       *FinetuningSnapshot        `json:"finetuning,omitempty"`
	GPU              *GPUPlan                   `json:"gpu,omitempty"`
}

type RuntimeRef struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	APIGroup      string   `json:"api_group"`
	ContentSHA256 string   `json:"content_sha256"`
	TargetJobs    []string `json:"target_jobs"`
}

type FixedObjectRef struct {
	StorageConnectionID string  `json:"storage_connection_id"`
	Bucket              string  `json:"bucket"`
	Key                 string  `json:"key"`
	VersionID           *string `json:"version_id,omitempty"`
	ImmutableCopy       *bool   `json:"immutable_copy,omitempty"`
	SizeBytes           int64   `json:"size_bytes,string"`
	SHA256              string  `json:"sha256"`
}

type InputRef struct {
	InputVersionID  string               `json:"input_version_id"`
	Object          FixedObjectRef       `json:"object"`
	Format          string               `json:"format"`
	SchemaVersion   string               `json:"schema_version"`
	RowCount        int64                `json:"row_count,string"`
	FeatureCount    uint32               `json:"feature_count"`
	PreparedDataset PreparedDatasetFacts `json:"prepared_dataset"`
}

type ProgramRef struct {
	ImageID            string      `json:"image_id"`
	ImageVersion       int64       `json:"image_version,string"`
	ImageScope         ImageScope  `json:"image_scope"`
	ImageDigest        string      `json:"image_digest"`
	Command            []string    `json:"command"`
	ResolvedArgs       []string    `json:"resolved_args"`
	ResolvedParameters []Parameter `json:"resolved_parameters"`
}

type CPUResources struct {
	Nodes              uint32 `json:"nodes"`
	ProcessesPerNode   uint32 `json:"processes_per_node"`
	RequestMillicpu    int64  `json:"request_millicpu,string"`
	LimitMillicpu      int64  `json:"limit_millicpu,string"`
	RequestMemoryBytes int64  `json:"request_memory_bytes,string"`
	LimitMemoryBytes   int64  `json:"limit_memory_bytes,string"`
}

type RuntimeIdentityRefs struct {
	ModeldevControlIdentityRef string `json:"modeldev_control_identity_ref"`
	KFPStepServiceAccount      string `json:"kfp_step_service_account"`
	TrainerServiceAccount      string `json:"trainer_service_account"`
	VerifierServiceAccount     string `json:"verifier_service_account"`
	TenantProxyIdentity        string `json:"tenant_proxy_identity"`
}

type EnvironmentBindingSnapshot struct {
	BindingID        string              `json:"binding_id"`
	BindingDigest    string              `json:"binding_digest"`
	ClusterID        string              `json:"cluster_id"`
	NamespaceName    string              `json:"namespace_name"`
	NamespaceUID     string              `json:"namespace_uid"`
	KFPConnectionRef string              `json:"kfp_connection_ref"`
	ExperimentID     string              `json:"experiment_id"`
	Identities       RuntimeIdentityRefs `json:"identities"`
}

type WorkspaceContract struct {
	Mode               string `json:"mode"`
	StorageClass       string `json:"storage_class"`
	CapacityBytes      int64  `json:"capacity_bytes,string"`
	InputSubpath       string `json:"input_subpath"`
	TrainingSubpath    string `json:"training_subpath"`
	ReportsSubpath     string `json:"reports_subpath"`
	PublicationSubpath string `json:"publication_subpath"`
	WorkSubpath        string `json:"work_subpath"`
}

type StorageScope struct {
	StorageConnectionID string `json:"storage_connection_id"`
	Bucket              string `json:"bucket"`
	ApprovedPrefix      string `json:"approved_prefix"`
	CredentialReference string `json:"credential_reference"`
}

// Validate checks the same finite storage authority used by frozen objects.
// It grants no credentials and performs no storage lookup.
func (scope StorageScope) Validate() error {
	if !validSnapshotReference(scope.StorageConnectionID) || !validSnapshotDNSName(scope.Bucket) || !validSnapshotPath(scope.ApprovedPrefix) || !validSnapshotReference(scope.CredentialReference) {
		return fmt.Errorf("%w: storage scope", ErrInvalidArgument)
	}
	return nil
}

type RequiredOutput struct {
	Role         string `json:"role"`
	RelativePath string `json:"relative_path"`
	InTar        bool   `json:"in_tar"`
}
type OutputContract struct {
	SchemaVersion   string           `json:"schema_version"`
	OutputKind      string           `json:"output_kind"`
	DeliveryMode    string           `json:"delivery_mode"`
	RequiredFiles   []RequiredOutput `json:"required_files"`
	CreateTarBundle bool             `json:"create_tar_bundle"`
}

// Canonical validates and returns current-schema canonical UTF-8 bytes. It never resolves
// current defaults, changes caller-owned values, or performs network I/O.
func (snapshot Snapshot) Canonical() ([]byte, error) { return snapshot.canonical(true) }

func (snapshot Snapshot) CanonicalPrepared() ([]byte, error) { return snapshot.canonical(false) }

func (snapshot Snapshot) canonical(requireRuntime bool) ([]byte, error) {
	if err := snapshot.validate(requireRuntime); err != nil {
		return nil, err
	}
	snapshot.IdentityRef.TenantID = strings.ToLower(snapshot.IdentityRef.TenantID)
	snapshot.IdentityRef.OperationID = strings.ToLower(snapshot.IdentityRef.OperationID)
	snapshot.IdentityRef.ExecutionID = strings.ToLower(snapshot.IdentityRef.ExecutionID)
	snapshot.Timing.AcceptedAt = snapshot.Timing.AcceptedAt.UTC()
	snapshot.Timing.ExecutionDeadlineAt = snapshot.Timing.ExecutionDeadlineAt.UTC()
	snapshot.Implementation.PipelineID = strings.ToLower(snapshot.Implementation.PipelineID)
	snapshot.Implementation.PipelineVersionID = strings.ToLower(snapshot.Implementation.PipelineVersionID)
	snapshot.Input.InputVersionID = strings.ToLower(snapshot.Input.InputVersionID)
	snapshot.Environment.BindingID = strings.ToLower(snapshot.Environment.BindingID)
	snapshot.Environment.NamespaceUID = strings.ToLower(snapshot.Environment.NamespaceUID)
	snapshot.Environment.ExperimentID = strings.ToLower(snapshot.Environment.ExperimentID)
	snapshot.RuntimePlan.TargetJobs = append([]string{}, snapshot.RuntimePlan.TargetJobs...)
	sort.Strings(snapshot.RuntimePlan.TargetJobs)
	snapshot.Program.ResolvedArgs = append([]string{}, snapshot.Program.ResolvedArgs...)
	snapshot.Program.ResolvedParameters = canonicalParameters(snapshot.Program.ResolvedParameters)
	snapshot.OutputContract.RequiredFiles = append([]RequiredOutput{}, snapshot.OutputContract.RequiredFiles...)
	sort.Slice(snapshot.OutputContract.RequiredFiles, func(i, j int) bool {
		return snapshot.OutputContract.RequiredFiles[i].RelativePath < snapshot.OutputContract.RequiredFiles[j].RelativePath
	})
	snapshot.DeadlineAt = snapshot.DeadlineAt.UTC()
	if snapshot.Finetuning != nil {
		fine := canonicalFinetuningSnapshot(*snapshot.Finetuning)
		snapshot.Finetuning = &fine
	}
	canonical, err := encodeCanonicalJSON(snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: snapshot encoding", ErrInvalidArgument)
	}
	return canonical, nil
}

// Digest returns lowercase SHA256 of Canonical, with no trailing newline.
func (snapshot Snapshot) Digest() (string, error) {
	canonical, err := snapshot.Canonical()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func DecodeCanonicalSnapshot(raw []byte) (Snapshot, error) {
	var s Snapshot
	if len(raw) == 0 || len(raw) > MaxManifestBytes {
		return s, ErrInvalidArgument
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil {
		return Snapshot{}, ErrInvalidArgument
	}
	canonical, e := s.Canonical()
	if e != nil || !bytes.Equal(canonical, raw) {
		return Snapshot{}, ErrInvalidArgument
	}
	return s, nil
}

// Validate checks the local contract only. A valid shape is not evidence that
// an environment, immutable object, image, or Runtime actually exists.
func (snapshot Snapshot) Validate() error { return snapshot.validate(true) }

// ValidatePrepared checks every fixed fact before a runtime plan is compiled.
// It cannot accept executions; only Validate accepts a final executable plan.
func (snapshot Snapshot) ValidatePrepared() error { return snapshot.validate(false) }

func (snapshot Snapshot) validate(requireRuntime bool) error {
	invalid := func(field string) error { return fmt.Errorf("%w: snapshot %s", ErrInvalidArgument, field) }
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return invalid("schema_version")
	}
	if snapshot.Limits.Validate() != nil || snapshot.Limits.Check(ContentDataset, snapshot.Input.Object.SizeBytes) != nil {
		return invalid("frozen content limit")
	}
	if (snapshot.Kind != "GENERAL_TRAINING" && snapshot.Kind != "FINETUNING") || snapshot.DeliveryMode != "SAVE_ARTIFACTS" {
		return invalid("capability")
	}
	if snapshot.Kind == "GENERAL_TRAINING" && snapshot.Finetuning != nil {
		return invalid("capability")
	}
	if snapshot.ComputeOrigin.DeviceMode == "cpu" && snapshot.GPU != nil {
		return invalid("CPU accelerator facts")
	}
	if snapshot.ComputeOrigin.DeviceMode == "gpu" && requireRuntime && (snapshot.GPU == nil || snapshot.GPU.ValidateFor(snapshot.ComputeOrigin) != nil) {
		return invalid("GPU execution facts")
	}
	if snapshot.ComputeOrigin.DeviceMode == "gpu" {
		request, err := GPURequestFor(snapshot.ComputeOrigin)
		if err != nil || request.ClusterID != snapshot.Environment.ClusterID {
			return invalid("GPU cluster")
		}
	}
	identity := snapshot.IdentityRef
	if !validUUID(identity.TenantID) || !validUUID(identity.OperationID) || !validUUID(identity.ExecutionID) || !ValidAuditActor(identity.Actor) || !snapshotSHA256Pattern.MatchString(identity.IntentHash) {
		return invalid("identity_ref")
	}
	definition, ok := Implementations()[snapshot.Implementation.ImplementationID]
	if !ok || definition.Kind != snapshot.Kind || definition.Version != snapshot.Implementation.Version || definition.ParameterContractVersion != snapshot.Implementation.ParameterContractVersion {
		return invalid("implementation")
	}
	if snapshot.ComputeOrigin.Validate() != nil || snapshot.Resources.RequestMillicpu != snapshot.ComputeOrigin.CPUMillis || snapshot.Resources.LimitMillicpu != snapshot.ComputeOrigin.CPUMillis || snapshot.Resources.RequestMemoryBytes != snapshot.ComputeOrigin.MemoryMiB*1024*1024 || snapshot.Resources.LimitMemoryBytes != snapshot.Resources.RequestMemoryBytes {
		return invalid("compute origin resources")
	}
	if !validRegisteredImage(snapshot.Program.ImageID, snapshot.Program.ImageVersion, snapshot.Program.ImageScope) {
		return invalid("registered image")
	}
	for _, value := range []string{snapshot.Implementation.PipelineID, snapshot.Implementation.PipelineVersionID, snapshot.Input.InputVersionID, snapshot.Environment.BindingID, snapshot.Environment.NamespaceUID, snapshot.Environment.ExperimentID} {
		if !validUUID(value) {
			return invalid("fixed identity")
		}
	}
	for _, value := range []string{snapshot.Implementation.PipelineIRSHA256, snapshot.Input.Object.SHA256, snapshot.Environment.BindingDigest} {
		if !snapshotSHA256Pattern.MatchString(value) {
			return invalid("content sha256")
		}
	}
	if snapshot.Implementation.ImplementationID != "builtin.mlp" && snapshot.Implementation.ImplementationID != "builtin.sft_lora" {
		return invalid("implementation")
	}
	if snapshot.Implementation.Version == "" || snapshot.Timing.Validate() != nil || !snapshot.DeadlineAt.Equal(snapshot.Timing.ExecutionDeadlineAt) {
		return invalid("frozen plan")
	}
	if requireRuntime && (snapshot.RuntimePlan.Validate() != nil || snapshot.RuntimePlan.Namespace != snapshot.Environment.NamespaceName || snapshot.RuntimePlan.Name != RuntimeName(snapshot.Environment.NamespaceUID, snapshot.IdentityRef.ExecutionID)) {
		return invalid("runtime plan")
	}
	input := snapshot.Input
	if input.PreparedDataset.ValidateForSource(input.Format, input.Object.SizeBytes, input.Object.SHA256, input.RowCount) != nil {
		return invalid("prepared dataset facts")
	}
	if snapshot.Kind == "GENERAL_TRAINING" && (input.Format != "CSV" || input.SchemaVersion != "ani.cpu.csv.v1" || input.RowCount <= 0 || input.FeatureCount != 16) {
		return invalid("fixed CSV contract")
	}
	object := input.Object
	if !validSnapshotReference(object.StorageConnectionID) || !validSnapshotDNSName(object.Bucket) || !validSnapshotPath(object.Key) || object.SizeBytes <= 0 {
		return invalid("fixed input object")
	}
	if (object.VersionID == nil) == (object.ImmutableCopy == nil) {
		return invalid("input immutability")
	}
	if object.VersionID != nil && (!validSnapshotText(*object.VersionID) || strings.EqualFold(*object.VersionID, "null")) {
		return invalid("input version")
	}
	if object.ImmutableCopy != nil && !*object.ImmutableCopy {
		return invalid("input immutable copy")
	}
	program := snapshot.Program
	if !validSnapshotImage(program.ImageDigest) {
		return invalid("image digest")
	}
	if len(program.Command) == 0 {
		return invalid("command")
	}
	for _, value := range program.Command {
		if !validSnapshotText(value) {
			return invalid("command")
		}
	}
	for _, value := range program.ResolvedArgs {
		if !validSnapshotText(value) {
			return invalid("resolved args")
		}
	}
	if ValidateResolvedParameters(snapshot.Implementation.ImplementationID, program.ResolvedParameters) != nil {
		return invalid("resolved parameters")
	}
	if snapshot.Kind == "FINETUNING" {
		if field := invalidFinetuningSnapshot(snapshot); field != "" {
			return invalid(field)
		}
	}
	if field := invalidCPUResources(snapshot.Resources); field != "" {
		return invalid(field)
	}
	environment := snapshot.Environment
	if !validSnapshotReference(environment.ClusterID) || !validSnapshotDNSLabel(environment.NamespaceName) || !validSnapshotReference(environment.KFPConnectionRef) {
		return invalid("environment references")
	}
	identities := environment.Identities
	if !validSnapshotReference(identities.ModeldevControlIdentityRef) || !validSnapshotReference(identities.TenantProxyIdentity) {
		return invalid("control identity references")
	}
	accounts := []string{identities.KFPStepServiceAccount, identities.TrainerServiceAccount, identities.VerifierServiceAccount}
	seenAccounts := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		if !validSnapshotDNSName(account) || seenAccounts[account] {
			return invalid("distinct workload service accounts")
		}
		seenAccounts[account] = true
	}
	if field := invalidWorkspaceContract(snapshot.Workspace); field != "" {
		return invalid(field)
	}
	scope := snapshot.PublicationScope
	if scope.Validate() != nil {
		return invalid("publication scope")
	}
	if field := invalidOutputContractForKind(snapshot.Kind, snapshot.OutputContract); field != "" {
		return invalid(field)
	}
	deadline := snapshot.DeadlineAt.UTC()
	if deadline.IsZero() || deadline.Year() < 1 || deadline.Year() > 9999 {
		return invalid("deadline_at")
	}
	return nil
}

var snapshotSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var snapshotDNSLabelPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var snapshotReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
var snapshotImageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*$`)

func validSnapshotText(value string) bool {
	if len(value) == 0 || len(value) > 4096 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	switch strings.ToUpper(value) {
	case "UNKNOWN", "NOT_READY", "UNSET", "UNSPECIFIED":
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validSnapshotReference(value string) bool {
	return validSnapshotText(value) && len(value) <= 512 && snapshotReferencePattern.MatchString(value) && !strings.Contains(value, "://")
}

func validSnapshotDNSLabel(value string) bool {
	return validSnapshotText(value) && len(value) <= 63 && snapshotDNSLabelPattern.MatchString(value)
}

func validSnapshotDNSName(value string) bool {
	if len(value) > 253 || !validSnapshotText(value) {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !validSnapshotDNSLabel(label) {
			return false
		}
	}
	return true
}

func validSnapshotPath(value string) bool {
	return len(value) <= MaxRelativePathBytes && validSnapshotText(value) && value != "." && value != ".." && !path.IsAbs(value) && path.Clean(value) == value && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, `\*?[]`)
}

func validSnapshotImage(value string) bool {
	parts := strings.Split(value, "@sha256:")
	if len(parts) != 2 || !snapshotSHA256Pattern.MatchString(parts[1]) {
		return false
	}
	name := parts[0]
	return validSnapshotText(name) && snapshotImageNamePattern.MatchString(name) && strings.Contains(name, "/") && !strings.HasPrefix(name, "/") && !strings.HasSuffix(name, "/") && !strings.Contains(name, "//")
}
