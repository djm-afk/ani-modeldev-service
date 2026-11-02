package submittest_test

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestSubmitConfirmedResponseRetainsRunAcrossCloseAndRecordingFailure(t *testing.T) {
	for _, test := range []struct {
		name                string
		closeBeforeResponse bool
		loseWriter          bool
	}{
		{name: "confirmation commits before receipt"},
		{name: "close during POST retains late Run", closeBeforeResponse: true},
		{name: "recording unavailable retains transient Run without ACK", loseWriter: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			request := dispatchRequest(t)
			request.Admission = admissionfixture.EnsureEnvironment(t, openPool(), request.Admission)
			request.Owner = request.Admission.Snapshot.DispatchOwner
			readerPool, writerPool := openPool(), openPool()
			if _, err := execution.New(readerPool).Accept(ctx, request.Admission); err != nil {
				t.Fatalf("CPU07_CONFIRM_PREFLIGHT: Admission unavailable; behavior NOT_RUN: %v", err)
			}
			var posts, tokens atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/apis/v2beta1/runs" {
					t.Error("unexpected KFP operation")
				}
				visible, err := submission.New(readerPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
				if err != nil || visible.State != biz.PipelineDispatchSubmitting {
					t.Errorf("POST preceded readable reservation: %v", err)
				}
				if test.closeBeforeResponse {
					_, err := execution.New(readerPool).ApplyCloseIntent(ctx, biz.CloseIntent{
						TenantID: request.Admission.TenantID, ExecutionID: request.Admission.ExecutionID,
						OperationID: request.Admission.OperationID, SpecHash: request.Admission.SpecHash,
						SourceGeneration: 1, Reason: biz.CloseReasonUserStop,
						RequestedAt: request.Admission.AcceptedAt.Add(time.Microsecond), RequestedActor: "governance:user:7",
					})
					if err != nil {
						t.Errorf("actual close during POST failed: %v", err)
					}
				}
				if test.loseWriter {
					// This closes a real pool after its reservation committed. No
					// fake repository hides the subsequent recording failure.
					writerPool.Close()
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(frozenRunLiteral(request, `{
  "run_id":"55555555-6666-4777-8888-999999999999",
  "experiment_id":"44444444-4444-4444-8444-444444444444",
  "display_name":"md-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
  "pipeline_version_reference":{"pipeline_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","pipeline_version_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"},
  "runtime_config":{"parameters":{"execution_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","spec_hash":"FIXTURE_SPEC_HASH"},"pipeline_root":"s3://fixture-kfp-artifacts/frozen-submit-root"},
  "service_account":"cpu-managed-step","state":"PENDING","created_at":"2026-09-30T09:00:00Z"
}`)))
			}))
			t.Cleanup(server.Close)
			provider := tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
				tokens.Add(1)
				return "synthetic-submit-token", nil
			})
			submitter := newSubmitter(t, submission.New(writerPool), server, provider)
			t.Log("CPU07_CONFIRM_PREFLIGHT PASS: real Admission, independent PG pools and TLS client")
			got, err := submitter.Submit(ctx, request)
			if got.Observation == nil || got.Observation.State != biz.PipelineSubmissionConfirmed || got.Observation.RunID != "55555555-6666-4777-8888-999999999999" {
				t.Fatalf("CPU07_CONFIRM_BEHAVIOR: complete response lost its observed Run: %v", err)
			}
			stored, readErr := submission.New(readerPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if readErr != nil || !reflect.DeepEqual(got.Dispatch, stored) {
				t.Fatalf("receipt differs from independently read committed facts: %v", readErr)
			}
			assertOriginalDispatch(t, stored, request)
			if test.loseWriter {
				if !errors.Is(err, biz.ErrPersistence) || stored.State != biz.PipelineDispatchSubmitting || len(stored.ConfirmedRuns) != 0 || stored.UncertainAt != nil {
					t.Fatal("recording failure falsely acknowledged confirmation or changed original reservation")
				}
			} else if err != nil || stored.State != biz.PipelineDispatchConfirmed || len(stored.ConfirmedRuns) != 1 || stored.ConfirmedRuns[0].RunID != got.Observation.RunID || stored.ConfirmedRuns[0].FirstObservedAt.Before(stored.ReservedAt) {
				t.Fatalf("confirmation was not durable before receipt: %v", err)
			}
			writerPool.Close()
			replayPool := openPool()
			replayed, err := newSubmitter(t, submission.New(replayPool), server, provider).Submit(ctx, request)
			if err != nil || replayed.Observation != nil || !reflect.DeepEqual(replayed.Dispatch, stored) || posts.Load() != 1 || tokens.Load() != 1 {
				t.Fatalf("reconnect repeated POST/credentials or changed durable facts: %v", err)
			}
			admitted, err := execution.New(replayPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil || !reflect.DeepEqual(admitted.Admission, request.Admission) || (admitted.Close != nil) != test.closeBeforeResponse {
				t.Fatal("confirmation or replay changed Admission/close facts")
			}
		})
	}
}
