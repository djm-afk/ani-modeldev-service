package runtimeproof

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/flowcontrol"
)

func TestComputeParentDependencyFailureDoesNotBecomeIdentityConflict(t *testing.T) {
	limiter := flowcontrol.NewTokenBucketRateLimiter(1, 1)
	limiter.Accept()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	limited := limiter.Wait(ctx)
	if limited == nil {
		t.Fatal("real client rate limiter fault not reached")
	}
	for _, failure := range []error{limited, context.DeadlineExceeded, errors.New("parent API unavailable")} {
		client := fake.NewSimpleDynamicClient(runtime.NewScheme())
		client.PrependReactor("get", "trainjobs", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, failure })
		parent := biz.RuntimeResource{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Namespace: "tenant-test", Name: "original-parent", UID: "original-parent-uid"}
		child := biz.RuntimeResource{APIVersion: "jobset.x-k8s.io/v1alpha2", Kind: "JobSet", Namespace: parent.Namespace, Name: "original-child", UID: "original-child-uid", OwnerUID: parent.UID}
		verifier := &Verifier{kube: client}
		err := verifier.computeParentsAbsent(context.Background(), []biz.RuntimeResource{parent, child}, child)
		if !errors.Is(err, biz.ErrCleanupUncertain) || errors.Is(err, biz.ErrCleanupConflict) {
			t.Fatal("dependency failure falsely became immutable identity conflict", err)
		}
		for _, action := range client.Actions() {
			if action.GetVerb() != "get" {
				t.Fatal("unproven parent absence authorized mutation")
			}
		}
	}
}
