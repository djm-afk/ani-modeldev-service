package service

import (
	"context"
	"errors"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var stepTestStart = time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)

func managedTiming(role string, offset time.Duration) biz.ManagedStepTiming {
	return biz.ManagedStepTiming{Binding: biz.FinetuningStepBinding{Role: role}, AdmittedAt: stepTestStart.Add(offset), DeadlineAt: stepTestStart.Add(offset + time.Hour)}
}

func finetuningStep(role string, offset time.Duration, result *biz.FinetuningStepResult) biz.FinetuningStepStart {
	return biz.FinetuningStepStart{Binding: biz.FinetuningStepBinding{Role: role}, AdmittedAt: stepTestStart.Add(offset), DeadlineAt: stepTestStart.Add(offset + time.Hour), Result: result}
}

func generalTrainingExecution() biz.Execution {
	return biz.Execution{Admission: biz.Admission{Snapshot: conformance.SnapshotV1()}}
}

func finetuningExecution(kind string) biz.Execution {
	return biz.Execution{Admission: biz.Admission{Snapshot: cpup01.Snapshot{Kind: kind}}}
}

func stepByRole(t *testing.T, steps []*modeldevv1.StepProgress, role string) *modeldevv1.StepProgress {
	t.Helper()
	for _, step := range steps {
		if step.Role == role {
			return step
		}
	}
	t.Fatalf("step %q missing from %v", role, stepRoles(steps))
	return nil
}

func stepRoles(steps []*modeldevv1.StepProgress) []string {
	roles := make([]string, 0, len(steps))
	for _, step := range steps {
		roles = append(roles, step.Role)
	}
	return roles
}

func TestExecutionStepsGeneralTrainingSequence(t *testing.T) {
	steps := executionSteps(generalTrainingExecution(), biz.ExecutionRuntime{})
	want := []string{stepRolePrepare, stepRoleTrainWait, stepRoleCollect, stepRolePublish, stepRoleClose}
	if len(steps) != len(want) {
		t.Fatalf("steps=%v, want %v", stepRoles(steps), want)
	}
	for i, role := range want {
		if steps[i].Role != role {
			t.Fatalf("steps=%v, want %v", stepRoles(steps), want)
		}
		if steps[i].State != stepStatePending || steps[i].StartedAt != nil || steps[i].FinishedAt != nil {
			t.Fatalf("%s step=%v, want bare PENDING", role, steps[i])
		}
	}
}

func TestExecutionStepsFinetuningSequence(t *testing.T) {
	steps := executionSteps(finetuningExecution("FINETUNING"), biz.ExecutionRuntime{})
	want := []string{stepRolePrepare, stepRoleTrainWait, stepRoleExport, stepRoleVerify, stepRoleCollect, stepRolePublish, stepRoleRegister, stepRoleClose}
	if len(steps) != len(want) {
		t.Fatalf("steps=%v, want %v", stepRoles(steps), want)
	}
	for i, role := range want {
		if steps[i].Role != role {
			t.Fatalf("steps=%v, want %v", stepRoles(steps), want)
		}
	}
}

func TestExecutionStepsRunningGeneralTraining(t *testing.T) {
	runtime := biz.ExecutionRuntime{
		Workspace:      &biz.WorkspaceBinding{},
		StepTimings:    []biz.ManagedStepTiming{managedTiming(stepRolePrepare, 0)},
		Training:       &biz.TrainingPlan{},
		TrainingWindow: &biz.TrainingBudgetTiming{AdmittedAt: stepTestStart.Add(5 * time.Minute), DeadlineAt: stepTestStart.Add(time.Hour)},
		Observation:    &biz.TrainingRuntimeObservation{Outcome: "RUNNING", ObservedAt: stepTestStart.Add(10 * time.Minute)},
	}
	steps := executionSteps(generalTrainingExecution(), runtime)

	prepare := stepByRole(t, steps, stepRolePrepare)
	if prepare.State != stepStateSucceeded || !prepare.StartedAt.AsTime().Equal(stepTestStart) || !prepare.FinishedAt.AsTime().Equal(stepTestStart.Add(5*time.Minute)) {
		t.Fatalf("prepare=%v", prepare)
	}
	train := stepByRole(t, steps, stepRoleTrainWait)
	if train.State != stepStateRunning || !train.StartedAt.AsTime().Equal(stepTestStart.Add(5*time.Minute)) || train.FinishedAt != nil || !train.DeadlineAt.AsTime().Equal(stepTestStart.Add(time.Hour)) {
		t.Fatalf("train-wait=%v", train)
	}
	if collect := stepByRole(t, steps, stepRoleCollect); collect.State != stepStatePending {
		t.Fatalf("collect=%v, want PENDING", collect)
	}
}

