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
	Mode                                                              string
	NamespaceName, NamespaceUID, PVCName, PVCUID                      string
	InputSubpath, TrainingSubpath, ReportsSubpath, PublicationSubpath string
	PreparedManifestSHA256                                            string
	PreparedManifestBytes                                             int64
}

type TrainingPlan struct {
	TenantID, OperationID, ExecutionID, SpecHash string
	Name, RequestSHA256                          string
	Snapshot                                     cpup01.Snapshot
	Workspace                                    WorkspaceBinding
	FinetuningTiming                             *TrainingBudgetTiming `json:",omitempty"`
}

// TrainingBudgetTiming is fixed by the first database-backed reservation. A
// TrainJob lookup, status replay, or delayed process start cannot renew it.
// The optional field preserves the exact historical CPU request hash.
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
	Terminal                                         bool
	ExitCode                                         *int32
	// InitializationAbort is owner-only Pod evidence, never a main-container
	// exit or permission for a managed step, training writer, or publication.
	InitializationAbort *PodInitializationAbort `json:",omitempty"`
}

// PodInitializationAbort retains the kubelet's explicit stopped-sandbox and
// incomplete-init facts. Waiting containers have no invented termination code.
type PodInitializationAbort struct {
	PodResourceVersion, NodeName, Phase, RestartPolicy string
	PodGeneration                                      int64
	DeletedAt, NotInitializedAt, SandboxStoppedAt      time.Time
	DeclaredInitContainers, DeclaredContainers         []string
	InitContainerExits                                 []InitializationContainerExit
	UnstartedInitContainers, UnstartedContainers       []string
}

type InitializationContainerExit struct {
	Name, ContainerID, ImageID string
	ExitCode                   int32
	StartedAt, FinishedAt      time.Time
}

// ValidAt validates the persisted abort facts independently of the Kubernetes
// adapter. A completed owner Run/Workflow is additionally required by close.
func (proof *PodInitializationAbort) ValidAt(observedAt time.Time) bool {
	if proof == nil || proof.PodResourceVersion == "" || proof.NodeName == "" || proof.Phase != "Failed" || proof.RestartPolicy != "Never" || proof.PodGeneration <= 0 || observedAt.IsZero() || proof.DeletedAt.IsZero() || proof.NotInitializedAt.IsZero() || proof.SandboxStoppedAt.IsZero() || proof.DeletedAt.After(observedAt) || proof.SandboxStoppedAt.After(observedAt) || proof.NotInitializedAt.After(proof.SandboxStoppedAt) || len(proof.InitContainerExits) == 0 || len(proof.UnstartedInitContainers) == 0 || len(proof.UnstartedContainers) == 0 || len(proof.DeclaredInitContainers) != len(proof.InitContainerExits)+len(proof.UnstartedInitContainers) || len(proof.DeclaredContainers) != len(proof.UnstartedContainers) {
		return false
	}
	seen := make(map[string]bool)
	var previous time.Time
	for i, exit := range proof.InitContainerExits {
		if exit.Name == "" || seen[exit.Name] || proof.DeclaredInitContainers[i] != exit.Name || exit.ContainerID == "" || exit.ImageID == "" || exit.ExitCode != 0 || exit.StartedAt.IsZero() || exit.FinishedAt.IsZero() || exit.FinishedAt.Before(exit.StartedAt) || exit.StartedAt.Before(previous) || exit.FinishedAt.After(proof.SandboxStoppedAt) {
			return false
		}
		seen[exit.Name], previous = true, exit.FinishedAt
	}
	for i, name := range proof.UnstartedInitContainers {
		if name == "" || seen[name] || proof.DeclaredInitContainers[len(proof.InitContainerExits)+i] != name {
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
	Handle        TrainingHandle
	Resources     []RuntimeResource
	Outcome       string // RUNNING, SUCCEEDED, FAILED, or UNKNOWN
	WritersAbsent bool
	ObservedAt    time.Time
}

type TrainingReservation struct {
	State      ExecutionRuntime
	SendPermit bool
}

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
	StepExitCleanup        *StepExitCleanupReceipt `json:",omitempty"`
	FinetuningRegistration *FinetuningRegistration `json:",omitempty"`
	Workspace              *WorkspaceBinding
	Training               *TrainingPlan
	TrainingRejection      *TrainingCreationRejection `json:",omitempty"`
	TrainingHandle         *TrainingHandle
	Observation            *TrainingRuntimeObservation
	Publication            *RuntimePublication
	FinetuningSteps        []FinetuningStepStart `json:",omitempty"`
	CloseGeneration        uint64
	CloseReason            string
	CloseRequestedAt       time.Time
	ClosedAt               *time.Time
	CloseEvidence          *ManagedCloseEvidence
	// CloseAuthority is an owner-only recovery association. It cannot authorize
	// managed steps or recreate a consumed submission permit.
	CloseAuthority    *RunAuthorityCandidate
	CloseReviewReason string
	OwnerRevision     uint64
}

type ExecutionRuntimeRepository interface {
	GetRuntime(context.Context, string, string) (ExecutionRuntime, error)
	RecordPrepared(context.Context, RunAuthorityCandidate, WorkspaceBinding) (ExecutionRuntime, bool, error)
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
	CreateTraining(context.Context, TrainingPlan) (TrainingHandle, error)
	FindTraining(context.Context, TrainingPlan) (TrainingHandle, error)
	ObserveTraining(context.Context, TrainingPlan, TrainingHandle, []RuntimeResource) (TrainingRuntimeObservation, error)
	StopTraining(context.Context, TrainingPlan, TrainingHandle) error
}

type PreparedWorkspaceVerifier interface {
	VerifyPrepared(context.Context, Execution, ManagedStepAssociation, WorkspaceBinding) error
}

type PublicationVerifier interface {
	VerifyPublication(context.Context, Execution, ManagedStepAssociation, RuntimePublication) (RuntimePublication, error)
	VerifyWritersAbsent(context.Context, Execution, ManagedStepAssociation) (ManagedCloseEvidence, error)
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
		workspace.ReportsSubpath != want.ReportsSubpath || workspace.PublicationSubpath != want.PublicationSubpath || workspace.PreparedManifestBytes <= 0 || !closeSpecHashPattern.MatchString(workspace.PreparedManifestSHA256) {
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
	if execution.Snapshot.Kind == "FINETUNING" {
		fine := execution.Snapshot.Finetuning
		if fine == nil || fine.Budgets.TrainingTimeoutSeconds == 0 || len(admittedAt) != 1 || admittedAt[0].IsZero() || !admittedAt[0].Before(execution.Snapshot.DeadlineAt) {
			return TrainingPlan{}, ErrRuntimeConflict
		}
		start := admittedAt[0].UTC()
		deadline := start.Add(time.Duration(fine.Budgets.TrainingTimeoutSeconds) * time.Second)
		if execution.Snapshot.DeadlineAt.Before(deadline) {
			deadline = execution.Snapshot.DeadlineAt.UTC()
		}
		plan.FinetuningTiming = &TrainingBudgetTiming{AdmittedAt: start, DeadlineAt: deadline}
	} else if len(admittedAt) > 1 {
		return TrainingPlan{}, ErrRuntimeConflict
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return TrainingPlan{}, ErrInvalidAdmission
	}
	digest := sha256.Sum256(encoded)
	plan.RequestSHA256 = hex.EncodeToString(digest[:])
	return plan, nil
}
