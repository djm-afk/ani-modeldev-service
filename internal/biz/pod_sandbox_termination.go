package biz

import "time"

// PodSandboxTermination is restricted to the pre-workspace name/close helpers.
// Kubelet v1.35.8 emits their precise missing-container marker only at the end
// of SyncTerminatedPod. Neither a terminal phase nor SandboxReady=False alone
// proves this fact. Training writers must still retain actual exits and logs.
type PodSandboxTermination struct {
	PodResourceVersion, NodeName, Phase, Reason, RestartPolicy, StepRole string
	PodGeneration, StatusObservedGeneration                              int64
	Conditions                                                           []SandboxTerminationCondition
	DeclaredInitContainers, DeclaredContainers                           []string
	InitContainerExits                                                   []InitializationContainerExit
	MissingContainers                                                    []SandboxMissingContainer
}

type SandboxTerminationCondition struct {
	Type, Status       string
	ObservedGeneration int64
	TransitionAt       time.Time
}

// ReportedExitCode is the kubelet's missing-state sentinel, not a process exit.
type SandboxMissingMarker struct {
	Reason, Message, ContainerID string
	ReportedExitCode             int32
	StartedAt, FinishedAt        *time.Time
}

type SandboxMissingContainer struct {
	Name, ContainerID, ImageID string
	RestartCount               int64
	Started, Ready             bool
	Current                    SandboxMissingMarker
	Previous                   *SandboxMissingMarker `json:",omitempty"`
}

const SandboxMissingMessage = "The container could not be located when the pod was terminated"
const SandboxPreviousMissingMessage = "The container could not be located when the pod was deleted.  The container used to be Running"

func (proof *PodSandboxTermination) SandboxUnreadyAt() time.Time {
	if proof != nil {
		for _, condition := range proof.Conditions {
			if condition.Type == "PodReadyToStartContainers" {
				return condition.TransitionAt
			}
		}
	}
	return time.Time{}
}

func validSandboxMarker(marker SandboxMissingMarker, message string) bool {
	return marker.Reason == "ContainerStatusUnknown" && marker.Message == message && marker.ReportedExitCode == 137 && marker.ContainerID == "" && marker.StartedAt == nil && marker.FinishedAt == nil
}

func (proof *PodSandboxTermination) ValidAt(observedAt time.Time) bool {
	if proof == nil || proof.PodResourceVersion == "" || proof.NodeName == "" || proof.Phase != "Failed" || proof.Reason != "Evicted" || proof.RestartPolicy != "Never" || proof.PodGeneration <= 0 || proof.StatusObservedGeneration != proof.PodGeneration || observedAt.IsZero() || (proof.StepRole != "workspace-name" && proof.StepRole != "finalize-close") {
		return false
	}
	expected := map[string]string{"Initialized": "True", "PodReadyToStartContainers": "False", "Ready": "False", "ContainersReady": "False"}
	var initializedAt time.Time
	for _, condition := range proof.Conditions {
		expectedStatus, found := expected[condition.Type]
		if !found || expectedStatus != condition.Status || condition.ObservedGeneration != proof.PodGeneration || condition.TransitionAt.IsZero() || condition.TransitionAt.After(observedAt) {
			return false
		}
		delete(expected, condition.Type)
		if condition.Type == "Initialized" {
			initializedAt = condition.TransitionAt
		}
	}
	unready := proof.SandboxUnreadyAt()
	if len(expected) != 0 || initializedAt.After(unready) || len(proof.DeclaredInitContainers) == 0 || len(proof.InitContainerExits) != len(proof.DeclaredInitContainers) || len(proof.DeclaredContainers) != 2 || len(proof.MissingContainers) != 2 {
		return false
	}
	seen := make(map[string]bool)
	var previous time.Time
	for i, exit := range proof.InitContainerExits {
		if exit.Name == "" || seen[exit.Name] || proof.DeclaredInitContainers[i] != exit.Name || exit.ContainerID == "" || exit.ImageID == "" || exit.ExitCode != 0 || exit.StartedAt.IsZero() || exit.FinishedAt.IsZero() || exit.FinishedAt.Before(exit.StartedAt) || exit.StartedAt.Before(previous) || exit.FinishedAt.After(initializedAt) {
			return false
		}
		seen[exit.Name], previous = true, exit.FinishedAt
	}
	regular := map[string]bool{}
	for i, container := range proof.MissingContainers {
		if container.Name == "" || seen[container.Name] || proof.DeclaredContainers[i] != container.Name || container.ContainerID != "" || container.ImageID != "" || container.Started || container.Ready || !validSandboxMarker(container.Current, SandboxMissingMessage) {
			return false
		}
		switch container.Name {
		case "main":
			if container.RestartCount != 0 || container.Previous != nil {
				return false
			}
		case "wait":
			if container.Previous == nil {
				if container.RestartCount != 0 {
					return false
				}
			} else if container.RestartCount != 1 || !validSandboxMarker(*container.Previous, SandboxPreviousMissingMessage) {
				return false
			}
		default:
			return false
		}
		seen[container.Name], regular[container.Name] = true, true
	}
	return regular["main"] && regular["wait"]
}
