package biz

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrTrainingLogsUnavailable = errors.New("TRAINING_LOGS_UNAVAILABLE")
var ErrTrainingLogNotFound = errors.New("TRAINING_LOG_NOT_FOUND")

type TrainingLogSource struct {
	LogID, Namespace, NamespaceUID, PodName, PodUID string
	OwnerUID, OwnerName, OwnerKind, OwnerAPIVersion string
	ContainerName                                   string
}

type TrainingLogOptions struct{ TailLines, MaxBytes uint32 }
type TrainingLogLine struct {
	Timestamp time.Time
	Text      string
}
type TrainingLogResult struct {
	Lines      []TrainingLogLine
	Truncated  bool
	ObservedAt time.Time
}

// RecordedTrainingLog retains one bounded tail with its original Pod and Job
// identities. It is independent of whether those API objects still exist.
type RecordedTrainingLog struct {
	Source TrainingLogSource
	Result TrainingLogResult
}

type TrainingLogReader interface {
	ReadTrainingLogs(context.Context, TrainingLogSource, TrainingLogOptions) (TrainingLogResult, error)
}

// ValidateRecordedTrainingLogs binds each saved tail to the original observed
// trainer Pod and its complete Job/JobSet/TrainJob chain. A receipt with no
// training Pods may legitimately contain no logs.
func ValidateRecordedTrainingLogs(record QueryRecord, logs []RecordedTrainingLog) error {
	seen := make(map[string]bool, len(logs))
	for _, log := range logs {
		source, err := TrainingLogSourceFor(record, log.Source.LogID)
		if err != nil || source != log.Source || seen[source.LogID] || !validRecordedTrainingLogResult(log.Result) {
			return ErrTrainingLogsUnavailable
		}
		seen[source.LogID] = true
	}
	return nil
}

func validRecordedTrainingLogResult(result TrainingLogResult) bool {
	validTime := func(value time.Time) bool { return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999 }
	if !validTime(result.ObservedAt) || len(result.Lines) == 0 || len(result.Lines) > 1000 {
		return false
	}
	bytes := 0
	for index, line := range result.Lines {
		if !validTime(line.Timestamp) || !utf8.ValidString(line.Text) || strings.Contains(line.Text, "\n") {
			return false
		}
		size := trainingLogLineBytes(line)
		if index > 0 {
			size++
		}
		if size > 65536-bytes {
			return false
		}
		bytes += size
	}
	return true
}

// trainingLogLineBytes accounts for the timestamp and separator from the
// original reader. A byte-capped final line may have no LF; only separators
// between returned lines are required when measuring the bounded payload.
func trainingLogLineBytes(line TrainingLogLine) int {
	return len(line.Timestamp.Format(time.RFC3339Nano)) + 1 + len(line.Text)
}

// RecordedTrainingLogsFor returns only a committed, identity-checked log tail.
// Capture in progress remains eligible for the original live reader. Callers
// receive a copy so per-request tail limits cannot rewrite the durable receipt.
func RecordedTrainingLogsFor(record QueryRecord, source TrainingLogSource, options TrainingLogOptions) (TrainingLogResult, bool, error) {
	if options.TailLines == 0 || options.TailLines > 1000 || options.MaxBytes == 0 || options.MaxBytes > 65536 {
		return TrainingLogResult{}, false, ErrTrainingLogsUnavailable
	}
	expected, err := TrainingLogSourceFor(record, source.LogID)
	if err != nil || expected != source {
		return TrainingLogResult{}, false, ErrTrainingLogsUnavailable
	}
	logs := []RecordedTrainingLog{}
	for _, resource := range record.Runtime.Observation.Resources {
		if resource.UID == source.PodUID && resource.TrainingLog != nil {
			logs = append(logs, *resource.TrainingLog)
		}
	}
	if len(logs) == 0 {
		receipt := record.Runtime.ComputeCleanup
		if receipt == nil || !receipt.LogsReady {
			return TrainingLogResult{}, false, nil
		}
		logs = receipt.Logs
	}
	if err := ValidateRecordedTrainingLogs(record, logs); err != nil {
		return TrainingLogResult{}, false, err
	}
	for _, log := range logs {
		if log.Source != source {
			continue
		}
		result := log.Result
		lines := result.Lines
		if len(lines) > int(options.TailLines) {
			lines = lines[len(lines)-int(options.TailLines):]
			result.Truncated = true
		}
		result.Lines = append([]TrainingLogLine(nil), lines...)
		bytes := 0
		for index, line := range result.Lines {
			bytes += trainingLogLineBytes(line)
			if index > 0 {
				bytes++
			}
		}
		for bytes > int(options.MaxBytes) && len(result.Lines) > 1 {
			bytes -= trainingLogLineBytes(result.Lines[0]) + 1
			result.Lines = result.Lines[1:]
			result.Truncated = true
		}
		if bytes > int(options.MaxBytes) {
			line := &result.Lines[0]
			budget := int(options.MaxBytes) - len(line.Timestamp.Format(time.RFC3339Nano)) - 1
			if budget < 0 {
				return TrainingLogResult{}, false, ErrTrainingLogsUnavailable
			}
			line.Text = line.Text[len(line.Text)-budget:]
			for !utf8.ValidString(line.Text) {
				line.Text = line.Text[1:]
			}
			result.Truncated = true
		}
		return result, true, nil
	}
	return TrainingLogResult{}, false, nil
}

