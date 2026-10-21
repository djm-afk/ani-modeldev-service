package biz_test

import (
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"strings"
	"testing"
	"time"
)

func trainingPlanAdmission(t *testing.T, fine bool) (biz.Execution, biz.WorkspaceBinding) {
	t.Helper()
	s, i := conformance.SnapshotV1(), conformance.IntentV1()
	if fine {
		s, i = conformance.FinetuningSnapshotV1(), conformance.FinetuningIntentV1()
	}
	id := s.IdentityRef
	a := admissionfixture.Bind(t, biz.Admission{TenantID: id.TenantID, Actor: id.Actor, OperationID: id.OperationID, ExecutionID: id.ExecutionID, Intent: i, Snapshot: s, AcceptedAt: s.DeadlineAt.Add(-3 * time.Hour)})
	w := s.Workspace
	return biz.Execution{Admission: a}, biz.WorkspaceBinding{Mode: w.Mode, NamespaceName: s.Environment.NamespaceName, NamespaceUID: s.Environment.NamespaceUID, PVCName: biz.ExpectedWorkspacePVCName(a.ExecutionID), PVCUID: "dddddddd-eeee-4fff-8111-222222222222", InputSubpath: w.InputSubpath, TrainingSubpath: w.TrainingSubpath, ReportsSubpath: w.ReportsSubpath, PublicationSubpath: w.PublicationSubpath, PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256}
}

func TestEveryTrainingPlanNeedsAnOriginalAdmittedTimeAndUsesTheFrozenStepWindow(t *testing.T) {
	for _, fine := range []bool{false, true} {
		execution, workspace := trainingPlanAdmission(t, fine)
		if _, err := biz.FreezeTrainingPlan(execution, workspace); err == nil {
			t.Fatal("TRAIN started without its original admitted time")
		}
		start := execution.AcceptedAt.Add(time.Minute)
		plan, err := biz.FreezeTrainingPlan(execution, workspace, start)
		want, _ := execution.Snapshot.Timing.DeadlineFor(start)
		if err != nil || plan.Timing == nil || !plan.Timing.AdmittedAt.Equal(start) || !plan.Timing.DeadlineAt.Equal(want) {
			t.Fatalf("incorrect common TRAIN budget: %+v / %v", plan, err)
		}
		replay, err := biz.FreezeTrainingPlan(execution, workspace, start)
		if err != nil || replay.RequestSHA256 != plan.RequestSHA256 {
			t.Fatal("original timing changed request identity", err)
		}
		renewed, err := biz.FreezeTrainingPlan(execution, workspace, start.Add(time.Second))
		if err != nil || renewed.RequestSHA256 == plan.RequestSHA256 {
			t.Fatal("different timing escaped the request digest", err)
		}
	}
}

func TestObservedRuntimeBindingChangesOnlyInternalRequestIdentity(t *testing.T) {
	e, w := trainingPlanAdmission(t, false)
	plan, err := biz.FreezeTrainingPlan(e, w, e.AcceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	binding := biz.RuntimeTemplateBinding{NamespaceUID: w.NamespaceUID, Name: e.Snapshot.RuntimePlan.Name, UID: "eeeeeeee-ffff-4111-8222-333333333333", ObservedSpecSHA256: e.Snapshot.RuntimePlan.ExpectedSpecSHA256}
	bound, err := biz.BindTrainingRuntime(plan, binding)
	if err != nil || bound.RequestSHA256 == plan.RequestSHA256 || bound.SpecHash != plan.SpecHash {
		t.Fatal("Runtime UID was not separated from frozen spec", err)
	}
	hash, err := bound.Snapshot.Digest()
	if err != nil || hash != e.SpecHash {
		t.Fatal("observed binding rewrote immutable snapshot", err)
	}
	binding.ObservedSpecSHA256 = strings.Repeat("0", 64)
	if _, err := biz.BindTrainingRuntime(plan, binding); err == nil {
		t.Fatal("drifted Runtime was bound")
	}
}
