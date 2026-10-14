package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const SnapshotSchemaVersion = "ani.modeldev.execution-spec.v1"

// Snapshot is the complete immutable admission configuration. It deliberately
// excludes operation/execution/tenant/actor: those belong to the authenticated
// command envelope. It also excludes all resources created after admission.
type Snapshot struct {
	SchemaVersion    string                     `json:"schema_version"`
	Kind             string                     `json:"kind"`
	DeliveryMode     string                     `json:"delivery_mode"`
	Release          ReleaseSnapshot            `json:"release"`
	Input            InputRef                   `json:"input"`
	Program          ProgramRef                 `json:"program"`
	Resources        CPUResources               `json:"resources"`
	Environment      EnvironmentBindingSnapshot `json:"environment"`
	Workspace        WorkspaceContract          `json:"workspace"`
	PublicationScope StorageScope               `json:"publication_scope"`
	OutputContract   OutputContract             `json:"output_contract"`
	DeadlineAt       time.Time                  `json:"deadline_at"`
	Finetuning       *FinetuningSnapshot        `json:"finetuning,omitempty"`
}

type ReleaseSnapshot struct {
	ReleaseID                 string     `json:"release_id"`
	ReleaseDigest             string     `json:"release_digest"`
	PresetID                  string     `json:"preset_id"`
	AcceptedBindingGeneration uint64     `json:"accepted_binding_generation,string"`
	PipelineID                string     `json:"pipeline_id"`
	PipelineVersionID         string     `json:"pipeline_version_id"`
	PipelineIRSHA256          string     `json:"pipeline_ir_sha256"`
	Runtime                   RuntimeRef `json:"runtime"`
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
	InputVersionID string         `json:"input_version_id"`
	Object         FixedObjectRef `json:"object"`
	Format         string         `json:"format"`
	SchemaVersion  string         `json:"schema_version"`
	RowCount       uint32         `json:"row_count"`
	FeatureCount   uint32         `json:"feature_count"`
}

type ProgramRef struct {
	ImageVersionID     string      `json:"image_version_id"`
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
}

type StorageScope struct {
	StorageConnectionID string `json:"storage_connection_id"`
	Bucket              string `json:"bucket"`
	ApprovedPrefix      string `json:"approved_prefix"`
	CredentialReference string `json:"credential_reference"`
}

type RequiredOutput struct {
	Role         string `json:"role"`
	RelativePath string `json:"relative_path"`
	MaxSizeBytes int64  `json:"max_size_bytes,string"`
	InTar        *bool  `json:"in_tar,omitempty"`
}

type OutputContract struct {
	SchemaVersion   string           `json:"schema_version"`
	OutputKind      string           `json:"output_kind"`
	DeliveryMode    string           `json:"delivery_mode"`
	RequiredFiles   []RequiredOutput `json:"required_files"`
	MaxFileCount    uint32           `json:"max_file_count"`
	MaxTotalBytes   int64            `json:"max_total_bytes,string"`
	CreateTarBundle bool             `json:"create_tar_bundle"`
	MaxTarBytes     *int64           `json:"max_tar_bytes,string,omitempty"`
}

// Canonical validates and returns v1 canonical UTF-8 bytes. It never resolves
// current defaults, changes caller-owned values, or performs network I/O.
func (snapshot Snapshot) Canonical() ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	snapshot.Release.ReleaseID = strings.ToLower(snapshot.Release.ReleaseID)
	snapshot.Release.PresetID = strings.ToLower(snapshot.Release.PresetID)
	snapshot.Release.PipelineID = strings.ToLower(snapshot.Release.PipelineID)
	snapshot.Release.PipelineVersionID = strings.ToLower(snapshot.Release.PipelineVersionID)
	snapshot.Input.InputVersionID = strings.ToLower(snapshot.Input.InputVersionID)
	snapshot.Program.ImageVersionID = strings.ToLower(snapshot.Program.ImageVersionID)
	snapshot.Environment.BindingID = strings.ToLower(snapshot.Environment.BindingID)
	snapshot.Environment.NamespaceUID = strings.ToLower(snapshot.Environment.NamespaceUID)
	snapshot.Environment.ExperimentID = strings.ToLower(snapshot.Environment.ExperimentID)
	snapshot.Release.Runtime.TargetJobs = append([]string{}, snapshot.Release.Runtime.TargetJobs...)
	sort.Strings(snapshot.Release.Runtime.TargetJobs)
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

