package resolutiontest

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	envrepo "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TM02: current timing is frozen in the published dynamic environment. The
// command travels over the existing verified Governance mTLS seam and commits
// through the actual restricted PostgreSQL inbox; no acceptance is substituted.
func TestAcceptedTimingSurvivesEnvironmentConfigDriftAndNewAdmissionStops(t *testing.T) {
	f := prepareResolution(t)
	if f.request.Intent.TimeoutSeconds != nil {
		t.Fatal("this regression must exercise the configured default")
	}
	connection := startAdmissionConnection(t, f, service.NewAdmission(f.resolver))
	admissionClient := modeldevv1.NewModelDevAdmissionServiceClient(connection)
	commandClient := modeldevv1.NewModelDevCommandServiceClient(connection)
	ctx := admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID)
	prepared, err := admissionClient.PrepareAdmission(ctx, admissionRPCRequest(t, f))
	if err != nil {
		t.Fatal("original Prepare", err)
	}
	finalize := &modeldevv1.FinalizeAdmissionRequest{CandidateCanonical: prepared.CandidateCanonical, CandidateHash: prepared.CandidateHash}
	resolved, err := admissionClient.FinalizeAdmission(ctx, finalize)
	if err != nil {
		t.Fatal("original Finalize", err)
	}
	original, err := contractpb.DecodeSnapshot(resolved.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	command := timingRevisionCommand(t, f.request, original, resolved.ExecutionSpecHash)
	first, err := commandClient.AcceptExecution(ctx, command)
	if err != nil || first.Replayed || first.Revision == 0 {
		t.Fatal("formal original Accept did not persist", err)
	}
	beforeIntent, beforeSnapshot := timingRevisionStoredBytes(t, f)
	oldTiming := original.Timing
	if oldTiming.ExecutionTimeoutSeconds != 3600 || oldTiming.ExecutionTimeoutMaxSeconds != 7200 || oldTiming.StepTimeoutSeconds != 900 {
		t.Fatal("original concrete timing did not come from old configuration", oldTiming)
	}
	if timingRevisionJobDeadline(t, original) != 900 {
		t.Fatal("old accepted Runtime Job did not retain its original S900")
	}

	// A changed current platform configuration must not replace this tenant's
	// fixed provision specification. Ensure records NEEDS_REVIEW; both fresh
	// admission and Finalize must stop, while an already accepted original stays.
	revisedConfig := f.environment.Config
	revisedConfig.Timing = cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 14400, ExecutionTimeoutMaxSeconds: 14400, StepTimeoutSeconds: 14400}
	revisedPlatform, err := envrepo.NewPlatform(revisedConfig, f.environment.Kube, fixtureImages{})
	if err != nil {
		t.Fatal(err)
	}
	environments, err := biz.NewTenantEnvironment(envrepo.NewRepository(f.openPool()), revisedPlatform, revisedConfig.ClusterID, revisedConfig.KFPConnectionRef)
	if err != nil {
		t.Fatal(err)
	}
	row, err := environments.Ensure(f.ctx, f.request.IdentityRef.TenantID, f.request.IdentityRef.Actor, uuid.NewString())
	if err != nil || row.State != biz.EnvironmentNeedsReview || row.ReasonCode != "ENVIRONMENT_CONFIG_DRIFT" {
		t.Fatal("changed configuration replaced environment authority", row.State, row.ReasonCode, err)
	}
	again, err := admissionClient.FinalizeAdmission(ctx, finalize)
	if again != nil || status.Code(err) != codes.Unavailable {
		t.Fatal("Finalize ignored current environment review", err)
	}
	replay, err := commandClient.AcceptExecution(ctx, command)
	if err != nil || !replay.Replayed || replay.Identity.ExecutionId != f.request.IdentityRef.ExecutionID || replay.Revision != first.Revision {
		t.Fatal("formal original replay changed acceptance", err)
	}
	stored, err := execution.New(f.openPool()).Get(f.ctx, f.request.IdentityRef.TenantID, f.request.IdentityRef.ExecutionID)
	if err != nil || stored.SpecHash != resolved.ExecutionSpecHash || !reflect.DeepEqual(stored.Snapshot.Timing, oldTiming) {
		t.Fatal("independent Get adopted current timing", err)
	}
	afterIntent, afterSnapshot := timingRevisionStoredBytes(t, f)
	if !bytes.Equal(beforeIntent, afterIntent) || !bytes.Equal(beforeSnapshot, afterSnapshot) {
		t.Fatal("configuration change/replay replaced original canonical database bytes")
	}

	// A new identity cannot silently adopt the changed timing on this binding.
	newFixture := f
	newFixture.request.IdentityRef.ExecutionID = uuid.NewString()
	newFixture.request.IdentityRef.OperationID = uuid.NewString()
	newFixture.request.AcceptedAt = f.request.AcceptedAt.Add(time.Second)
	next, err := admissionClient.PrepareAdmission(ctx, admissionRPCRequest(t, newFixture))
	if next != nil || status.Code(err) != codes.Unavailable {
		t.Fatal("new Prepare bypassed environment configuration review", err)
	}
	afterIntent, afterSnapshot = timingRevisionStoredBytes(t, f)
	if !bytes.Equal(beforeIntent, afterIntent) || !bytes.Equal(beforeSnapshot, afterSnapshot) {
		t.Fatal("new max-budget acceptance changed original accepted canonical bytes")
	}
	t.Log("TM02 FEDORA: accepted T3600/M7200/S900 and original canonical bytes retained through config drift, replay and Get; READY binding was not rewritten; subsequent Prepare/Finalize refused NEEDS_REVIEW; no long-running live task inferred")
}

