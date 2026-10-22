package biz_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func trainingPlanAdmission(t *testing.T, fine bool) (biz.Execution, biz.WorkspaceBinding) {
	t.Helper()
	snapshot, intent := conformance.SnapshotV1(), conformance.IntentV1()
	if fine {
		snapshot, intent = conformance.FinetuningSnapshotV1(), conformance.FinetuningIntentV1()
	} else {
		intent = cpup01.Intent{Name: "training-plan-fixture", Kind: "GENERAL_TRAINING", PresetID: snapshot.Release.PresetID, DatasetVersionID: snapshot.Input.InputVersionID}
	}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	specHash, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	execution := biz.Execution{Admission: biz.Admission{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:synthetic-fixture", OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: snapshot.DeadlineAt.Add(-3 * time.Hour)}}
	w := snapshot.Workspace
	workspace := biz.WorkspaceBinding{Mode: w.Mode, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, PVCName: "training-plan-fixture", PVCUID: "dddddddd-eeee-4fff-8111-222222222222", InputSubpath: w.InputSubpath, TrainingSubpath: w.TrainingSubpath, ReportsSubpath: w.ReportsSubpath, PublicationSubpath: w.PublicationSubpath, PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256}
	return execution, workspace
}

func TestFinetuningTrainingPlanNeedsOneOriginalAdmittedTime(t *testing.T) {
	execution, workspace := trainingPlanAdmission(t, true)
	if _, err := biz.FreezeTrainingPlan(execution, workspace); err == nil {
		t.Fatal("TRAIN started without an admitted database time")
	}
	start := execution.Snapshot.DeadlineAt.Add(-2 * time.Hour)
	plan, err := biz.FreezeTrainingPlan(execution, workspace, start)
	if err != nil || plan.FinetuningTiming == nil || !plan.FinetuningTiming.AdmittedAt.Equal(start) || !plan.FinetuningTiming.DeadlineAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("incorrect shared TRAIN budget: %+v / %v", plan, err)
	}
	replayed, err := biz.FreezeTrainingPlan(execution, workspace, plan.FinetuningTiming.AdmittedAt)
	if err != nil || replayed.RequestSHA256 != plan.RequestSHA256 {
		t.Fatal("original admission did not recreate the immutable lookup shape", err)
	}
	late, err := biz.FreezeTrainingPlan(execution, workspace, start.Add(time.Minute))
	if err != nil || late.RequestSHA256 == plan.RequestSHA256 {
		t.Fatal("renewed timing did not change the request identity", err)
	}
}

func TestCPUTrainingPlanOptionalTimingPreservesHistoricalPayloadAndHash(t *testing.T) {
	execution, workspace := trainingPlanAdmission(t, false)
	plan, err := biz.FreezeTrainingPlan(execution, workspace)
	if err != nil || plan.FinetuningTiming != nil {
		t.Fatal(err)
	}
	// Encode the exact pre-feature struct, rather than pinning a newly generated
	// hash that could silently bless an accidental CPU serialization change.
	legacy := struct {
		TenantID, OperationID, ExecutionID, SpecHash string
		Name, RequestSHA256                          string
		Snapshot                                     cpup01.Snapshot
		Workspace                                    biz.WorkspaceBinding
	}{plan.TenantID, plan.OperationID, plan.ExecutionID, plan.SpecHash, plan.Name, "", plan.Snapshot, plan.Workspace}
	bytes, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(bytes)
	if plan.RequestSHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("new optional timing changed the legacy CPU request hash")
	}
	replayed, err := biz.FreezeTrainingPlan(execution, workspace, execution.AcceptedAt)
	if err != nil || replayed.RequestSHA256 != plan.RequestSHA256 || replayed.FinetuningTiming != nil {
		t.Fatal("repository clock changed a CPU plan", err)
	}
}
