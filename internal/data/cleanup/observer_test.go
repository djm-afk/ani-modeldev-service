package cleanup_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/cleanup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A configured GPU observer may refuse owner-signature verification. The
// management preflight must preserve that capability and its refusal.
type refusingManagementObserver struct {
	biz.TrainingRuntime
	calls int
}

func (o *refusingManagementObserver) ObserveTraining(context.Context, biz.TrainingPlan, biz.TrainingHandle, []biz.RuntimeResource) (biz.TrainingRuntimeObservation, error) {
	o.calls++
	return biz.TrainingRuntimeObservation{}, biz.ErrRuntimeNotReady
}

func TestManagementCleanupPreservesConfiguredTrainingObserver(t *testing.T) {
	f := newCleanupResolutionFixture(t)
	plan, err := biz.CleanupPlanFor(f.record)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range plan.Targets {
		object := cleanupControllerObject(target, "9", target.OwnerUID != "")
		object.SetGeneration(1)
		object.Object["spec"] = map[string]any{"suspend": false}
		if _, err := f.kube.Resource(controllerResource(target)).Namespace(f.namespace()).Create(context.Background(), object, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	f.kube.ClearActions()
	observer := &refusingManagementObserver{}
	adapter := cleanup.New(f.kube, f.proof)
	if method := reflect.ValueOf(adapter).MethodByName("WithTrainingRuntime"); method.IsValid() {
		method.Call([]reflect.Value{reflect.ValueOf(observer)})
	}
	err = adapter.VerifyCleanupWritersAbsent(context.Background(), f.record)
	if observer.calls != 1 || !errors.Is(err, biz.ErrRuntimeNotReady) {
		t.Fatalf("MANAGEMENT_GPU_OWNER_OBSERVER_BYPASSED: calls=%d err=%v", observer.calls, err)
	}
	f.assertReadOnly(t)
}
