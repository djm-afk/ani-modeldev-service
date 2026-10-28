package trainer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
)

func newFinetuningTrainingFixture(t *testing.T) *trainingFixture {
	t.Helper()
	f := newTrainingFixture(t)
	seed := conformance.FinetuningSnapshotV1()
	seed.Environment = f.plan.Snapshot.Environment
	seed.DeadlineAt = f.plan.Snapshot.DeadlineAt
	a := admissionfixture.Bind(t, biz.Admission{TenantID: f.plan.TenantID, OperationID: f.plan.OperationID, ExecutionID: f.plan.ExecutionID, Actor: "governance:user:trainer-fixture", Intent: conformance.FinetuningIntentV1(), Snapshot: seed, AcceptedAt: f.plan.Snapshot.Timing.AcceptedAt})
	plan, err := biz.FreezeTrainingPlan(biz.Execution{Admission: a}, f.plan.Workspace, f.plan.Timing.AdmittedAt)
	if err != nil {
		t.Fatal(err)
	}
	binding := f.plan.RuntimeBinding
	binding.Name, binding.ObservedSpecSHA256 = a.Snapshot.RuntimePlan.Name, a.Snapshot.RuntimePlan.ExpectedSpecSHA256
	f.plan, err = biz.BindTrainingRuntime(plan, binding)
	if err != nil {
		t.Fatal(err)
	}
	installFixtureRuntime(t, f)
	return f
}

func freezeFinetuningFixtureRuntime(t *testing.T, f *trainingFixture) {
	t.Helper()
	encoded, err := json.Marshal(f.runtime["spec"])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	f.plan.Snapshot.RuntimePlan.ExpectedSpecSHA256 = hex.EncodeToString(digest[:])
}

func TestFinetuningTrainingPartitionsWritesAndPreservesOriginalLookupDeadline(t *testing.T) {
	f := newFinetuningTrainingFixture(t)
	f.loseCreateResponse = true
	adapter := f.adapter(t)
	if _, err := adapter.CreateTraining(context.Background(), f.plan); !errors.Is(err, biz.ErrTrainingUncertain) {
		t.Fatal(err)
	}
	original, err := json.Marshal(f.train)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.FindTraining(context.Background(), f.plan); err != nil {
		t.Fatal(err)
	}
	if f.posts != 1 {
		t.Fatal("lost reply recreated finetuning")
	}
	current, err := json.Marshal(f.train)
	if err != nil || !reflect.DeepEqual(original, current) {
		t.Fatal("lookup changed the original deadline or template")
	}
	spec := runtimePodTemplate(f)["spec"].(map[string]any)
	if spec["serviceAccountName"] != "cpu-training" {
		t.Fatalf("unsupported/unsafe override: %v", spec)
	}
	volumes := spec["volumes"].([]any)
	if len(volumes) != 4 {
		t.Fatal("missing independent workspace partitions or cache")
	}
	seen := map[string]bool{}
	for _, value := range volumes {
		v := value.(map[string]any)
		name := v["name"].(string)
		if seen[name] {
			t.Fatal("Trainer override volume names collide")
		}
		seen[name] = true
		if _, exists := v["emptyDir"]; exists {
			t.Fatal("training cache escaped execution PVC")
		}
		if v["persistentVolumeClaim"].(map[string]any)["claimName"] != f.plan.Workspace.PVCName {
			t.Fatal("partition escaped original PVC")
		}

	}
	container := spec["containers"].([]any)[0].(map[string]any)
	if len(container) != 9 {
		t.Fatal("container overrides include credentials or unsupported security fields")
	}
	mounts := container["volumeMounts"].([]any)
	want := map[string]struct {
		subpath  string
		readOnly bool
	}{
		"/workspace/inputs":           {"inputs", true},
		"/workspace/training/adapter": {"training/adapter", false},
		"/workspace/reports/train":    {"reports/train", false},
		"/workspace/work/train":       {"work/train", false},
	}
	for _, value := range mounts {
		m := value.(map[string]any)
		expected, ok := want[m["mountPath"].(string)]
		if !ok || m["readOnly"] != expected.readOnly || (expected.subpath != "" && m["subPath"] != expected.subpath) {
			t.Fatalf("wrong partition: %v", m)
		}
		delete(want, m["mountPath"].(string))
	}
	if len(want) != 0 {
		t.Fatal("managed finetune entry paths are not mounted")
	}
	annotations := f.train["metadata"].(map[string]any)["annotations"].(map[string]any)
	if annotations["modeldev.ani.io/training-deadline-at"] != f.plan.Timing.DeadlineAt.Format(time.RFC3339Nano) {
		t.Fatal("lookup did not retain the original admitted deadline")
	}
}

func TestFinetuningTrainingRejectsUnsafeRuntimeBudgetAndWriterMaterial(t *testing.T) {
	for _, mode := range []string{"missing timing", "renewed deadline", "missing Job deadline", "wrong Job budget", "wrong UID", "writable root"} {
		t.Run(mode, func(t *testing.T) {
			f := newFinetuningTrainingFixture(t)
			pod := runtimePodTemplate(f)["spec"].(map[string]any)
			job := f.runtime["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["replicatedJobs"].([]any)[0].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			switch mode {
			case "missing timing":
				f.plan.Timing = nil
			case "renewed deadline":
				f.plan.Timing.DeadlineAt = f.plan.Timing.DeadlineAt.Add(time.Minute)
			case "missing Job deadline":
				delete(job, "activeDeadlineSeconds")
			case "wrong Job budget":
				job["activeDeadlineSeconds"] = float64(1800)
			case "wrong UID":
				pod["securityContext"].(map[string]any)["runAsUser"] = float64(1000)
			case "writable root":
				pod["containers"].([]any)[0].(map[string]any)["securityContext"].(map[string]any)["readOnlyRootFilesystem"] = false
			}
			freezeFinetuningFixtureRuntime(t, f)
			if _, err := f.adapter(t).CreateTraining(context.Background(), f.plan); !errors.Is(err, biz.ErrRuntimeConflict) {
				t.Fatalf("unsafe material accepted: %v", err)
			}
			if f.posts != 0 {
				t.Fatal("unsafe finetune created a TrainJob")
			}
		})
	}
}