// TrainingLogSourceFor resolves only durable observations of the fixed
// trainer container. A log ID can select a recorded segment, never a Pod name.
func TrainingLogSourceFor(record QueryRecord, logID string) (TrainingLogSource, error) {
	state := record.Runtime
	if state.Observation == nil || state.Training == nil || state.TrainingHandle == nil ||
		state.Observation.Handle != *state.TrainingHandle || state.Observation.ObservedAt.IsZero() {
		return TrainingLogSource{}, ErrTrainingLogsUnavailable
	}
	resources := make(map[string]RuntimeResource, len(state.Observation.Resources))
	for _, resource := range state.Observation.Resources {
		if resource.UID == "" {
			return TrainingLogSource{}, ErrTrainingLogsUnavailable
		}
		if _, duplicate := resources[resource.UID]; duplicate {
			return TrainingLogSource{}, ErrTrainingLogsUnavailable
		}
		resources[resource.UID] = resource
	}
	var selected *TrainingLogSource
	pods := 0
	namespace := record.Execution.Snapshot.Environment.NamespaceName
	for _, pod := range state.Observation.Resources {
		if pod.Kind != "Pod" {
			continue
		}
		pods++
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(record.Execution.ExecutionID+":"+pod.UID+":node")).String()
		if logID != "" && logID != id {
			continue
		}
		if selected != nil || pod.APIVersion != "v1" || pod.Namespace != namespace || !pod.APIObjectPresent && (!pod.Terminal || pod.ExitCode == nil) {
			return TrainingLogSource{}, ErrTrainingLogsUnavailable
		}
		owner, found := resources[pod.OwnerUID]
		if !found || owner.Kind != "Job" || owner.APIVersion != "batch/v1" || owner.Namespace != namespace {
			return TrainingLogSource{}, ErrTrainingLogsUnavailable
		}
		set, found := resources[owner.OwnerUID]
		if !found || set.Kind != "JobSet" || set.APIVersion != "jobset.x-k8s.io/v1alpha2" || set.Namespace != namespace {
			return TrainingLogSource{}, ErrTrainingLogsUnavailable
		}
		train, found := resources[set.OwnerUID]
		if !found || train.UID != state.TrainingHandle.TrainJobUID || train.Kind != "TrainJob" ||
			train.APIVersion != "trainer.kubeflow.org/v1alpha1" || train.Namespace != namespace || train.Name != state.Training.Name ||
			state.TrainingHandle.NamespaceUID != record.Execution.Snapshot.Environment.NamespaceUID {
			return TrainingLogSource{}, ErrTrainingLogsUnavailable
		}
		selected = &TrainingLogSource{LogID: id, Namespace: namespace, NamespaceUID: state.TrainingHandle.NamespaceUID,
			PodName: pod.Name, PodUID: pod.UID, OwnerUID: owner.UID, OwnerName: owner.Name,
			OwnerKind: owner.Kind, OwnerAPIVersion: owner.APIVersion, ContainerName: "node"}
	}
	if logID == "" && pods != 1 {
		return TrainingLogSource{}, ErrTrainingLogsUnavailable
	}
	if selected == nil {
		return TrainingLogSource{}, ErrTrainingLogNotFound
	}
	return *selected, nil
}
