package trainer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestLostTrainingResponseAfterRuntimeAndPVCDriftStillStopsOriginalUID(t *testing.T) {
	for _, mode := range []string{"runtime missing", "runtime drift", "pvc missing", "pvc replacement", "request drift", "namespace replacement"} {
		t.Run(mode, func(t *testing.T) {
			f := newTrainingFixture(t)
			f.loseCreateResponse = true
			a := f.adapter(t)
			if _, err := a.CreateTraining(context.Background(), f.plan); !errors.Is(err, biz.ErrTrainingUncertain) {
				t.Fatal("lost response did not retain the dispatched request", err)
			}
			switch mode {
			case "runtime missing":
				f.runtime = nil
			case "runtime drift":
				f.runtime["spec"] = map[string]any{}
			case "pvc missing":
				f.pvc = nil
			case "pvc replacement":
				_ = unstructured.SetNestedField(f.pvc, "replacement", "metadata", "uid")
			case "request drift":
				_ = unstructured.SetNestedField(f.train, "foreign", "spec", "trainer", "image")
			case "namespace replacement":
				_ = unstructured.SetNestedField(f.namespace, "replacement", "metadata", "uid")
			}
			if _, err := a.FindTraining(context.Background(), f.plan); err == nil {
				t.Fatal("normal lookup accepted mutable input drift")
			}
			handle, err := a.FindTrainingForClose(context.Background(), f.plan)
			if mode == "request drift" || mode == "namespace replacement" {
				if !errors.Is(err, biz.ErrRuntimeConflict) || f.patches != 0 {
					t.Fatal("close recovery accepted a different original request or namespace", handle, err)
				}
				return
			}
			if err != nil || handle.TrainJobUID != trainingUID || handle.PVCUID != workspaceUID {
				t.Fatal("close recovery lost the original dispatched identity", handle, err)
			}
			if err := a.StopTraining(context.Background(), f.plan, handle); err != nil || f.patches != 1 || f.posts != 1 {
				t.Fatal("original UID was not suspended exactly once without resending creation", err)
			}
		})
	}
}
