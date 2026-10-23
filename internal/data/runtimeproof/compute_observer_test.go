package runtimeproof_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
)

// This decorates the production Trainer boundary. The configured observer may
// carry the GPU owner signer; recreating an adapter discards that capability.
type configuredCleanupObserver struct {
	biz.TrainingRuntime
	calls int
	err   error
}

func (o *configuredCleanupObserver) ObserveTraining(ctx context.Context, plan biz.TrainingPlan, handle biz.TrainingHandle, history []biz.RuntimeResource) (biz.TrainingRuntimeObservation, error) {
	o.calls++
	if o.err != nil {
		return biz.TrainingRuntimeObservation{}, o.err
	}
	return o.TrainingRuntime.ObserveTraining(ctx, plan, handle, history)
}

func TestComputeCleanupPreservesConfiguredTrainingObserver(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured owner capability", true: "configured owner refuses observation"}[denied], func(t *testing.T) {
			f := newComputeCleanupFixture(t, false)
			observer := &configuredCleanupObserver{TrainingRuntime: trainer.New(f.kube)}
			if denied {
				observer.err = biz.ErrRuntimeNotReady
			}
			// Keep the test runnable against 886: the old cleanup bypasses the
			// configured capability and wrongly succeeds even when it refuses.
			if method := reflect.ValueOf(f.step.proof).MethodByName("WithTrainingRuntime"); method.IsValid() {
				method.Call([]reflect.Value{reflect.ValueOf(observer)})
			}
			result, err := f.worker(t).ReconcileOnce(context.Background())
			if denied {
				if err == nil && result.Unresolved == 0 || len(f.deleted) != 0 || f.logReads != 0 {
					t.Fatalf("CONFIGURED_GPU_OWNER_OBSERVER_BYPASSED: result=%+v deletes=%v logs=%d err=%v", result, f.deleted, f.logReads, err)
				}
			} else if err != nil || result.Unresolved != 0 || len(f.deleted) != 7 || f.logReads != 1 {
				t.Fatalf("configured observer did not complete original cleanup: %+v %v", result, err)
			}
			if observer.calls != 1 || denied && !errors.Is(observer.err, biz.ErrRuntimeNotReady) {
				t.Fatalf("cleanup discarded the configured owner observer: calls=%d", observer.calls)
			}
		})
	}
}