func TestExecutionStepsRunningCollect(t *testing.T) {
	runtime := biz.ExecutionRuntime{
		Workspace:      &biz.WorkspaceBinding{},
		StepTimings:    []biz.ManagedStepTiming{managedTiming(stepRolePrepare, 0), managedTiming(stepRoleCollect, 30*time.Minute)},
		Training:       &biz.TrainingPlan{},
		TrainingWindow: &biz.TrainingBudgetTiming{AdmittedAt: stepTestStart.Add(5 * time.Minute), DeadlineAt: stepTestStart.Add(time.Hour)},
		Observation:    &biz.TrainingRuntimeObservation{Outcome: "SUCCEEDED", ObservedAt: stepTestStart.Add(25 * time.Minute)},
	}
	steps := executionSteps(generalTrainingExecution(), runtime)

	if train := stepByRole(t, steps, stepRoleTrainWait); train.State != stepStateSucceeded || !train.FinishedAt.AsTime().Equal(stepTestStart.Add(30*time.Minute)) {
		t.Fatalf("train-wait=%v", train)
	}
	collect := stepByRole(t, steps, stepRoleCollect)
	if collect.State != stepStateRunning || !collect.StartedAt.AsTime().Equal(stepTestStart.Add(30*time.Minute)) || collect.FinishedAt != nil {
		t.Fatalf("collect=%v", collect)
	}
	if publish := stepByRole(t, steps, stepRolePublish); publish.State != stepStatePending {
		t.Fatalf("publish=%v, want PENDING", publish)
	}
	if !collectRunning(runtime) {
		t.Fatal("collectRunning=false, want true")
	}
}

func TestExecutionStepsPublishedExecution(t *testing.T) {
	uploadedAt := stepTestStart.Add(40 * time.Minute)
	closedAt := stepTestStart.Add(45 * time.Minute)
	runtime := biz.ExecutionRuntime{
		Workspace:      &biz.WorkspaceBinding{},
		StepTimings:    []biz.ManagedStepTiming{managedTiming(stepRolePrepare, 0), managedTiming(stepRoleCollect, 30*time.Minute), managedTiming(stepRolePublish, 38*time.Minute)},
		Training:       &biz.TrainingPlan{},
		TrainingWindow: &biz.TrainingBudgetTiming{AdmittedAt: stepTestStart.Add(5 * time.Minute), DeadlineAt: stepTestStart.Add(time.Hour)},
		Observation:    &biz.TrainingRuntimeObservation{Outcome: "SUCCEEDED"},
		Publication:    &biz.RuntimePublication{Upload: biz.UploadCompletion{CompletedAt: uploadedAt}, VerifiedAt: uploadedAt},
		CloseReason:    "NATURAL_TERMINAL",
		ClosedAt:       &closedAt,
	}
	steps := executionSteps(generalTrainingExecution(), runtime)
	for _, role := range []string{stepRolePrepare, stepRoleTrainWait, stepRoleCollect, stepRolePublish, stepRoleClose} {
		if step := stepByRole(t, steps, role); step.State != stepStateSucceeded {
			t.Fatalf("%s=%v, want SUCCEEDED", role, step)
		}
	}
	if publish := stepByRole(t, steps, stepRolePublish); !publish.FinishedAt.AsTime().Equal(uploadedAt) {
		t.Fatalf("publish=%v", publish)
	}
	if close := stepByRole(t, steps, stepRoleClose); !close.FinishedAt.AsTime().Equal(closedAt) || close.StartedAt != nil {
		t.Fatalf("close=%v", close)
	}
	if collectRunning(runtime) {
		t.Fatal("collectRunning=true after publication")
	}
}

