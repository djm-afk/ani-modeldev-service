package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

var (
	ErrRuntimeConflict     = errors.New("EXECUTION_RUNTIME_CONFLICT")
	ErrTrainingUncertain   = errors.New("TRAINING_CREATION_UNCERTAIN")
	ErrTrainingRejected    = errors.New("TRAINING_CREATION_REJECTED")
	ErrTrainingUnavailable = errors.New("TRAINING_UNAVAILABLE")
	ErrTrainingNotFound    = errors.New("TRAINING_NOT_FOUND")
	ErrRuntimeNotReady     = errors.New("EXECUTION_RUNTIME_NOT_READY")
)

// WorkspaceBinding is an observed runtime fact, separate from the immutable
// execution snapshot. Its subpaths come exclusively from that snapshot.
type WorkspaceBinding struct {
	Mode                                                                           string
	NamespaceName, NamespaceUID, PVCName, PVCUID                                   string
	InputSubpath, TrainingSubpath, ReportsSubpath, PublicationSubpath, WorkSubpath string
	PreparedManifestSHA256                                                         string
	PreparedManifestBytes                                                          int64
}

type TrainingPlan struct {
	TenantID, OperationID, ExecutionID, SpecHash string
	Name, RequestSHA256                          string
	Snapshot                                     cpup01.Snapshot
	Workspace                                    WorkspaceBinding
	RuntimeBinding                               RuntimeTemplateBinding
	Timing                                       *TrainingBudgetTiming `json:",omitempty"`
}

// TrainingBudgetTiming is fixed by the first database-backed reservation. A
// TrainJob lookup, status replay, or delayed process start cannot renew it.
// Every implementation uses this original shared TRAIN/train-wait window.
type TrainingBudgetTiming struct {
	AdmittedAt time.Time
	DeadlineAt time.Time
}

type TrainingHandle struct {
	NamespaceUID, TrainJobUID, PVCUID string
}

// TrainingCreationRejection records a complete trusted API refusal of the
// original request. It neither clears its consumed permit nor permits another
// POST. ObservedAt is assigned by the repository's database clock.
type TrainingCreationRejection struct {
	RequestSHA256, NamespaceUID string
	StatusCode                  int32
	ObservedAt                  time.Time
}

func (*TrainingCreationRejection) Error() string { return ErrTrainingRejected.Error() }
func (*TrainingCreationRejection) Unwrap() error { return ErrTrainingRejected }

type RuntimeResource struct {
	APIVersion, Kind, Namespace, Name, UID, OwnerUID string
	APIObjectPresent                                 bool
	CreationDisabled                                 bool
	Suspension                                       *TrainingControllerSuspension `json:",omitempty"`
	Terminal                                         bool
	ExitCode                                         *int32
	// A terminal worker's bounded log tail travels with its original exit
	// observation, so releasing its finalizer cannot erase the only copy.
	TrainingLog *RecordedTrainingLog `json:",omitempty"`
	// InitializationAbort is owner-only Pod evidence, never a main-container
	// exit or permission for a managed step, training writer, or publication.
	InitializationAbort *PodInitializationAbort `json:",omitempty"`
	// SandboxTermination is an owner-only kubelet post-termination marker.
	// It preserves missing process status without inventing an actual exit.
	SandboxTermination *PodSandboxTermination `json:",omitempty"`
}

// TrainingControllerSuspension is a controller acknowledgement, distinct from
// merely writing spec.suspend. Zero-worker closure also requires the complete
// original controller graph and a fresh, unpaginated retained-Pod inventory.
type TrainingControllerSuspension struct {
	Generation, ObservedGeneration int64
	AcknowledgedAt                 time.Time
	WorkerExitRetention            bool
}

// PodInitializationAbort retains the kubelet's explicit stopped-sandbox and
// incomplete-init facts. Waiting containers have no invented termination code.
type PodInitializationAbort struct {
	PodResourceVersion, NodeName, Phase, RestartPolicy string
	PodGeneration                                      int64
	DeletedAt, NotInitializedAt, SandboxStoppedAt      time.Time
	DeclaredInitContainers, DeclaredContainers         []string
	InitContainerExits                                 []InitializationContainerExit
	UncreatedInitContainer                             *InitializationContainerNotCreated `json:",omitempty"`
	UnstartedInitContainers, UnstartedContainers       []string
}