func timingRevisionJobDeadline(t *testing.T, snapshot cpup01.Snapshot) uint64 {
	t.Helper()
	var expected struct {
		Template struct {
			Spec struct {
				Jobs []struct {
					Template struct {
						Spec struct {
							Deadline uint64 `json:"activeDeadlineSeconds"`
						} `json:"spec"`
					} `json:"template"`
				} `json:"replicatedJobs"`
			} `json:"spec"`
		} `json:"template"`
	}
	if err := json.Unmarshal(snapshot.RuntimePlan.ExpectedSpec, &expected); err != nil || len(expected.Template.Spec.Jobs) != 1 {
		t.Fatal("expected exactly one compiled Runtime Job", err)
	}
	return expected.Template.Spec.Jobs[0].Template.Spec.Deadline
}

func timingRevisionCommand(t *testing.T, request biz.PrepareAdmissionInput, snapshot cpup01.Snapshot, hash string) *modeldevv1.AcceptExecutionRequest {
	t.Helper()
	intent, err := contractpb.EncodeIntent(request.Intent)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := contractpb.EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	id := request.IdentityRef
	return &modeldevv1.AcceptExecutionRequest{Identity: &trainingv1.ExecutionIdentity{ExecutionId: id.ExecutionID, OperationId: id.OperationID, ExecutionSpecHash: hash}, ResourceTenantId: id.TenantID, AdmittedActorId: id.Actor, IntentHash: id.IntentHash, Snapshot: wire, Intent: intent, AcceptedAt: timestamppb.New(request.AcceptedAt)}
}

func timingRevisionStoredBytes(t *testing.T, f resolutionFixture) ([]byte, []byte) {
	t.Helper()
	var intent, snapshot []byte
	err := f.openPool().QueryRow(f.ctx, `SELECT intent_canonical,snapshot_canonical FROM modeldev_executions WHERE tenant_id=$1::uuid AND execution_id=$2::uuid`, f.request.IdentityRef.TenantID, f.request.IdentityRef.ExecutionID).Scan(&intent, &snapshot)
	if err != nil {
		t.Fatal("restricted original canonical read", err)
	}
	return intent, snapshot
}
