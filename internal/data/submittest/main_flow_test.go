package submittest_test

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// This is the submission-to-authority portion of the primary flow. The real
// submitter, HTTP client and PostgreSQL repositories run together. Only KFP and
// its credential/association observations are external-system substitutes.
// It is not a complete training flow or proof of authenticated managed steps.
func TestMainFlowSubmittedRunBindsAuthorityWithoutResubmission(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := dispatchRequest(t)
	request.Admission = admissionfixture.EnsureEnvironment(t, openPool(), request.Admission)
	request.Owner = request.Admission.Snapshot.DispatchOwner
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	writerPool, readerPool := openPool(), openPool()
	admissions, repository := execution.New(writerPool), submission.New(writerPool)
	if _, err := admissions.Accept(ctx, request.Admission); err != nil {
		t.Fatalf("main flow could not persist admission: %v", err)
	}
	t.Log("MAIN_FLOW: admission committed")
	var posts atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/apis/v2beta1/runs" || r.Header.Get("Authorization") != "Bearer synthetic-main-flow-token" {
			t.Error("main flow used an unexpected KFP operation or identity")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		stored, err := submission.New(readerPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
		if err != nil || stored.State != biz.PipelineDispatchSubmitting {
			t.Error("main flow sent CreateRun before the reservation committed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(frozenRunLiteral(request, `{
  "run_id":"55555555-6666-4777-8888-999999999999",
  "experiment_id":"44444444-4444-4444-8444-444444444444",
  "display_name":"md-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
  "pipeline_version_reference":{"pipeline_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","pipeline_version_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"},
  "runtime_config":{"parameters":{"execution_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","spec_hash":"FIXTURE_SPEC_HASH"},"pipeline_root":"s3://fixture-kfp-artifacts/frozen-submit-root"},
  "service_account":"cpu-managed-step","state":"PENDING"
}`)))
	}))
	t.Cleanup(peer.Close)
	provider := tokenProviderFunc(func(_ context.Context, tenant string, environment cpup01.EnvironmentBindingSnapshot) (string, error) {
		if tenant != request.Admission.TenantID || environment != request.Admission.Snapshot.Environment {
			t.Error("main flow requested credentials outside the frozen tenant environment")
		}
		return "synthetic-main-flow-token", nil
	})
	result, err := newSubmitter(t, repository, peer, provider).Submit(ctx, request)
	if err != nil || result.Observation == nil || result.Observation.RunID != "55555555-6666-4777-8888-999999999999" || result.Dispatch.State != biz.PipelineDispatchConfirmed || posts.Load() != 1 {
		t.Fatalf("main flow did not persist the single CreateRun response: %v", err)
	}
	t.Log("MAIN_FLOW: one CreateRun sent through TLS and Run observation committed")
	candidate := biz.RunAuthorityCandidate{
		TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID,
		OperationID: request.Admission.OperationID, SpecHash: request.Admission.SpecHash,
		AttemptID: result.Dispatch.AttemptID, PlanHash: result.Dispatch.PlanHash,
		RunID: result.Observation.RunID, NamespaceName: request.Admission.Snapshot.Environment.NamespaceName,
		NamespaceUID: request.Admission.Snapshot.Environment.NamespaceUID,
		WorkflowName: "main-flow-workflow", WorkflowUID: "cccccccc-dddd-4eee-8fff-111111111111",
	}
	bound, err := repository.BindRunAuthority(ctx, candidate)
	if err != nil || bound.Replayed || bound.RunAuthorityCandidate != candidate {
		t.Fatalf("main flow could not retain the first eligible authority: %v", err)
	}
	writerPool.Close()
	restarted := submission.New(openPool())
	replay, err := newSubmitter(t, restarted, peer, provider).Submit(ctx, request)
	if err != nil || replay.Observation != nil || posts.Load() != 1 {
		t.Fatalf("main flow restart repeated CreateRun: %v", err)
	}
	winner, err := restarted.GetRunAuthority(ctx, candidate.TenantID, candidate.ExecutionID)
	if err != nil || winner.RunAuthorityCandidate != candidate || !winner.BoundAt.Equal(bound.BoundAt) || winner.OwnerRevision != bound.OwnerRevision {
		t.Fatalf("main flow restart lost the authoritative Run: %v", err)
	}
	t.Log("MAIN_FLOW: authority committed and recovered without another CreateRun")
}
