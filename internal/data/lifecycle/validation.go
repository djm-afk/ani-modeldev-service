package lifecycle

import (
	"encoding/hex"
	"reflect"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func validWorkspace(execution biz.Execution, workspace biz.WorkspaceBinding) bool {
	frozen := execution.Snapshot.Workspace
	environment := execution.Snapshot.Environment
	return workspace.Mode == frozen.Mode && workspace.NamespaceName == environment.NamespaceName && workspace.NamespaceUID == environment.NamespaceUID && len(validation.IsDNS1123Subdomain(workspace.PVCName)) == 0 && validOpaqueID(workspace.PVCUID) && workspace.InputSubpath == frozen.InputSubpath && workspace.TrainingSubpath == frozen.TrainingSubpath && workspace.ReportsSubpath == frozen.ReportsSubpath && workspace.PublicationSubpath == frozen.PublicationSubpath && validDigest(workspace.PreparedManifestSHA256) && workspace.PreparedManifestBytes > 0
}
func validOpaqueID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < '!' || character > '~' {
			return false
		}
	}
	return true
}
func validDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func mergeObservation(state biz.ExecutionRuntime, observation biz.TrainingRuntimeObservation) (biz.TrainingRuntimeObservation, error) {
	if state.Training == nil || state.TrainingHandle == nil || state.Workspace == nil {
		return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeNotReady
	}
	if observation.Handle != *state.TrainingHandle || observation.ObservedAt.IsZero() || observation.ObservedAt.Year() > 9999 {
		return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
	}
	if observation.Outcome != "RUNNING" && observation.Outcome != "SUCCEEDED" && observation.Outcome != "FAILED" && observation.Outcome != "UNKNOWN" {
		return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
	}
	history := make(map[string]biz.RuntimeResource)
	if state.Observation != nil {
		observation.SpecificationMismatch = observation.SpecificationMismatch || state.Observation.SpecificationMismatch
		if observation.ObservedAt.Before(state.Observation.ObservedAt) {
			return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
		}
		for _, resource := range state.Observation.Resources {
			history[resource.UID] = resource
		}
	}
	seen := make(map[string]bool)
	for _, resource := range observation.Resources {
		if !validOpaqueID(resource.UID) || resource.Kind == "" || resource.APIVersion == "" || len(validation.IsDNS1123Subdomain(resource.Name)) != 0 || resource.Namespace != state.Workspace.NamespaceName || seen[resource.UID] {
			return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
		}
		previous, existed := history[resource.UID]
		if existed {
			if previous.APIVersion != resource.APIVersion || previous.Kind != resource.Kind || previous.Namespace != resource.Namespace || previous.Name != resource.Name || previous.OwnerUID != resource.OwnerUID || previous.Terminal && !resource.Terminal {
				return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
			}
			if previous.ExitCode != nil && (resource.ExitCode == nil || *previous.ExitCode != *resource.ExitCode) {
				return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
			}
			if previous.TrainingLog != nil && !reflect.DeepEqual(previous.TrainingLog, resource.TrainingLog) {
				return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
			}
		}
		// The API no longer returning a UID is not fresh proof of termination.
		if !resource.APIObjectPresent && resource.Terminal && (!existed || !previous.Terminal) {
			return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
		}
		seen[resource.UID] = true
		history[resource.UID] = resource
	}
	merged := observation
	merged.Resources = make([]biz.RuntimeResource, 0, len(history))
	hasTrainJob, hasJobSet, hasPod := false, false, false
	allTerminal := true
	allSuccessful := true
	for _, resource := range history {
		// Initialization abort proves only an owner-controlled KFP Pod. It
		// cannot replace any training controller or writer exit observation.
		if resource.InitializationAbort != nil {
			return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
		}
		switch resource.Kind {
		case "TrainJob":
			if resource.UID != state.TrainingHandle.TrainJobUID || resource.Name != state.Training.Name || resource.OwnerUID != "" {
				return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
			}
			hasTrainJob = true
		case "JobSet":
			if resource.OwnerUID != state.TrainingHandle.TrainJobUID {
				return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
			}
			hasJobSet = true
		case "PodGroup":
			if resource.APIVersion != "scheduling.volcano.sh/v1beta1" || resource.Name != state.Training.Name || resource.OwnerUID != state.TrainingHandle.TrainJobUID || !resource.Terminal || resource.ExitCode != nil {
				return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
			}
		case "Job":
			owner, ok := history[resource.OwnerUID]
			if !ok || owner.Kind != "JobSet" {
				return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
			}
		case "Pod":
			owner, ok := history[resource.OwnerUID]
			if !ok || (owner.Kind != "JobSet" && owner.Kind != "Job") {
				return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
			}
			hasPod = true
			if resource.ExitCode == nil || *resource.ExitCode != 0 {
				allSuccessful = false
			}
		default:
			return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
		}
		if resource.Kind == "Pod" {
			if !resource.Terminal || resource.ExitCode == nil {
				allTerminal = false
			}
		} else if !resource.Terminal && !resource.CreationDisabled {
			allTerminal = false
		}
		merged.Resources = append(merged.Resources, resource)
	}
	sort.Slice(merged.Resources, func(i, j int) bool { return merged.Resources[i].UID < merged.Resources[j].UID })
	if !hasTrainJob {
		return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
	}
	noWorker := !hasPod && validNoWorkerClose(state, observation, history)
	if observation.NoWorkerPodListResourceVersion != "" && (hasPod || !noWorker || !observation.WritersAbsent) {
		return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
	}
	if observation.WritersAbsent && (!allTerminal || !hasJobSet || !hasPod && !noWorker) {
		return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeNotReady
	}
	if observation.Outcome == "SUCCEEDED" && (!observation.WritersAbsent || !allSuccessful || !hasPod) {
		return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeNotReady
	}
	if state.Observation != nil && state.Observation.Outcome == "SUCCEEDED" && observation.Outcome != "SUCCEEDED" {
		return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeConflict
	}
	return merged, nil
}

