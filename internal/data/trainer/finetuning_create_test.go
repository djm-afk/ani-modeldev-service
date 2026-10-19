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

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func newFinetuningTrainingFixture(t *testing.T) *trainingFixture {
	t.Helper()
	f := newTrainingFixture(t)
	start := time.Now().UTC().Truncate(time.Microsecond)
	f.plan.Snapshot.Kind = "FINETUNING"
	f.plan.Snapshot.DeadlineAt = start.Add(3 * time.Hour)
	f.plan.Snapshot.Finetuning = &cpup01.FinetuningSnapshot{Budgets: cpup01.ComponentBudgets{TrainingTimeoutSeconds: 3600}}
	f.plan.FinetuningTiming = &biz.TrainingBudgetTiming{AdmittedAt: start, DeadlineAt: start.Add(time.Hour)}
	f.plan.Snapshot.Program.Command = []string{"/opt/venv/bin/python", "-I", "/opt/finetuning/finetune_entry.py"}
	f.plan.Snapshot.Program.ResolvedArgs = []string{"train", "--configuration", "/workspace/inputs/finetune_configuration.json", "--workspace", "/workspace"}
	f.plan.Snapshot.Resources.RequestMillicpu, f.plan.Snapshot.Resources.LimitMillicpu = 2000, 4000
	f.plan.Snapshot.Resources.RequestMemoryBytes, f.plan.Snapshot.Resources.LimitMemoryBytes = 6<<30, 8<<30
	pod := runtimePodTemplate(f)["spec"].(map[string]any)
	pod["securityContext"] = map[string]any{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "fsGroup": float64(10001)}
	container := pod["containers"].([]any)[0].(map[string]any)
	container["securityContext"].(map[string]any)["readOnlyRootFilesystem"] = true
	job := f.runtime["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["replicatedJobs"].([]any)[0].(map[string]any)
	job["template"].(map[string]any)["spec"].(map[string]any)["activeDeadlineSeconds"] = float64(3600)
	freezeFinetuningFixtureRuntime(t, f)
	return f
}

func freezeFinetuningFixtureRuntime(t *testing.T, f *trainingFixture) {
	t.Helper()
	encoded, err := json.Marshal(f.runtime["spec"])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	f.plan.Snapshot.Release.Runtime.ContentSHA256 = hex.EncodeToString(digest[:])
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
	overrides, _, _ := unstructured.NestedSlice(f.train, "spec", "podTemplateOverrides")
	ov := overrides[0].(map[string]any)
	spec := ov["spec"].(map[string]any)
	if len(spec) != 3 || spec["serviceAccountName"] != "cpu-training" {
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
		if name == "ft-tmp" {
			disk := v["emptyDir"].(map[string]any)
			if disk["sizeLimit"] != "4Gi" || disk["medium"] == "Memory" {
				t.Fatal("cache became an unbounded or memory volume")
			}
		} else if v["persistentVolumeClaim"].(map[string]any)["claimName"] != f.plan.Workspace.PVCName {
			t.Fatal("partition escaped the original PVC")
		}
	}
	container := spec["containers"].([]any)[0].(map[string]any)
	if len(container) != 2 {
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
		"/tmp":                        {"", false},
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
	if annotations["modeldev.ani.io/training-deadline-at"] != f.plan.FinetuningTiming.DeadlineAt.Format(time.RFC3339Nano) {
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
				f.plan.FinetuningTiming = nil
			case "renewed deadline":
				f.plan.FinetuningTiming.DeadlineAt = f.plan.FinetuningTiming.DeadlineAt.Add(time.Minute)
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
