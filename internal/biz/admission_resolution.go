package biz

import (
	"context"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

var (
	ErrImplementationUnavailable    = errors.New("IMPLEMENTATION_UNAVAILABLE")
	ErrAdmissionInputNotReady       = errors.New("INPUT_NOT_READY")
	ErrAdmissionEnvironmentNotReady = errors.New("ENVIRONMENT_NOT_READY")
	ErrAdmissionPlanMismatch        = errors.New("ADMISSION_PLAN_MISMATCH")
	ErrUnsupportedDevice            = errors.New("UNSUPPORTED_DEVICE")
)

type PrepareAdmissionInput struct {
	IdentityRef   cpup01.IdentityRef
	Intent        cpup01.Intent
	ComputeOrigin cpup01.ComputeOrigin
	AcceptedAt    time.Time
}

type TenantAdmissionFacts struct {
	TenantID            string
	Environment         cpup01.EnvironmentBindingSnapshot
	PublicationScope    cpup01.StorageScope
	InputScope          cpup01.StorageScope
	Workspace           cpup01.WorkspaceContract
	InputFilePath       string
	OutputDirectoryPath string
	// TargetPlatform is the resolved OS/architecture of the training Pod, taken
	// from installation configuration. It is never inferred from device names.
	TargetPlatform cpup01.TrainingPlatform
	Timing         cpup01.TimingConfiguration
}

type AdmissionResolution struct {
	Snapshot cpup01.Snapshot
	SpecHash string
}
type AdmissionMaterialReader interface {
	ReadImplementationMaterial(context.Context, string, string) (cpup01.ImplementationMaterial, error)
}
type AdmissionInputReader interface {
	Get(context.Context, string, string) (InputVersion, error)
}
type AdmissionFactsReader interface {
	ReadAdmissionFacts(context.Context, string, string) (TenantAdmissionFacts, error)
}

// TrainingImageResolution is the Resource answer for one catalogue selection.
// It carries the authoritative registry authority, so no consumer re-derives
// the host from the reference.
type TrainingImageResolution struct {
	ImageID           string
	Scope             string
	Reference         string
	Digest            string
	RegistryAuthority string
	Platform          cpup01.TrainingPlatform
}

// TrainingImageResolver is the narrow read-only catalogue port. Prepare uses it
// and nothing else: it never writes a Secret and never touches Kubernetes.
type TrainingImageResolver interface {
	ResolveTrainingImage(ctx context.Context, tenantID, imageID, scope string, target cpup01.TrainingPlatform) (TrainingImageResolution, error)
}

// TrainingImageEnsurer is the narrow write port used only after durable
// acceptance, at the runtime creation boundary. Resource remains the single
// Secret writer; ModelDev receives only the non-sensitive reference.
type TrainingImageEnsurer interface {
	EnsurePullSecret(ctx context.Context, tenantID, clusterID, namespace, namespaceUID, registry string) (string, error)
}

type AdmissionResolver struct {
	materials AdmissionMaterialReader
	inputs    AdmissionInputReader
	facts     AdmissionFactsReader
	models    FinetuningModelFactsReader
	images    TrainingImageResolver
}

func NewAdmissionResolver(materials AdmissionMaterialReader, inputs AdmissionInputReader, facts AdmissionFactsReader, images TrainingImageResolver) *AdmissionResolver {
	return &AdmissionResolver{materials: materials, inputs: inputs, facts: facts, images: images}
}

func (r *AdmissionResolver) Prepare(ctx context.Context, request PrepareAdmissionInput) (cpup01.PreparedCandidate, error) {
	if err := ctx.Err(); err != nil {
		return cpup01.PreparedCandidate{}, err
	}
	intent, err := canonicalResolutionIntent(request.Intent)
	if err != nil {
		return cpup01.PreparedCandidate{}, err
	}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	identity := request.IdentityRef
	if err != nil || identity.IntentHash != intentHash || !validAdmissionID(identity.TenantID) || !validAdmissionID(identity.OperationID) || !validAdmissionID(identity.ExecutionID) || !cpup01.ValidAuditActor(identity.Actor) || request.ComputeOrigin.Validate() != nil || request.ComputeOrigin.ComputePresetID != intent.ComputePresetID {
		return cpup01.PreparedCandidate{}, ErrInvalidAdmission
	}
	if r == nil || r.materials == nil || r.inputs == nil || r.facts == nil {
		return cpup01.PreparedCandidate{}, ErrAdmissionEnvironmentNotReady
	}
	material, err := r.materials.ReadImplementationMaterial(ctx, intent.ImplementationID, request.ComputeOrigin.DeviceMode)
	if err != nil {
		return cpup01.PreparedCandidate{}, err
	}
	if _, err := material.Canonical(); err != nil || material.Implementation.ImplementationID != intent.ImplementationID {
		return cpup01.PreparedCandidate{}, ErrImplementationUnavailable
	}
	facts, err := r.facts.ReadAdmissionFacts(ctx, identity.TenantID, intent.ImplementationID)
	if err != nil {
		return cpup01.PreparedCandidate{}, err
	}
	if !strings.EqualFold(facts.TenantID, identity.TenantID) || facts.Workspace != material.Workspace {
		return cpup01.PreparedCandidate{}, ErrAdmissionEnvironmentNotReady
	}
	timing, err := facts.Timing.Resolve(request.AcceptedAt, intent.TimeoutSeconds)
	if err != nil {
		return cpup01.PreparedCandidate{}, ErrInvalidAdmission
	}
	version, err := r.inputs.Get(ctx, identity.TenantID, intent.DatasetVersionID)
	if err != nil {
		return cpup01.PreparedCandidate{}, err
	}
	if version.Import.TenantID != identity.TenantID || version.Import.InputVersionID != intent.DatasetVersionID {
		return cpup01.PreparedCandidate{}, ErrInvalidAdmission
	}
	input, err := verifiedAdmissionInput(version, intent.Kind)
	if err != nil {
		return cpup01.PreparedCandidate{}, err
	}
	if !compatibleAdmissionFacts(facts, input.Object, intent.Kind) {
		return cpup01.PreparedCandidate{}, ErrAdmissionEnvironmentNotReady
	}
	parameters, err := intent.ResolveParameters()
	if err != nil {
		return cpup01.PreparedCandidate{}, ErrInvalidAdmission
	}
	var fine *cpup01.FinetuningSnapshot
	if intent.Kind == "FINETUNING" {
		fine, err = r.resolveFinetuningModelFacts(ctx, identity.TenantID, intent, material, parameters, input.Object, request.ComputeOrigin.DeviceMode)
		if err != nil {
			return cpup01.PreparedCandidate{}, err
		}
	}
	var args []string
	if fine != nil {
		args, err = resolveFinetuningArguments(material.Program.ArgsTemplate, input.Object, fine.BaseModel)
	} else {
		args, err = resolveAdmissionArguments(material.Training.ArgsTemplate, parameters, facts, input.Object)
	}
	if err != nil {
		return cpup01.PreparedCandidate{}, err
	}
	memory := request.ComputeOrigin.MemoryMiB * 1024 * 1024
	snapshot := cpup01.Snapshot{SchemaVersion: cpup01.SchemaVersionFor(intent.Kind), Kind: intent.Kind, DeliveryMode: "SAVE_ARTIFACTS", IdentityRef: identity, Implementation: material.Implementation, ComputeOrigin: request.ComputeOrigin, Timing: timing, Input: input, Resources: cpup01.CPUResources{Nodes: 1, ProcessesPerNode: 1, RequestMillicpu: request.ComputeOrigin.CPUMillis, LimitMillicpu: request.ComputeOrigin.CPUMillis, RequestMemoryBytes: memory, LimitMemoryBytes: memory}, Environment: facts.Environment, Workspace: facts.Workspace, PublicationScope: facts.PublicationScope, OutputContract: material.OutputContract, DeadlineAt: timing.ExecutionDeadlineAt, Finetuning: fine}
	if fine != nil {
		program := cpup01.ProgramRef{ImageVersionID: material.Program.ImageVersionID, ImageDigest: material.Program.ImageDigest, Command: append([]string{}, material.Program.Command...), ResolvedArgs: args, ResolvedParameters: parameters}
		snapshot.Program = &program
	} else {
		program, resolveErr := r.resolveTrainingProgram(ctx, identity.TenantID, intent, material, facts, args, parameters)
		if resolveErr != nil {
			return cpup01.PreparedCandidate{}, resolveErr
		}
		snapshot.TrainingProgram = program
	}
	gpuRequest, err := cpup01.GPURequestFor(request.ComputeOrigin)
	if err != nil {
		return cpup01.PreparedCandidate{}, ErrUnsupportedDevice
	}
	candidate := cpup01.PreparedCandidate{SchemaVersion: cpup01.CandidateSchemaVersion, Intent: intent, Snapshot: snapshot, GPURequest: gpuRequest}
	if err := candidate.Validate(); err != nil {
		return cpup01.PreparedCandidate{}, ErrInvalidAdmission
	}
	if err := ctx.Err(); err != nil {
		return cpup01.PreparedCandidate{}, err
	}
	// Detach every slice and optional object reference from provider-owned facts.
	raw, err := candidate.Canonical()
	if err != nil {
		return cpup01.PreparedCandidate{}, ErrInvalidAdmission
	}
	hash, err := candidate.Digest()
	if err != nil {
		return cpup01.PreparedCandidate{}, ErrInvalidAdmission
	}
	normalized, err := cpup01.DecodePreparedCandidate(raw, hash)
	if err != nil {
		return cpup01.PreparedCandidate{}, ErrInvalidAdmission
	}
	return normalized, nil
}

func (r *AdmissionResolver) Finalize(ctx context.Context, candidate cpup01.PreparedCandidate, gpu *cpup01.GPUPlan) (AdmissionResolution, error) {
	if r == nil {
		return AdmissionResolution{}, ErrAdmissionEnvironmentNotReady
	}
	if err := ctx.Err(); err != nil {
		return AdmissionResolution{}, err
	}
	if candidate.Validate() != nil {
		return AdmissionResolution{}, ErrAdmissionPlanMismatch
	}
	// Finalize consumes the original candidate and already-validated formal ACC facts only.
	snapshot := candidate.Snapshot
	if snapshot.ComputeOrigin.DeviceMode == "cpu" {
		if gpu != nil {
			return AdmissionResolution{}, ErrAdmissionPlanMismatch
		}
	} else {
		if gpu == nil || gpu.ValidateFor(snapshot.ComputeOrigin) != nil {
			return AdmissionResolution{}, ErrAdmissionPlanMismatch
		}
		snapshot.GPU = gpu
	}
	plan, err := CompileRuntimePlan(snapshot)
	if err != nil {
		return AdmissionResolution{}, err
	}
	snapshot.RuntimePlan = plan
	hash, err := snapshot.Digest()
	if err != nil {
		return AdmissionResolution{}, ErrAdmissionPlanMismatch
	}
	raw, err := snapshot.Canonical()
	if err != nil {
		return AdmissionResolution{}, ErrAdmissionPlanMismatch
	}
	normalized, err := cpup01.DecodeCanonicalSnapshot(raw)
	if err != nil {
		return AdmissionResolution{}, ErrAdmissionPlanMismatch
	}
	return AdmissionResolution{Snapshot: normalized, SpecHash: hash}, nil
}

func canonicalResolutionIntent(original cpup01.Intent) (cpup01.Intent, error) {
	raw, _, err := cpup01.CanonicalIntent(original)
	if err != nil {
		return cpup01.Intent{}, ErrInvalidAdmission
	}
	intent, err := cpup01.DecodeCanonicalIntent(raw)
	if err != nil {
		return cpup01.Intent{}, ErrInvalidAdmission
	}
	return intent, nil
}
func compatibleAdmissionFacts(f TenantAdmissionFacts, object cpup01.FixedObjectRef, kind string) bool {
	if kind == "GENERAL_TRAINING" && (f.InputFilePath != "/inputs/data.csv" || f.OutputDirectoryPath != "/outputs") {
		return false
	}
	if kind == "FINETUNING" && (f.InputFilePath != "/workspace/inputs/data/finetune_data.json" || f.OutputDirectoryPath != "/workspace/training") {
		return false
	}
	scope := f.InputScope
	return scope.StorageConnectionID == object.StorageConnectionID && scope.Bucket == object.Bucket && ValidStorageKey(scope.ApprovedPrefix) && strings.HasPrefix(object.Key, scope.ApprovedPrefix+"/") && validAdmissionContainerPath(f.InputFilePath) && validAdmissionContainerPath(f.OutputDirectoryPath) && f.InputFilePath != f.OutputDirectoryPath && !strings.HasPrefix(f.InputFilePath, f.OutputDirectoryPath+"/") && !strings.HasPrefix(f.OutputDirectoryPath, f.InputFilePath+"/")
}
func validAdmissionContainerPath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value && ValidStorageKey(strings.TrimPrefix(value, "/")) && !strings.ContainsAny(value, "*?[]")
}
// resolveTrainingProgram freezes the catalogue selection and the trusted
// command/args contract. It only reads Resource's resolution: Prepare must stay
// read-only, so no Secret is written and Kubernetes is never touched here. The
// user selection wins; an absent pair falls back to the trusted material
// default, which is resolved by the same Resource call and never by a local
// image version, a bare reference or the first catalogue entry.
func (r *AdmissionResolver) resolveTrainingProgram(ctx context.Context, tenant string, intent cpup01.Intent, material cpup01.ImplementationMaterial, facts TenantAdmissionFacts, args []string, parameters []cpup01.Parameter) (*cpup01.TrainingProgram, error) {
	if material.Training == nil || (intent.TrainingImageID == nil) != (intent.TrainingImageScope == nil) {
		return nil, ErrImplementationUnavailable
	}
	if r.images == nil {
		return nil, ErrImplementationUnavailable
	}
	imageID, scope := material.Training.DefaultImage.ImageID, material.Training.DefaultImage.Scope
	if intent.TrainingImageID != nil {
		imageID, scope = *intent.TrainingImageID, *intent.TrainingImageScope
	}
	resolution, err := r.images.ResolveTrainingImage(ctx, tenant, imageID, scope, facts.TargetPlatform)
	if err != nil {
		return nil, err
	}
	// The answer must describe exactly the accepted selection, at the pinned
	// target platform, anchored on Resource's authoritative registry authority.
	if resolution.ImageID != imageID || resolution.Scope != scope || resolution.Platform != facts.TargetPlatform ||
		resolution.RegistryAuthority == "" || resolution.Digest == "" ||
		!strings.HasPrefix(resolution.Reference, resolution.RegistryAuthority+"/") || !strings.HasSuffix(resolution.Reference, "@"+resolution.Digest) {
		return nil, ErrImplementationUnavailable
	}
	secretRef, err := cpup01.PullSecretName(tenant, facts.Environment.ClusterID, facts.Environment.NamespaceName, resolution.RegistryAuthority)
	if err != nil {
		return nil, ErrAdmissionEnvironmentNotReady
	}
	return &cpup01.TrainingProgram{
		Image: cpup01.TrainingImageSelection{
			ImageID:           resolution.ImageID,
			Scope:             resolution.Scope,
			RegistryAuthority: resolution.RegistryAuthority,
			Digest:            resolution.Digest,
			Reference:         resolution.Reference,
			Platform:          resolution.Platform,
			ProtocolVersion:   cpup01.TrainingImageProtocolVersion,
			PullSecretRef:     secretRef,
		},
		Command:            append([]string{}, material.Training.Command...),
		ResolvedArgs:       append([]string{}, args...),
		ResolvedParameters: parameters,
	}, nil
}

func resolveAdmissionArguments(template []cpup01.MaterialArgument, parameters []cpup01.Parameter, facts TenantAdmissionFacts, object cpup01.FixedObjectRef) ([]string, error) {
	values := map[string]string{"INPUT_PATH": facts.InputFilePath, "OUTPUT_PATH": facts.OutputDirectoryPath, "INPUT_SHA256": object.SHA256, "INPUT_BYTES": strconv.FormatInt(object.SizeBytes, 10)}
	for _, p := range parameters {
		values[strings.ToUpper(p.Name)] = p.Value
	}
	args := make([]string, 0, len(template))
	for _, a := range template {
		if (a.Literal == "") == (a.Source == "") {
			return nil, ErrImplementationUnavailable
		}
		value := a.Literal
		if a.Source != "" {
			value = values[a.Source]
			if value == "" {
				return nil, ErrImplementationUnavailable
			}
		}
		args = append(args, value)
	}
	return args, nil
}
