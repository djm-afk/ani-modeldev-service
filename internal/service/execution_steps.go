package service

import (
	"context"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Step states reported by the timeline. They describe persisted facts only, so
// a stopped or failed execution never shows an invented running step.
const (
	stepStatePending   = "PENDING"
	stepStateRunning   = "RUNNING"
	stepStateSucceeded = "SUCCEEDED"
	stepStateFailed    = "FAILED"
	stepStateStopped   = "STOPPED"
)

// Step roles. The sequence depends on kind; role names follow the managed step
// roles so the timeline and current_step stay comparable.
const (
	stepRolePrepare   = "prepare"
	stepRoleTrainWait = "train-wait"
	stepRoleExport    = "export"
	stepRoleVerify    = "verify"
	stepRoleCollect   = "collect"
	stepRolePublish   = "publish"
	stepRoleRegister  = "register"
	stepRoleClose     = "close"
)

// metricsReadLimit bounds a metrics object read. The output contract caps the
// file at 1 MiB, so a larger object is rejected instead of partially parsed.
const metricsReadLimit = 1 << 20

// executionSteps assembles the ordered timeline from committed runtime facts.
// Ending times are inferred from the next step's admission because managed step
// timings carry no finish fact.
func executionSteps(execution biz.Execution, runtime biz.ExecutionRuntime) []*modeldevv1.StepProgress {
	finetuning := execution.Snapshot.Kind == "FINETUNING"
	roles := []string{stepRolePrepare, stepRoleTrainWait}
	if finetuning {
		roles = append(roles, stepRoleExport, stepRoleVerify)
	}
	roles = append(roles, stepRoleCollect, stepRolePublish)
	if finetuning {
		roles = append(roles, stepRoleRegister)
	}
	roles = append(roles, stepRoleClose)
	registrationRequired := execution.Snapshot.Finetuning.RequiresModelRegistration()
	steps := make([]*modeldevv1.StepProgress, 0, len(roles))
	for _, role := range roles {
		steps = append(steps, stepProgress(role, runtime, registrationRequired))
	}
	return steps
}

func stepProgress(role string, runtime biz.ExecutionRuntime, registrationRequired bool) *modeldevv1.StepProgress {
	step := &modeldevv1.StepProgress{Role: role, State: stepStatePending}
	if exportOrVerify(role) {
		finishFinetuningStep(step, role, runtime)
		return step
	}
	if role == stepRoleClose {
		finishCloseStep(step, runtime)
		return step
	}
	finishManagedStep(step, role, runtime, registrationRequired)
	return step
}

func exportOrVerify(role string) bool {
	return role == stepRoleExport || role == stepRoleVerify
}

// finishFinetuningStep uses the only real process facts available: export and
// verify report their own start, finish and exit code.
func finishFinetuningStep(step *modeldevv1.StepProgress, role string, runtime biz.ExecutionRuntime) {
	var start *biz.FinetuningStepStart
	for i := range runtime.FinetuningSteps {
		if runtime.FinetuningSteps[i].Binding.Role == role {
			start = &runtime.FinetuningSteps[i]
		}
	}
	if start == nil {
		step.State = stepStatePending
		return
	}
	step.DeadlineAt = timestampOrNil(start.DeadlineAt)
	if result := start.Result; result != nil {
		step.StartedAt = timestampOrNil(result.StartedAt)
		step.FinishedAt = timestampOrNil(result.FinishedAt)
		step.ExitCode = result.ExitCode
		if result.ExitCode == 0 {
			step.State = stepStateSucceeded
		} else {
			step.State = stepStateFailed
		}
		return
	}
	step.StartedAt = timestampOrNil(start.AdmittedAt)
	step.State = activeStepState(runtime)
}

func finishCloseStep(step *modeldevv1.StepProgress, runtime biz.ExecutionRuntime) {
	if runtime.ClosedAt == nil {
		return
	}
	step.State = stepStateSucceeded
	step.FinishedAt = timestampOrNil(*runtime.ClosedAt)
	// The close anchor is the only evidence for when closure began; without it
	// the client sees a completed row rather than a guessed start.
	if evidence := runtime.CloseEvidence; evidence != nil {
		step.StartedAt = timestampOrNil(evidence.ObservedAt)
	}
}

// finishManagedStep covers prepare, train-wait, collect, publish and register.
// Their finish facts come from whichever successor fact exists first.
func finishManagedStep(step *modeldevv1.StepProgress, role string, runtime biz.ExecutionRuntime, registrationRequired bool) {
	startAt, deadlineAt := managedStepWindow(role, runtime)
	step.DeadlineAt = timestampOrNil(deadlineAt)
	step.StartedAt = timestampOrNil(startAt)
	step.State = managedStepState(role, runtime, registrationRequired)
	if step.State == stepStateSucceeded {
		step.FinishedAt = timestampOrNil(successorAnchor(role, runtime))
	}
}

func managedStepWindow(role string, runtime biz.ExecutionRuntime) (time.Time, time.Time) {
	if role == stepRoleTrainWait {
		if window := runtime.TrainingWindow; window != nil {
			return window.AdmittedAt, window.DeadlineAt
		}
		return time.Time{}, time.Time{}
	}
	if timing := biz.TimingForManagedRole(runtime, role); timing != nil {
		return timing.AdmittedAt, timing.DeadlineAt
	}
	return time.Time{}, time.Time{}
}

func managedStepState(role string, runtime biz.ExecutionRuntime, registrationRequired bool) string {
	switch role {
	case stepRolePrepare:
		if hasPostPrepareFact(runtime) {
			return stepStateSucceeded
		}
		if started(runtime, stepRolePrepare) {
			if runtime.CloseReason == "STEP_FAILED" {
				return stepStateFailed
			}
			return activeStepState(runtime)
		}
	case stepRoleTrainWait:
		if runtime.Publication != nil || biz.TimingForManagedRole(runtime, stepRoleCollect) != nil || finetuningStepStarted(runtime) {
			return stepStateSucceeded
		}
		if runtime.TrainingWindow != nil {
			if outcome := observationOutcome(runtime); outcome == "FAILED" {
				return stepStateFailed
			}
			return activeStepState(runtime)
		}
	case stepRoleCollect:
		if biz.TimingForManagedRole(runtime, stepRolePublish) != nil || runtime.Publication != nil {
			return stepStateSucceeded
		}
		// Same predicate as current_step: an admitted collect without publish is
		// the running step, unless the execution has already ended.
		if collectRunning(runtime) {
			return activeStepState(runtime)
		}
	case stepRolePublish:
		if runtime.Publication != nil {
			return stepStateSucceeded
		}
		if started(runtime, stepRolePublish) {
			if runtime.CloseReason == "STEP_FAILED" {
				return stepStateFailed
			}
			return activeStepState(runtime)
		}
	case stepRoleRegister:
		if registration := runtime.FinetuningRegistration; registration != nil {
			switch registration.State {
			case biz.RegistrationStateReady:
				return stepStateSucceeded
			case biz.RegistrationStateUnavailable:
				return stepStateFailed
			}
		}
		// A snapshot without a target only publishes artifacts, so the pod
		// confirms the bundle and records no registration fact on purpose.
		// Reading that silence as an interrupted registration would report a
		// STOPPED step on a delivery that never asked to register.
		if started(runtime, stepRoleRegister) {
			if !registrationRequired && runtime.ClosedAt != nil && runtime.CloseReason == "NATURAL_TERMINAL" {
				return stepStateSucceeded
			}
			return activeStepState(runtime)
		}
	}
	// No progress fact. A closed execution never turns such a step into RUNNING
	// or SUCCEEDED, so PENDING is the only safe remainder.
	return stepStatePending
}

// activeStepState separates a still-running step from one whose execution ended.
// RUNNING requires an open execution so a stop or failure cannot look live.
func activeStepState(runtime biz.ExecutionRuntime) string {
	if runtime.ClosedAt == nil {
		return stepStateRunning
	}
	if runtime.CloseReason == "STEP_FAILED" {
		return stepStateFailed
	}
	// USER_STOP, DEADLINE and every other terminal close stop the step that was
	// in flight; none of them may keep reporting RUNNING.
	return stepStateStopped
}

func started(runtime biz.ExecutionRuntime, role string) bool {
	return biz.TimingForManagedRole(runtime, role) != nil
}

func finetuningStepStarted(runtime biz.ExecutionRuntime) bool {
	return len(runtime.FinetuningSteps) > 0
}

func observationOutcome(runtime biz.ExecutionRuntime) string {
	if runtime.Observation == nil {
		return ""
	}
	return runtime.Observation.Outcome
}

// hasPostPrepareFact reports any evidence that prepare completed: entering
// training, a training window, or any downstream step admission.
func hasPostPrepareFact(runtime biz.ExecutionRuntime) bool {
	return runtime.TrainingWindow != nil || runtime.Training != nil || runtime.Publication != nil || finetuningStepStarted(runtime) ||
		biz.TimingForManagedRole(runtime, stepRoleCollect) != nil || biz.TimingForManagedRole(runtime, stepRolePublish) != nil || biz.TimingForManagedRole(runtime, stepRoleRegister) != nil
}

// successorAnchor returns the earliest time at which this step can be treated as
// finished, taken from the next observed fact.
func successorAnchor(role string, runtime biz.ExecutionRuntime) time.Time {
	candidates := make([]time.Time, 0, 4)
	add := func(at time.Time) {
		if !at.IsZero() {
			candidates = append(candidates, at)
		}
	}
	switch role {
	case stepRolePrepare:
		if window := runtime.TrainingWindow; window != nil {
			add(window.AdmittedAt)
		}
		for _, next := range []string{stepRoleExport, stepRoleVerify, stepRoleCollect, stepRolePublish} {
			if timing := biz.TimingForManagedRole(runtime, next); timing != nil {
				add(timing.AdmittedAt)
			}
		}
		for _, start := range runtime.FinetuningSteps {
			add(start.AdmittedAt)
		}
		if runtime.Publication != nil {
			add(runtime.Publication.Upload.CompletedAt)
		}
	case stepRoleTrainWait:
		for _, next := range []string{stepRoleExport, stepRoleVerify, stepRoleCollect, stepRolePublish} {
			if timing := biz.TimingForManagedRole(runtime, next); timing != nil {
				add(timing.AdmittedAt)
			}
		}
		for _, start := range runtime.FinetuningSteps {
			add(start.AdmittedAt)
		}
		if runtime.Publication != nil {
			add(runtime.Publication.Upload.CompletedAt)
		}
	case stepRoleCollect:
		if timing := biz.TimingForManagedRole(runtime, stepRolePublish); timing != nil {
			add(timing.AdmittedAt)
		}
		if runtime.Publication != nil {
			add(runtime.Publication.Upload.CompletedAt)
		}
	case stepRolePublish:
		if runtime.Publication != nil {
			add(runtime.Publication.Upload.CompletedAt)
		}
	case stepRoleRegister:
		if registration := runtime.FinetuningRegistration; registration != nil {
			add(registration.RecordedAt)
		}
	}
	if len(candidates) == 0 && runtime.ClosedAt != nil {
		add(*runtime.ClosedAt)
	}
	if len(candidates) == 0 {
		return time.Time{}
	}
	earliest := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Before(earliest) {
			earliest = candidate
		}
	}
	return earliest
}

