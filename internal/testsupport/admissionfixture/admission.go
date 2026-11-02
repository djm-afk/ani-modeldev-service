// Package admissionfixture binds authored test facts to the single active
// admission envelope. It supplies no transport, authorization or persistence.
package admissionfixture

import (
	"github.com/google/uuid"
	"net/url"
	"path"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// Bind is used only when constructing a valid fixture, never when asserting
// rejection of a deliberately incoherent admission.
func Bind(t testing.TB, a biz.Admission) biz.Admission {
	t.Helper()
	_, hash, err := cpup01.CanonicalIntent(a.Intent)
	if err != nil {
		t.Fatal("fixture intent", err)
	}
	a.IntentHash = hash
	a.Snapshot.IdentityRef = cpup01.IdentityRef{TenantID: a.TenantID, Actor: a.Actor, OperationID: a.OperationID, ExecutionID: a.ExecutionID, IntentHash: hash}
	if a.Snapshot.Environment.NamespaceName != "tenant-"+a.TenantID {
		a.Snapshot.Environment.BindingID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("environment/"+a.TenantID)).String()
		a.Snapshot.Environment.NamespaceUID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("namespace/"+a.TenantID)).String()
		a.Snapshot.Environment.ExperimentID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("experiment/"+a.TenantID)).String()
	}
	a.Snapshot.Environment.NamespaceName = "tenant-" + a.TenantID
	root, err := url.Parse(a.Snapshot.DispatchOwner.PipelineRoot)
	if err != nil || root.Host == "" {
		t.Fatal("fixture requires artifact bucket")
	}
	root.Path = "/artifacts/" + a.TenantID + "/" + a.ExecutionID
	a.Snapshot.DispatchOwner.PipelineRoot = root.String()
	a.Snapshot.PublicationScope.ApprovedPrefix = "executions/" + a.TenantID
	a.Snapshot.Input.Object.Key = "inputs/" + a.TenantID + "/" + path.Base(a.Snapshot.Input.Object.Key)
	if a.Snapshot.Finetuning != nil {
		a.Snapshot.Finetuning.BaseModel.Scope.ApprovedPrefix = a.TenantID
		a.Snapshot.Finetuning.BaseModel.Object.Key = a.TenantID + "/" + path.Base(a.Snapshot.Finetuning.BaseModel.Object.Key)
	}
	seconds := uint64((a.Snapshot.DeadlineAt.Sub(a.AcceptedAt) + time.Second - 1) / time.Second)
	if seconds == 0 {
		t.Fatal("fixture requires positive execution lifetime")
	}
	step := a.Snapshot.Timing.StepTimeoutSeconds
	if step == 0 {
		step = 900
	}
	maximum := a.Snapshot.Timing.ExecutionTimeoutMaxSeconds
	if maximum < seconds {
		maximum = seconds
	}
	configuration := cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: seconds, ExecutionTimeoutMaxSeconds: maximum, StepTimeoutSeconds: step}
	timing, err := configuration.Resolve(a.AcceptedAt, a.Intent.TimeoutSeconds)
	if err != nil {
		t.Fatal("fixture timing", err)
	}
	a.Snapshot.Timing, a.Snapshot.DeadlineAt = timing, timing.ExecutionDeadlineAt
	a.Snapshot.RuntimePlan, err = biz.CompileRuntimePlan(a.Snapshot)
	if err != nil {
		t.Fatal("fixture runtime", err)
	}
	a.SpecHash, err = a.Snapshot.Digest()
	if err != nil {
		t.Fatal("fixture snapshot", err)
	}
	raw, err := a.Snapshot.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshot, err = cpup01.DecodeCanonicalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