// InitializationContainerNotCreated is the kubelet's missing-container marker,
// not a process exit. Sandbox/generation/deletion proof is retained separately
// by PodInitializationAbort; this marker cannot authorize a successful init.
type InitializationContainerNotCreated struct {
	Name, Reason, Message, ContainerID, ImageID string
	ReportedExitCode                            int32
	StartedAt, FinishedAt                       *time.Time
	Started, Ready, LastStateEmpty              bool
	RestartCount                                int64
}

type InitializationContainerExit struct {
	Name, ContainerID, ImageID string
	ExitCode                   int32
	StartedAt, FinishedAt      time.Time
}

// ValidAt validates the persisted abort facts independently of the Kubernetes
// adapter. A completed owner Run/Workflow is additionally required by close.
func (proof *PodInitializationAbort) ValidAt(observedAt time.Time) bool {
	if proof == nil || proof.PodResourceVersion == "" || proof.NodeName == "" || proof.Phase != "Failed" || proof.RestartPolicy != "Never" || proof.PodGeneration <= 0 || observedAt.IsZero() || proof.DeletedAt.IsZero() || proof.NotInitializedAt.IsZero() || proof.SandboxStoppedAt.IsZero() || proof.DeletedAt.After(observedAt) || proof.SandboxStoppedAt.After(observedAt) || proof.NotInitializedAt.After(proof.SandboxStoppedAt) || len(proof.UnstartedInitContainers) == 0 || len(proof.UnstartedContainers) == 0 || len(proof.DeclaredContainers) != len(proof.UnstartedContainers) {
		return false
	}
	seen := make(map[string]bool)
	initPrefix := len(proof.InitContainerExits)
	if marker := proof.UncreatedInitContainer; marker != nil {
		if initPrefix != 0 || marker.Name == "" || marker.Reason != "ContainerStatusUnknown" || marker.Message != "The container could not be located when the pod was terminated" || marker.ReportedExitCode != 137 || marker.ContainerID != "" || marker.ImageID != "" || marker.StartedAt != nil || marker.FinishedAt != nil || marker.Started || marker.Ready || marker.RestartCount != 0 || !marker.LastStateEmpty || len(proof.DeclaredInitContainers) == 0 || proof.DeclaredInitContainers[0] != marker.Name {
			return false
		}
		seen[marker.Name], initPrefix = true, 1
	} else if initPrefix == 0 {
		return false
	}
	if len(proof.DeclaredInitContainers) != initPrefix+len(proof.UnstartedInitContainers) {
		return false
	}
	var previous time.Time
	for i, exit := range proof.InitContainerExits {
		if exit.Name == "" || seen[exit.Name] || proof.DeclaredInitContainers[i] != exit.Name || exit.ContainerID == "" || exit.ImageID == "" || exit.ExitCode != 0 || exit.StartedAt.IsZero() || exit.FinishedAt.IsZero() || exit.FinishedAt.Before(exit.StartedAt) || exit.StartedAt.Before(previous) || exit.FinishedAt.After(proof.SandboxStoppedAt) {
			return false
		}
		seen[exit.Name], previous = true, exit.FinishedAt
	}
	for i, name := range proof.UnstartedInitContainers {
		if name == "" || seen[name] || proof.DeclaredInitContainers[initPrefix+i] != name {
			return false
		}
		seen[name] = true
	}
	main := false
	for i, name := range proof.UnstartedContainers {
		if name == "" || seen[name] || proof.DeclaredContainers[i] != name {
			return false
		}
		seen[name], main = true, main || name == "main"
	}
	return main
}

// WritersAbsent requires verified current controller/child facts and all known
// historical Pods. Neither a missing object nor a Complete condition suffices.
type TrainingRuntimeObservation struct {
	Handle                         TrainingHandle
	Resources                      []RuntimeResource
	Outcome                        string // RUNNING, SUCCEEDED, FAILED, or UNKNOWN
	WritersAbsent                  bool
	SpecificationMismatch          bool   `json:",omitempty"`
	NoWorkerPodListResourceVersion string `json:",omitempty"`
	ObservedAt                     time.Time
}

type TrainingReservation struct {
	State      ExecutionRuntime
	SendPermit bool
}

// RuntimeCreation keeps the original creation fence even when no HTTP receipt
// survives. A dispatch attempt is never erased to manufacture an unsent proof.
type RuntimeCreation struct {
	Name, NamespaceUID, ExpectedSpecSHA256 string
	Attempts                               []RuntimeDispatchAttempt
	Binding                                *RuntimeTemplateBinding `json:",omitempty"`
	ReviewReason, NextAction               string
}

