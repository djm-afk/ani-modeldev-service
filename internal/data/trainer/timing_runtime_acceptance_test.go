package trainer_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRuntimeSameNameReplacementCannotComputeButOriginalTrainingStillStops(t *testing.T) {
	f := newTrainingFixture(t)
	a := f.adapter(t)
	ctx := context.Background()
	original, err := a.CreateTraining(ctx, f.plan)
	if err != nil || original.TrainJobUID != trainingUID {
		t.Fatal("original training", err)
	}
	f.mu.Lock()
	originalSpec, _, err := unstructured.NestedMap(f.runtime, "spec")
	if err == nil {
		err = unstructured.SetNestedField(f.runtime, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "metadata", "uid")
	}
	replacementSpec, _, readErr := unstructured.NestedMap(f.runtime, "spec")
	name, _, _ := unstructured.NestedString(f.runtime, "metadata", "name")
	f.mu.Unlock()
	if err != nil || readErr != nil || name != f.plan.RuntimeBinding.Name || !reflect.DeepEqual(originalSpec, replacementSpec) {
		t.Fatal("external replacement fixture changed more than the original Runtime UID", err, readErr)
	}
	if _, err := a.FindTraining(ctx, f.plan); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("normal lookup adopted the same-name replacement Runtime", err)
	}
	if _, err := a.CreateTraining(ctx, f.plan); !errors.Is(err, biz.ErrRuntimeConflict) {
		t.Fatal("new computation accepted the same-name replacement Runtime", err)
	}
	recovered, err := a.FindTrainingForClose(ctx, f.plan)
	if err != nil || recovered != original {
		t.Fatal("close lookup lost the original training UID and frozen request", err)
	}
	if err := a.StopTraining(ctx, f.plan, original); err != nil {
		t.Fatal("original training became unstoppable after Runtime replacement", err)
	}
	observed, err := a.ObserveTraining(ctx, f.plan, original, nil)
	if err != nil || observed.WritersAbsent {
		t.Fatal("suspension alone was mistaken for exited original writers", err)
	}
	f.setTerminal(t, 7)
	observed, err = a.ObserveTraining(ctx, f.plan, original, observed.Resources)
	if err != nil || observed.WritersAbsent {
		t.Fatalf("uncommitted terminal logs/exit released the original worker: %+v %v", observed, err)
	}
	observed, err = a.ObserveTraining(ctx, f.plan, original, observed.Resources)
	if err != nil || !observed.WritersAbsent || observed.Outcome != "FAILED" {
		t.Fatalf("original failed workers were not retained: %+v / %v", observed, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	uid, _, _ := unstructured.NestedString(f.train, "metadata", "uid")
	if uid != original.TrainJobUID || f.posts != 1 || f.patches != 1 || f.deletes != 0 {
		t.Fatalf("replacement caused a resend, retarget or deletion: uid=%s posts=%d patches=%d deletes=%d", uid, f.posts, f.patches, f.deletes)
	}
}