func TestExecutionStepsFinetuningStepResults(t *testing.T) {
	exportStart, exportEnd := stepTestStart.Add(time.Hour), stepTestStart.Add(70*time.Minute)
	runtime := biz.ExecutionRuntime{
		Workspace:      &biz.WorkspaceBinding{},
		StepTimings:    []biz.ManagedStepTiming{managedTiming(stepRolePrepare, 0), managedTiming(stepRoleCollect, 3*time.Hour)},
		Training:       &biz.TrainingPlan{},
		TrainingWindow: &biz.TrainingBudgetTiming{AdmittedAt: stepTestStart.Add(5 * time.Minute), DeadlineAt: stepTestStart.Add(time.Hour)},
		Observation:    &biz.TrainingRuntimeObservation{Outcome: "SUCCEEDED"},
		FinetuningSteps: []biz.FinetuningStepStart{
			finetuningStep(stepRoleExport, time.Hour, &biz.FinetuningStepResult{StartedAt: exportStart, FinishedAt: exportEnd, ExitCode: 0}),
			finetuningStep(stepRoleVerify, 2*time.Hour, &biz.FinetuningStepResult{StartedAt: stepTestStart.Add(2 * time.Hour), FinishedAt: stepTestStart.Add(150 * time.Minute), ExitCode: 3}),
		},
	}
	steps := executionSteps(finetuningExecution("FINETUNING"), runtime)

	export := stepByRole(t, steps, stepRoleExport)
	if export.State != stepStateSucceeded || export.ExitCode != 0 || !export.StartedAt.AsTime().Equal(exportStart) || !export.FinishedAt.AsTime().Equal(exportEnd) {
		t.Fatalf("export=%v", export)
	}
	verify := stepByRole(t, steps, stepRoleVerify)
	if verify.State != stepStateFailed || verify.ExitCode != 3 || !verify.FinishedAt.AsTime().Equal(stepTestStart.Add(150*time.Minute)) {
		t.Fatalf("verify=%v", verify)
	}
	if train := stepByRole(t, steps, stepRoleTrainWait); train.State != stepStateSucceeded || !train.FinishedAt.AsTime().Equal(exportStart) {
		t.Fatalf("train-wait=%v", train)
	}
	if collect := stepByRole(t, steps, stepRoleCollect); collect.State != stepStateRunning {
		t.Fatalf("collect=%v, want RUNNING", collect)
	}
}

func TestExecutionStepsUserStopLeavesNoRunningStep(t *testing.T) {
	closedAt := stepTestStart.Add(20 * time.Minute)
	runtime := biz.ExecutionRuntime{
		Workspace:      &biz.WorkspaceBinding{},
		StepTimings:    []biz.ManagedStepTiming{managedTiming(stepRolePrepare, 0)},
		Training:       &biz.TrainingPlan{},
		TrainingWindow: &biz.TrainingBudgetTiming{AdmittedAt: stepTestStart.Add(5 * time.Minute), DeadlineAt: stepTestStart.Add(time.Hour)},
		Observation:    &biz.TrainingRuntimeObservation{Outcome: "RUNNING"},
		CloseReason:    string(biz.CloseReasonUserStop),
		ClosedAt:       &closedAt,
	}
	steps := executionSteps(generalTrainingExecution(), runtime)

	if train := stepByRole(t, steps, stepRoleTrainWait); train.State != stepStateStopped {
		t.Fatalf("train-wait=%v, want STOPPED", train)
	}
	for _, role := range []string{stepRoleCollect, stepRolePublish} {
		if step := stepByRole(t, steps, role); step.State != stepStatePending {
			t.Fatalf("%s=%v, want PENDING", role, step)
		}
	}
	if prepare := stepByRole(t, steps, stepRolePrepare); prepare.State != stepStateSucceeded {
		t.Fatalf("prepare=%v", prepare)
	}
}

func TestExecutionStepsStepFailureMarksPrepare(t *testing.T) {
	closedAt := stepTestStart.Add(10 * time.Minute)
	runtime := biz.ExecutionRuntime{
		StepTimings: []biz.ManagedStepTiming{managedTiming(stepRolePrepare, 0)},
		CloseReason: "STEP_FAILED",
		ClosedAt:    &closedAt,
	}
	steps := executionSteps(generalTrainingExecution(), runtime)
	prepare := stepByRole(t, steps, stepRolePrepare)
	if prepare.State != stepStateFailed || prepare.FinishedAt != nil {
		t.Fatalf("prepare=%v, want FAILED without finish time", prepare)
	}
	for _, role := range []string{stepRoleTrainWait, stepRoleCollect, stepRolePublish} {
		if step := stepByRole(t, steps, role); step.State != stepStatePending {
			t.Fatalf("%s=%v, want PENDING", role, step)
		}
	}
}

func TestExecutionStepsRegistrationStates(t *testing.T) {
	for _, test := range []struct {
		name  string
		state string
		want  string
	}{
		{name: "ready", state: biz.RegistrationStateReady, want: stepStateSucceeded},
		{name: "unavailable", state: biz.RegistrationStateUnavailable, want: stepStateFailed},
		{name: "pending", state: biz.RegistrationStatePending, want: stepStateRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := biz.ExecutionRuntime{
				StepTimings:            []biz.ManagedStepTiming{managedTiming(stepRoleRegister, time.Hour)},
				FinetuningRegistration: &biz.FinetuningRegistration{State: test.state, RecordedAt: stepTestStart.Add(2 * time.Hour)},
			}
			steps := executionSteps(finetuningExecution("FINETUNING"), runtime)
			register := stepByRole(t, steps, stepRoleRegister)
			if register.State != test.want {
				t.Fatalf("register=%v, want %s", register, test.want)
			}
			if test.want == stepStateSucceeded && !register.FinishedAt.AsTime().Equal(stepTestStart.Add(2*time.Hour)) {
				t.Fatalf("register=%v", register)
			}
		})
	}
}