type RuntimeDispatchAttempt struct {
	DispatchedAt  time.Time
	NotSentAt     *time.Time `json:",omitempty"`
	NotSentReason string     `json:",omitempty"`
}

type RuntimeTemplateBinding struct {
	NamespaceUID, Name, UID, ObservedSpecSHA256 string
}

// RuntimeCreationNotSent can be emitted only by the original adapter before
// invoking Kubernetes Create. GET 404 and expired leases never produce it.
type RuntimeCreationNotSent struct {
	NamespaceUID, Name, ExpectedSpecSHA256, Reason string
	AttemptDispatchedAt                            time.Time
}

func (*RuntimeCreationNotSent) Error() string { return "RUNTIME_CREATION_NOT_SENT" }

type PublishedRuntimeFile struct {
	ArtifactID string
	File       cpup01.OutputFile
	Object     cpup01.FixedObjectRef
}

type UploadCompletion struct {
	RunID, WorkflowUID, TaskID, PodUID, ContainerName string
	CompletedAt, ObservedAt                           time.Time
}

type RuntimePublication struct {
	ID, LogicalKey, ReceiptID string
	Files                     []PublishedRuntimeFile
	Manifest                  cpup01.FixedObjectRef
	Bundle                    *cpup01.FixedObjectRef
	Upload                    UploadCompletion
	VerifiedAt                time.Time
}

// ExecutionRuntime contains committed facts; no state value reconstructs an
// outbound creation permit. Normal step advancement belongs to KFP.
type ExecutionRuntime struct {
	GPURelease             *GPUReleaseState        `json:",omitempty"`
	StepTimings            []ManagedStepTiming     `json:",omitempty"`
	RuntimeTemplate        *RuntimeCreation        `json:",omitempty"`
	TrainingWindow         *TrainingBudgetTiming   `json:",omitempty"`
	ComputeCleanup         *ComputeCleanupReceipt  `json:",omitempty"`
	StepExitCleanup        *StepExitCleanupReceipt `json:",omitempty"`
	FinetuningRegistration *FinetuningRegistration `json:",omitempty"`
	Workspace              *WorkspaceBinding
	Training               *TrainingPlan
	TrainingRejection      *TrainingCreationRejection `json:",omitempty"`
	TrainingHandle         *TrainingHandle
	Observation            *TrainingRuntimeObservation
	Publication            *RuntimePublication
	FinetuningSteps        []FinetuningStepStart `json:",omitempty"`
	// CloseOperationID is generated once under the execution lock and retained
	// across normal, stopped and failed closure and all release observations.
	CloseOperationID string `json:"close_operation_id,omitempty"`
	CloseGeneration  uint64
	CloseReason      string
	CloseRequestedAt time.Time
	ClosedAt         *time.Time
	CloseEvidence    *ManagedCloseEvidence
	// CloseAuthority is an owner-only recovery association. It cannot authorize
	// managed steps or recreate a consumed submission permit.
	CloseAuthority    *RunAuthorityCandidate
	CloseReviewReason string
	OwnerRevision     uint64
}

type ExecutionRuntimeRepository interface {
	GetRuntime(context.Context, string, string) (ExecutionRuntime, error)
	RecordPrepared(context.Context, RunAuthorityCandidate, WorkspaceBinding) (ExecutionRuntime, bool, error)
	ReserveRuntime(context.Context, RunAuthorityCandidate) (TrainingReservation, error)
	RecordRuntimeBinding(context.Context, RunAuthorityCandidate, RuntimeTemplateBinding) (ExecutionRuntime, error)
	RecordRuntimeNotSent(context.Context, RunAuthorityCandidate, RuntimeCreationNotSent) (ExecutionRuntime, error)
	RecordRuntimeReview(context.Context, RunAuthorityCandidate) (ExecutionRuntime, error)
	ReserveTraining(context.Context, RunAuthorityCandidate) (TrainingReservation, error)
	RecordTrainingRejection(context.Context, RunAuthorityCandidate, TrainingCreationRejection) (ExecutionRuntime, error)
	RecordTrainingHandle(context.Context, RunAuthorityCandidate, TrainingHandle) (ExecutionRuntime, error)
	RecordTrainingObservation(context.Context, RunAuthorityCandidate, TrainingRuntimeObservation) (ExecutionRuntime, error)
	RecordPublication(context.Context, RunAuthorityCandidate, RuntimePublication) (ExecutionRuntime, bool, error)
	RequestRuntimeClose(context.Context, RunAuthorityCandidate, string) (ExecutionRuntime, bool, error)
	ConfirmRuntimeClosed(context.Context, RunAuthorityCandidate, uint64, TrainingRuntimeObservation, ManagedCloseEvidence) (ExecutionRuntime, error)
	// CloseUnboundRuntime fixes the verified Run association and closes creation
	// in one transaction. It cannot leave an open authority binding visible.
	CloseUnboundRuntime(context.Context, RunAuthorityCandidate, ManagedCloseEvidence) (RunAuthority, ExecutionRuntime, error)
}