// Validate checks the local contract only. A valid shape is not evidence that
// an environment, immutable object, image, or Runtime actually exists.
func (snapshot Snapshot) Validate() error {
	invalid := func(field string) error { return fmt.Errorf("%w: snapshot %s", ErrInvalidArgument, field) }
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return invalid("schema_version")
	}
	if (snapshot.Kind != "GENERAL_TRAINING" && snapshot.Kind != "FINETUNING") || snapshot.DeliveryMode != "SAVE_ARTIFACTS" {
		return invalid("capability")
	}
	if snapshot.Kind == "GENERAL_TRAINING" && snapshot.Finetuning != nil {
		return invalid("capability")
	}
	for _, value := range []string{snapshot.Release.ReleaseID, snapshot.Release.PresetID, snapshot.Release.PipelineID, snapshot.Release.PipelineVersionID, snapshot.Input.InputVersionID, snapshot.Program.ImageVersionID, snapshot.Environment.BindingID, snapshot.Environment.NamespaceUID, snapshot.Environment.ExperimentID} {
		if !validUUID(value) {
			return invalid("fixed identity")
		}
	}
	for _, value := range []string{snapshot.Release.ReleaseDigest, snapshot.Release.PipelineIRSHA256, snapshot.Release.Runtime.ContentSHA256, snapshot.Input.Object.SHA256, snapshot.Environment.BindingDigest} {
		if !snapshotSHA256Pattern.MatchString(value) {
			return invalid("content sha256")
		}
	}
	if snapshot.Release.AcceptedBindingGeneration == 0 {
		return invalid("binding generation")
	}
	if field := invalidRuntimeRef(snapshot.Release.Runtime); field != "" {
		return invalid(field)
	}
	input := snapshot.Input
	if snapshot.Kind == "GENERAL_TRAINING" && (input.Format != "CSV" || input.SchemaVersion != "ani.cpu.csv.v1" || input.RowCount != 1024 || input.FeatureCount != 16) {
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
	if snapshot.Kind == "GENERAL_TRAINING" && len(program.ResolvedParameters) != 3 {
		return invalid("complete resolved parameters")
	}
	// Reuse the accepted parameter grammar with this snapshot's real IDs.
	// This is only a local validation value, never a submitted user intent.
	parameterCheck := Intent{Name: "snapshot-validation", Kind: snapshot.Kind, PresetID: snapshot.Release.PresetID, DatasetVersionID: snapshot.Input.InputVersionID, ImageVersionID: &program.ImageVersionID, GeneralParameters: &program.ResolvedParameters}
	if snapshot.Kind == "GENERAL_TRAINING" {
		if err := validateIntent(parameterCheck); err != nil {
			return invalid("resolved parameters")
		}
	} else if field := invalidFinetuningSnapshot(snapshot); field != "" {
		return invalid(field)
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
	if !validSnapshotReference(scope.StorageConnectionID) || !validSnapshotDNSName(scope.Bucket) || !validSnapshotPath(scope.ApprovedPrefix) || !validSnapshotReference(scope.CredentialReference) {
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
	return validSnapshotText(value) && value != "." && value != ".." && !path.IsAbs(value) && path.Clean(value) == value && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, `\*?[]`)
}

func validSnapshotImage(value string) bool {
	parts := strings.Split(value, "@sha256:")
	if len(parts) != 2 || !snapshotSHA256Pattern.MatchString(parts[1]) {
		return false
	}
	name := parts[0]
	return validSnapshotText(name) && snapshotImageNamePattern.MatchString(name) && strings.Contains(name, "/") && !strings.HasPrefix(name, "/") && !strings.HasSuffix(name, "/") && !strings.Contains(name, "//")
}