func validNoWorkerClose(state biz.ExecutionRuntime, observation biz.TrainingRuntimeObservation, resources map[string]biz.RuntimeResource) bool {
	if state.CloseGeneration == 0 || state.CloseRequestedAt.IsZero() || !validOpaqueID(observation.NoWorkerPodListResourceVersion) {
		return false
	}
	counts := map[string]int{}
	for _, resource := range resources {
		if resource.Kind == "PodGroup" {
			continue
		}
		proof := resource.Suspension
		if resource.Kind == "Pod" || !resource.APIObjectPresent || !resource.CreationDisabled || proof == nil || proof.Generation < 2 || proof.AcknowledgedAt.IsZero() || proof.AcknowledgedAt.After(observation.ObservedAt.Add(5*time.Second)) || !proof.AcknowledgedAt.Add(time.Second).After(state.CloseRequestedAt) || proof.ObservedGeneration != 0 && proof.ObservedGeneration != proof.Generation || proof.ObservedGeneration == 0 && proof.Generation != 2 {
			return false
		}
		counts[resource.Kind]++
		if resource.Kind == "Job" && !proof.WorkerExitRetention {
			return false
		}
	}
	return counts["TrainJob"] == 1 && counts["JobSet"] == 1 && counts["Job"] == 1
}

func validPublication(execution biz.Execution, authority biz.RunAuthorityCandidate, state biz.ExecutionRuntime, publication biz.RuntimePublication, requireFreshObservation bool) error {
	if state.Observation == nil || state.Observation.SpecificationMismatch || state.Observation.Outcome != "SUCCEEDED" || !state.Observation.WritersAbsent || state.TrainingHandle == nil {
		return biz.ErrRuntimeNotReady
	}
	if _, err := databaseID(publication.ID); err != nil {
		return biz.ErrRuntimeConflict
	}
	// A write must be freshly verified. Later close observations do not revoke
	// the immutable publication already stored by that successful write.
	if !validOpaqueID(publication.LogicalKey) || !validOpaqueID(publication.ReceiptID) || publication.Upload.RunID != authority.RunID || publication.Upload.WorkflowUID != authority.WorkflowUID || !validOpaqueID(publication.Upload.TaskID) || !validOpaqueID(publication.Upload.PodUID) || publication.Upload.ContainerName == "" || publication.Upload.CompletedAt.IsZero() || publication.Upload.ObservedAt.Before(publication.Upload.CompletedAt) || publication.VerifiedAt.Before(publication.Upload.ObservedAt) || requireFreshObservation && publication.VerifiedAt.Before(state.Observation.ObservedAt) {
		return biz.ErrRuntimeConflict
	}
	files := make([]cpup01.OutputFile, 0, len(publication.Files))
	keys := make(map[string]bool)
	artifactIDs := make(map[string]bool)
	for _, file := range publication.Files {
		if _, err := databaseID(file.ArtifactID); err != nil || artifactIDs[file.ArtifactID] {
			return biz.ErrRuntimeConflict
		}
		artifactIDs[file.ArtifactID] = true
		if !validPublicationObject(execution, file.Object) || file.File.SizeBytes != file.Object.SizeBytes || file.File.SHA256 != file.Object.SHA256 || keys[file.Object.Key] {
			return biz.ErrRuntimeConflict
		}
		keys[file.Object.Key] = true
		files = append(files, file.File)
	}
	manifest, digest, err := cpup01.OutputManifestBytes(cpup01.AdmissionEnvelope(execution.Admission), files)
	if err != nil || !validPublicationObject(execution, publication.Manifest) || publication.Manifest.SizeBytes != int64(len(manifest)) || publication.Manifest.SHA256 != digest || keys[publication.Manifest.Key] {
		return biz.ErrRuntimeConflict
	}
	keys[publication.Manifest.Key] = true
	if execution.Snapshot.OutputContract.CreateTarBundle != (publication.Bundle != nil) {
		return biz.ErrRuntimeConflict
	}
	if publication.Bundle != nil && (!validPublicationObject(execution, *publication.Bundle) || keys[publication.Bundle.Key]) {
		return biz.ErrRuntimeConflict
	}
	return nil
}
func validPublicationObject(execution biz.Execution, object cpup01.FixedObjectRef) bool {
	scope := execution.Snapshot.PublicationScope
	versioned := object.VersionID != nil && *object.VersionID != "" && *object.VersionID != "null"
	immutable := object.ImmutableCopy != nil && *object.ImmutableCopy
	return object.StorageConnectionID == scope.StorageConnectionID && object.Bucket == scope.Bucket && biz.ValidStorageKey(object.Key) && strings.HasPrefix(object.Key, scope.ApprovedPrefix+"/") && object.SizeBytes > 0 && validDigest(object.SHA256) && (versioned || immutable)
}
func samePublication(left, right biz.RuntimePublication) bool {
	// Verification may sample later read timestamps. That is evidence of the
	// same immutable publication and must not replace the first receipt.
	left.ReceiptID = ""
	right.ReceiptID = ""
	left.VerifiedAt = right.VerifiedAt
	left.Upload.ObservedAt = right.Upload.ObservedAt
	left.Files = append([]biz.PublishedRuntimeFile{}, left.Files...)
	right.Files = append([]biz.PublishedRuntimeFile{}, right.Files...)
	sort.Slice(left.Files, func(i, j int) bool { return left.Files[i].File.RelativePath < left.Files[j].File.RelativePath })
	sort.Slice(right.Files, func(i, j int) bool { return right.Files[i].File.RelativePath < right.Files[j].File.RelativePath })
	return reflect.DeepEqual(left, right)
}