type TrainingRuntime interface {
	CreateRuntime(context.Context, TrainingPlan) (RuntimeTemplateBinding, error)
	FindRuntime(context.Context, TrainingPlan) (RuntimeTemplateBinding, error)
	CreateTraining(context.Context, TrainingPlan) (TrainingHandle, error)
	FindTraining(context.Context, TrainingPlan) (TrainingHandle, error)
	ObserveTraining(context.Context, TrainingPlan, TrainingHandle, []RuntimeResource) (TrainingRuntimeObservation, error)
	StopTraining(context.Context, TrainingPlan, TrainingHandle) error
}

// TrainingCloseRecovery resolves an already dispatched original request after
// the close fence. It grants no creation authority and does not depend on the
// current Runtime or PVC still authorizing new computation.
type TrainingCloseRecovery interface {
	FindTrainingForClose(context.Context, TrainingPlan) (TrainingHandle, error)
}

type PreparedWorkspaceVerifier interface {
	VerifyPrepared(context.Context, Execution, ManagedStepAssociation, WorkspaceBinding) error
	VerifyPreparedCompletion(context.Context, Execution, ManagedStepAssociation, WorkspaceBinding) error
}

type PublicationVerifier interface {
	VerifyPublication(context.Context, Execution, ManagedStepAssociation, WorkspaceBinding, RuntimePublication) (RuntimePublication, error)
	VerifyWritersAbsent(context.Context, Execution, ManagedStepAssociation, *WorkspaceBinding) (ManagedCloseEvidence, error)
}

type ManagedCloseEvidence struct {
	RunID, WorkflowUID string
	// NoDispatch is proved under the durable creation fence and identity lock.
	// It is never inferred from an external NotFound response.
	NoDispatch       bool
	Resources        []RuntimeResource
	SkippedTasks     []ManagedSkippedTask
	ObservedAt       time.Time
	OwnerTermination *ManagedOwnerTermination
	// UnpreparedWorkspace records only a PVC observed after the original KFP
	// CreatePVC succeeded and prepare was refused. It grants no prepared data or
	// training authority; owner cleanup must dispose this exact UID after close.
	UnpreparedWorkspace *UnpreparedWorkspace `json:",omitempty"`
}

type UnpreparedWorkspace struct {
	NamespaceName, NamespaceUID, PVCName, PVCUID string
	PVCResourceVersion, SpecSHA256               string
	CreateTaskID, CreatePodUID, RequestSHA256    string
	CreatedAt                                    time.Time
}

// ValidUnpreparedWorkspace checks the persisted identity envelope only. The
// infrastructure verifier must independently prove the original CreatePVC
// request, complete owner graph, exact current UID and writer absence.
func ValidUnpreparedWorkspace(execution Execution, owner RunAuthorityCandidate, fact *UnpreparedWorkspace) bool {
	return fact != nil && fact.NamespaceName == owner.NamespaceName && fact.NamespaceUID == owner.NamespaceUID && owner.NamespaceName == execution.Snapshot.Environment.NamespaceName && owner.NamespaceUID == execution.Snapshot.Environment.NamespaceUID && fact.PVCName == ExpectedWorkspacePVCName(execution.ExecutionID) && validAdmissionID(fact.PVCUID) && fact.PVCResourceVersion != "" && closeSpecHashPattern.MatchString(fact.SpecSHA256) && closeSpecHashPattern.MatchString(fact.RequestSHA256) && validAdmissionID(fact.CreateTaskID) && validAdmissionID(fact.CreatePodUID) && !fact.CreatedAt.IsZero()
}

// ResourceVersion is an observation fence, not a new volume identity.
func SameUnpreparedWorkspace(a, b *UnpreparedWorkspace) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, right := *a, *b
	left.PVCResourceVersion, right.PVCResourceVersion = "", ""
	// Native Kubernetes timestamps and JSON facts can retain different local
	// zone representations of the same instant across persistence.
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return false
	}
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	return left == right
}