func TestCurrentStepFromFactsCollectFix(t *testing.T) {
	collect := biz.ExecutionRuntime{
		Training:    &biz.TrainingPlan{},
		StepTimings: []biz.ManagedStepTiming{managedTiming(stepRoleCollect, 0)},
	}
	if step := currentStepFromFacts(collect); step != modeldevv1.PipelineStep_PIPELINE_STEP_COLLECT {
		t.Fatalf("step=%v, want COLLECT", step)
	}
	admittedPublish := collect
	admittedPublish.StepTimings = append(admittedPublish.StepTimings, managedTiming(stepRolePublish, time.Minute))
	if step := currentStepFromFacts(admittedPublish); step != modeldevv1.PipelineStep_PIPELINE_STEP_UNSPECIFIED {
		t.Fatalf("step=%v, want unspecified once publish is admitted", step)
	}
	published := collect
	published.Publication = &biz.RuntimePublication{}
	if step := currentStepFromFacts(published); step != modeldevv1.PipelineStep_PIPELINE_STEP_UNSPECIFIED {
		t.Fatalf("step=%v, want unspecified after publication", step)
	}
	if step := currentStepFromFacts(biz.ExecutionRuntime{Training: &biz.TrainingPlan{}}); step != modeldevv1.PipelineStep_PIPELINE_STEP_UNSPECIFIED {
		t.Fatalf("step=%v, want unspecified without collect", step)
	}
}

type stubMetricsReader struct {
	document []byte
	err      error
	calls    int
	scope    cpup01.StorageScope
	object   cpup01.FixedObjectRef
	limit    int64
}

func (reader *stubMetricsReader) ReadObject(_ context.Context, scope cpup01.StorageScope, object cpup01.FixedObjectRef, maxBytes int64) ([]byte, error) {
	reader.calls++
	reader.scope, reader.object, reader.limit = scope, object, maxBytes
	return reader.document, reader.err
}

func metricsRecord(roles ...string) biz.QueryRecord {
	snapshot := conformance.SnapshotV1()
	files := make([]biz.PublishedRuntimeFile, 0, len(roles))
	for _, role := range roles {
		files = append(files, biz.PublishedRuntimeFile{ArtifactID: "artifact", File: cpup01.OutputFile{RelativePath: "metrics.jsonl", Role: role}, Object: cpup01.FixedObjectRef{Key: "tenant-fixed/executions/metrics.jsonl"}})
	}
	return biz.QueryRecord{
		Execution: biz.Execution{Admission: biz.Admission{Snapshot: snapshot}},
		Runtime:   biz.ExecutionRuntime{Publication: &biz.RuntimePublication{Files: files}},
	}
}

func TestExecutionMetricsReadsPublishedMetrics(t *testing.T) {
	reader := &stubMetricsReader{document: []byte(`{"schema":"ani.metric.v1","step":1,"name":"train.loss","value":0.5}`)}
	query := NewQuery(nil, nil).WithMetrics(reader)
	metrics := query.executionMetrics(context.Background(), metricsRecord("SUMMARY", "METRICS"))
	if len(metrics) != 1 || metrics[0].Name != "train.loss" {
		t.Fatalf("metrics=%v", metrics)
	}
	if reader.scope != conformance.SnapshotV1().PublicationScope || reader.limit != metricsReadLimit {
		t.Fatalf("scope=%+v limit=%d", reader.scope, reader.limit)
	}
}

func TestExecutionMetricsDegradesQuietly(t *testing.T) {
	for _, test := range []struct {
		name   string
		reader *stubMetricsReader
		record biz.QueryRecord
	}{
		{name: "no reader", reader: nil, record: metricsRecord("METRICS")},
		{name: "no publication", reader: &stubMetricsReader{document: []byte("{}")}, record: biz.QueryRecord{Execution: biz.Execution{Admission: biz.Admission{Snapshot: conformance.SnapshotV1()}}}},
		{name: "no metrics file", reader: &stubMetricsReader{document: []byte("{}")}, record: metricsRecord("SUMMARY")},
		{name: "read failure", reader: &stubMetricsReader{err: errors.New("unavailable")}, record: metricsRecord("METRICS")},
		{name: "corrupt document", reader: &stubMetricsReader{document: []byte("not json")}, record: metricsRecord("METRICS")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var query *Query
			if test.reader != nil {
				query = NewQuery(nil, nil).WithMetrics(test.reader)
			} else {
				query = NewQuery(nil, nil)
			}
			if metrics := query.executionMetrics(context.Background(), test.record); len(metrics) != 0 {
				t.Fatalf("metrics=%v, want none", metrics)
			}
		})
	}
}