// Writer absence includes completed managed data-plane steps as well as
// training. The current close Pod is a control observer, not an uploader.
func validCloseEvidence(authority biz.RunAuthorityCandidate, reason string, evidence biz.ManagedCloseEvidence) bool {
	if evidence.RunID != authority.RunID || evidence.WorkflowUID != authority.WorkflowUID || evidence.ObservedAt.IsZero() || (reason == "NATURAL_TERMINAL" && len(evidence.SkippedTasks) != 0) {
		return false
	}
	if owner := evidence.OwnerTermination; owner != nil {
		if owner.WorkflowNodesSHA256 != "" && !validDigest(owner.WorkflowNodesSHA256) {
			return false
		}
		if (owner.RunState != "SUCCEEDED" && owner.RunState != "FAILED" && owner.RunState != "CANCELED") || (owner.WorkflowPhase != "Succeeded" && owner.WorkflowPhase != "Failed" && owner.WorkflowPhase != "Error") || owner.RunFinishedAt.IsZero() || owner.WorkflowFinishedAt.IsZero() || owner.RunFinishedAt.After(evidence.ObservedAt) || owner.WorkflowFinishedAt.After(evidence.ObservedAt) || !validOpaqueID(owner.WorkflowResourceVersion) {
			return false
		}
	} else if len(evidence.Resources)+len(evidence.SkippedTasks) < 4 {
		return false
	}
	skippedNames, skippedIDs := make(map[string]bool), make(map[string]bool)
	for _, task := range evidence.SkippedTasks {
		switch task.TaskName {
		case "prepare", "train-wait", "collect", "publish":
		default:
			return false
		}
		if skippedNames[task.TaskName] || !validOpaqueID(task.TaskID) || skippedIDs[task.TaskID] {
			return false
		}
		skippedNames[task.TaskName], skippedIDs[task.TaskID] = true, true
	}
	seen := make(map[string]bool, len(evidence.Resources))
	for _, resource := range evidence.Resources {
		if !validOpaqueID(resource.UID) || seen[resource.UID] || resource.APIVersion != "v1" || resource.Kind != "Pod" || resource.Namespace != authority.NamespaceName || len(validation.IsDNS1123Subdomain(resource.Name)) != 0 || resource.OwnerUID != authority.WorkflowUID || !resource.APIObjectPresent || !resource.Terminal {
			return false
		}
		if abort := resource.InitializationAbort; abort != nil {
			if evidence.OwnerTermination == nil || reason == "NATURAL_TERMINAL" || resource.ExitCode != nil || !resource.CreationDisabled || !validOpaqueID(abort.PodResourceVersion) || len(validation.IsDNS1123Subdomain(abort.NodeName)) != 0 || !abort.ValidAt(evidence.ObservedAt) {
				return false
			}
			seen[resource.UID] = true
			continue
		}
		if resource.ExitCode == nil {
			return false
		}
		if reason == "NATURAL_TERMINAL" && *resource.ExitCode != 0 {
			return false
		}
		seen[resource.UID] = true
	}
	return true
}