// OwnerTermination is independent controller evidence for background close.
// A successful terminate request alone does not establish either terminal fact.
type ManagedOwnerTermination struct {
	RunState                string
	RunFinishedAt           time.Time
	WorkflowPhase           string
	WorkflowFinishedAt      time.Time
	WorkflowResourceVersion string
	WorkflowNodesSHA256     string `json:",omitempty"`
}

// StepExitCleanupReceipt is recorded before releasing or deleting an original
// Workflow Pod. Its complete owner evidence includes control/close Pods which
// were still running when a managed close task confirmed data-plane closure.
// Training Pods and their controllers are deliberately outside this receipt.
type StepExitCleanupReceipt struct {
	CloseGeneration  uint64
	Evidence         ManagedCloseEvidence
	StartedAt        time.Time
	RemovedResources []RuntimeResource
	CompletedAt      *time.Time `json:",omitempty"`
}

type ManagedSkippedTask struct {
	TaskName, TaskID string
}

// ValidateWorkspaceBinding checks only the immutable workspace contract. It
// cannot construct or renew the separate first-admission training deadline.
func ValidateWorkspaceBinding(execution Execution, workspace WorkspaceBinding) error {
	if _, _, err := execution.CanonicalPayloads(); err != nil {
		return ErrInvalidAdmission
	}
	want := execution.Snapshot.Workspace
	if workspace.Mode != want.Mode || workspace.NamespaceName != execution.Snapshot.Environment.NamespaceName || workspace.NamespaceUID != execution.Snapshot.Environment.NamespaceUID ||
		workspace.PVCName == "" || workspace.PVCUID == "" || workspace.InputSubpath != want.InputSubpath || workspace.TrainingSubpath != want.TrainingSubpath ||
		workspace.ReportsSubpath != want.ReportsSubpath || workspace.PublicationSubpath != want.PublicationSubpath || workspace.WorkSubpath != want.WorkSubpath || workspace.PreparedManifestBytes <= 0 || !closeSpecHashPattern.MatchString(workspace.PreparedManifestSHA256) {
		return ErrRuntimeConflict
	}
	return nil
}

func FreezeTrainingPlan(execution Execution, workspace WorkspaceBinding, admittedAt ...time.Time) (TrainingPlan, error) {
	if err := ValidateWorkspaceBinding(execution, workspace); err != nil {
		return TrainingPlan{}, err
	}
	plan := TrainingPlan{TenantID: execution.TenantID, OperationID: execution.OperationID, ExecutionID: execution.ExecutionID, SpecHash: execution.SpecHash,
		Name: "md-" + strings.ToLower(execution.ExecutionID), Snapshot: execution.Snapshot, Workspace: workspace}
	if len(admittedAt) != 1 || admittedAt[0].IsZero() || !admittedAt[0].Before(execution.Snapshot.DeadlineAt) {
		return TrainingPlan{}, ErrRuntimeConflict
	}
	start := admittedAt[0].UTC()
	deadline, err := execution.Snapshot.Timing.DeadlineFor(start)
	if err != nil {
		return TrainingPlan{}, ErrRuntimeConflict
	}
	plan.Timing = &TrainingBudgetTiming{AdmittedAt: start, DeadlineAt: deadline}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return TrainingPlan{}, ErrInvalidAdmission
	}
	digest := sha256.Sum256(encoded)
	plan.RequestSHA256 = hex.EncodeToString(digest[:])
	return plan, nil
}

// BindTrainingRuntime adds observed Runtime identity to the internal TrainJob
// request, never to the immutable admission snapshot.
func BindTrainingRuntime(plan TrainingPlan, binding RuntimeTemplateBinding) (TrainingPlan, error) {
	if binding.NamespaceUID != plan.Workspace.NamespaceUID || binding.Name != plan.Snapshot.RuntimePlan.Name || binding.UID == "" || binding.ObservedSpecSHA256 != plan.Snapshot.RuntimePlan.ExpectedSpecSHA256 {
		return TrainingPlan{}, ErrRuntimeConflict
	}
	plan.RuntimeBinding = binding
	plan.RequestSHA256 = ""
	raw, err := json.Marshal(plan)
	if err != nil {
		return TrainingPlan{}, ErrRuntimeConflict
	}
	digest := sha256.Sum256(raw)
	plan.RequestSHA256 = hex.EncodeToString(digest[:])
	return plan, nil
}
