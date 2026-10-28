package lifecycle

import (
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestSandboxTerminationCloseEvidenceRemainsOwnerOnlyAndCannotBecomeSuccess(t *testing.T) {
	now := time.Now().UTC()
	authority := biz.RunAuthorityCandidate{RunID: "run-id", WorkflowUID: "workflow-uid", NamespaceName: "tenant-ns"}
	for _, attack := range []string{"valid", "ordinary-managed", "natural-success", "mixed-exit", "mixed-initialization", "training-kind", "foreign-owner", "creation-open", "missing-sandbox", "condition-generation", "status-generation", "future-condition", "extra-condition", "declared-mismatch", "missing-marker", "fake-log", "wrong-role"} {
		t.Run(attack, func(t *testing.T) {
			proof := &biz.PodSandboxTermination{PodResourceVersion: "11957475", NodeName: "node2", Phase: "Failed", Reason: "Evicted", RestartPolicy: "Never", StepRole: "workspace-name", PodGeneration: 1, StatusObservedGeneration: 1,
				Conditions:             []biz.SandboxTerminationCondition{{Type: "Initialized", Status: "True", ObservedGeneration: 1, TransitionAt: now.Add(-5 * time.Second)}, {Type: "PodReadyToStartContainers", Status: "False", ObservedGeneration: 1, TransitionAt: now.Add(-time.Second)}, {Type: "Ready", Status: "False", ObservedGeneration: 1, TransitionAt: now.Add(-8 * time.Second)}, {Type: "ContainersReady", Status: "False", ObservedGeneration: 1, TransitionAt: now.Add(-8 * time.Second)}},
				DeclaredInitContainers: []string{"init", "kfp-launcher"}, DeclaredContainers: []string{"main", "wait"},
				InitContainerExits: []biz.InitializationContainerExit{{Name: "init", ContainerID: "containerd://init", ImageID: "init-image", StartedAt: now.Add(-10 * time.Second), FinishedAt: now.Add(-9 * time.Second)}, {Name: "kfp-launcher", ContainerID: "containerd://launcher", ImageID: "launcher-image", StartedAt: now.Add(-7 * time.Second), FinishedAt: now.Add(-6 * time.Second)}},
				MissingContainers:  []biz.SandboxMissingContainer{{Name: "main", Current: biz.SandboxMissingMarker{Reason: "ContainerStatusUnknown", Message: biz.SandboxMissingMessage, ReportedExitCode: 137}}, {Name: "wait", RestartCount: 1, Current: biz.SandboxMissingMarker{Reason: "ContainerStatusUnknown", Message: biz.SandboxMissingMessage, ReportedExitCode: 137}, Previous: &biz.SandboxMissingMarker{Reason: "ContainerStatusUnknown", Message: biz.SandboxPreviousMissingMessage, ReportedExitCode: 137}}}}
			evidence := biz.ManagedCloseEvidence{RunID: authority.RunID, WorkflowUID: authority.WorkflowUID, ObservedAt: now, OwnerTermination: &biz.ManagedOwnerTermination{RunState: "FAILED", RunFinishedAt: now.Add(-time.Second), WorkflowPhase: "Failed", WorkflowFinishedAt: now.Add(-time.Second), WorkflowResourceVersion: "123"}, Resources: []biz.RuntimeResource{{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: "evicted-control-pod", UID: "evicted-control-pod-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, CreationDisabled: true, SandboxTermination: proof}}}
			reason := "USER_STOP"
			switch attack {
			case "ordinary-managed":
				evidence.OwnerTermination = nil
				zero := int32(0)
				for _, name := range []string{"prepare", "collect", "publish"} {
					evidence.Resources = append(evidence.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: authority.NamespaceName, Name: name, UID: name + "-uid", OwnerUID: authority.WorkflowUID, APIObjectPresent: true, Terminal: true, ExitCode: &zero})
				}
			case "natural-success":
				reason = "NATURAL_TERMINAL"
			case "mixed-exit":
				zero := int32(0)
				evidence.Resources[0].ExitCode = &zero
			case "mixed-initialization":
				evidence.Resources[0].InitializationAbort = &biz.PodInitializationAbort{}
			case "training-kind":
				evidence.Resources[0].Kind = "TrainJob"
			case "foreign-owner":
				evidence.Resources[0].OwnerUID = "another-workflow"
			case "creation-open":
				evidence.Resources[0].CreationDisabled = false
			case "missing-sandbox":
				proof.Conditions = proof.Conditions[2:]
			case "condition-generation":
				proof.Conditions[1].ObservedGeneration = 2
			case "status-generation":
				proof.StatusObservedGeneration = 2
			case "future-condition":
				proof.Conditions[1].TransitionAt = now.Add(time.Second)
			case "extra-condition":
				proof.Conditions = append(proof.Conditions, biz.SandboxTerminationCondition{Type: "unknown-condition", ObservedGeneration: 1, TransitionAt: now})
			case "declared-mismatch":
				proof.DeclaredContainers = []string{"main"}
			case "missing-marker":
				proof.MissingContainers[0].Current.Message = "unproved missing state"
			case "fake-log":
				evidence.Resources[0].TrainingLog = &biz.RecordedTrainingLog{}
			case "wrong-role":
				proof.StepRole = "train-wait"
			}
			if got := validCloseEvidence(authority, reason, evidence); got != (attack == "valid") {
				t.Fatalf("%s close acceptance=%v", attack, got)
			}
		})
	}
}