func timestampOrNil(at time.Time) *timestamppb.Timestamp {
	if at.IsZero() {
		return nil
	}
	return timestamppb.New(at)
}

// currentStepFromFacts keeps current_step and the collect step in one place so
// the two cannot drift apart.
func currentStepFromFacts(runtime biz.ExecutionRuntime) modeldevv1.PipelineStep {
	if collectRunning(runtime) {
		return modeldevv1.PipelineStep_PIPELINE_STEP_COLLECT
	}
	return modeldevv1.PipelineStep_PIPELINE_STEP_UNSPECIFIED
}

// collectRunning is true while collect is admitted and publish has not started.
func collectRunning(runtime biz.ExecutionRuntime) bool {
	return biz.TimingForManagedRole(runtime, stepRoleCollect) != nil && biz.TimingForManagedRole(runtime, stepRolePublish) == nil && runtime.Publication == nil
}

// executionMetrics derives the training curve from the published METRICS
// artifact. Any failure degrades to an absent curve: the detail response cannot
// fail because a metric file is missing, unreadable or malformed.
func (query *Query) executionMetrics(ctx context.Context, record biz.QueryRecord) []*trainingv1.Metric {
	if query == nil || query.metrics == nil {
		return nil
	}
	publication := record.Runtime.Publication
	if publication == nil {
		return nil
	}
	var metrics *biz.PublishedRuntimeFile
	for i := range publication.Files {
		if publication.Files[i].File.Role == "METRICS" {
			metrics = &publication.Files[i]
			break
		}
	}
	if metrics == nil {
		return nil
	}
	document, err := query.metrics.ReadObject(ctx, record.Execution.Snapshot.PublicationScope, metrics.Object, metricsReadLimit)
	if err != nil {
		return nil
	}
	return parseMetrics(document)
}
