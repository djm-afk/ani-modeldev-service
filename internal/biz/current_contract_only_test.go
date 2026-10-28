package biz_test

import (
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"testing"
)

func TestCurrentRuntimeRequiresVolcanoPolicy(t *testing.T) {
	plan := conformance.SnapshotV1().RuntimePlan
	plan.ExpectedSpec = []byte(`{"template":{}}`)
	if _, err := biz.RuntimePlanSchedulesPodGroup(plan); err == nil {
		t.Fatal("accepted a retired runtime without current Volcano policy")
	}
}
func TestVerificationSourceErrorsAreIndependentClasses(t *testing.T) {
	for source, content := range map[error]error{biz.ErrInputSourceUnavailable: biz.ErrInputVerification, biz.ErrObjectSourceUnavailable: biz.ErrObjectVerification} {
		if errors.Is(source, content) {
			t.Fatal("source outage retains compatibility content parent")
		}
		if !errors.Is(source, source) {
			t.Fatal("lost independent source class")
		}
	}
}
func TestManagedStepsRequireCurrentTaskIdentityResolver(t *testing.T) {
	calls := []string{}
	if _, err := biz.NewManagedSteps(&taskIdentityAuthority{}, &taskIdentityExecutions{}, &taskIdentityWorkload{calls: &calls}, &taskIdentityLegacyRuns{calls: &calls}); err == nil {
		t.Fatal("accepted a verifier without authenticated task identity")
	}
}
