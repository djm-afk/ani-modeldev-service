package submittest_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestSubmitCancellationAfterRealHTTPObservationStillPersistsOriginalOutcome(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		name := "incomplete HTTP response remains uncertain"
		if confirmed {
			name = "complete HTTP response retains confirmed Run"
		}
		t.Run(name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			callerContext, cancelCaller := context.WithCancel(ctx)
			defer cancelCaller()
			request := dispatchRequest(t)
			request.Admission = admissionfixture.EnsureEnvironment(t, openPool(), request.Admission)
			request.Owner = request.Admission.Snapshot.DispatchOwner
			readerPool := openPool()
			if _, err := execution.New(readerPool).Accept(ctx, request.Admission); err != nil {
				t.Fatalf("CPU07_CANCEL_PREFLIGHT: real Admission unavailable; behavior NOT_RUN: %v", err)
			}

			trace := &canceledObservationWriteTrace{budget: 3 * time.Second}
			config := readerPool.Config()
			config.ConnConfig.Tracer = trace
			writerPool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal("CPU07_CANCEL_PREFLIGHT: traced writer pool unavailable; behavior NOT_RUN")
			}
			t.Cleanup(writerPool.Close)
			if err := writerPool.Ping(ctx); err != nil {
				t.Fatal("CPU07_CANCEL_PREFLIGHT: traced writer connection unavailable; behavior NOT_RUN")
			}
			var posts, tokens atomic.Int32
			visibleAtPOST := make(chan biz.PipelineDispatch, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/apis/v2beta1/runs" {
					t.Error("unexpected KFP operation")
				}
				visible, err := submission.New(readerPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
				if err != nil || visible.State != biz.PipelineDispatchSubmitting {
					t.Errorf("POST preceded independently readable reservation: %v", err)
				}
				select {
				case visibleAtPOST <- visible:
				default:
					t.Error("cancellation caused a repeated POST")
				}
				if confirmed {
					writeConfirmedSubmitResponse(t, w, request)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"run_id":"55555555-6666-4777-8888-999999999999"}`))
			}))
			t.Cleanup(server.Close)
			provider := tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
				tokens.Add(1)
				return "synthetic-submit-token", nil
			})
			certificates := x509.NewCertPool()
			certificates.AddCert(server.Certificate())
			client, err := kfp.New(kfp.Config{ConnectionRef: "kfp-managed-v1", Endpoint: server.URL, RootCAs: certificates, Timeout: 3 * time.Second}, provider)
			if err != nil {
				t.Fatal("CPU07_CANCEL_PREFLIGHT: actual TLS client unavailable; behavior NOT_RUN")
			}
			client.WithMutationIdentity(submitIdentityFixture(t, request.Admission))
			creator := &cancelAfterHTTPObservation{delegate: client, cancel: cancelCaller, trace: trace}
			submitter, err := biz.NewPipelineSubmitter(submission.New(writerPool), creator, trace.budget)
			if err != nil {
				t.Fatal("CPU07_CANCEL_PREFLIGHT: submitter construction failed; behavior NOT_RUN")
			}
			got, submitErr := submitter.Submit(callerContext, request)
			if !errors.Is(callerContext.Err(), context.Canceled) || !creator.returned || !trace.observed.Load() || trace.invalid.Load() {
				t.Fatal("canceled caller did not receive a live, finitely bounded real database write context")
			}
			if got.Observation == nil || !reflect.DeepEqual(*got.Observation, creator.observation) || posts.Load() != 1 || tokens.Load() != 1 {
				t.Fatal("submitter lost the original actual-client observation or repeated the call")
			}
			if confirmed {
				if submitErr != nil || creator.callErr != nil || got.Observation.State != biz.PipelineSubmissionConfirmed || got.Observation.RunID != "55555555-6666-4777-8888-999999999999" || got.Dispatch.State != biz.PipelineDispatchConfirmed || len(got.Dispatch.ConfirmedRuns) != 1 || got.Dispatch.ConfirmedRuns[0].RunID != got.Observation.RunID || got.Dispatch.UncertainAt != nil {
					t.Fatalf("cancellation lost a confirmed original Run: %v", submitErr)
				}
			} else if !errors.Is(submitErr, biz.ErrPipelineSubmissionUncertain) || creator.callErr == nil || got.Observation.State != biz.PipelineSubmissionUncertain || got.Observation.RunID != "" || got.Dispatch.State != biz.PipelineDispatchUncertain || got.Dispatch.UncertainAt == nil || len(got.Dispatch.ConfirmedRuns) != 0 {
				t.Fatalf("cancellation lost original uncertainty or invented a confirmed Run: %v", submitErr)
			}
			var original biz.PipelineDispatch
			select {
			case original = <-visibleAtPOST:
			default:
				t.Fatal("actual TLS POST never observed its committed reservation")
			}
			assertOriginalDispatch(t, got.Dispatch, request)
			if got.Dispatch.AttemptID != original.AttemptID || got.Dispatch.PlanHash != original.PlanHash || !got.Dispatch.ReservedAt.Equal(original.ReservedAt) || !reflect.DeepEqual(got.Dispatch.Plan, original.Plan) || got.Dispatch.NotSentAt != nil {
				t.Fatal("canceled write changed the original reservation")
			}
			writerPool.Close()
			readerPool.Close()
			server.CloseClientConnections()
			reconnectedPool := openPool()
			reconnected := submission.New(reconnectedPool)
			stored, err := reconnected.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil || !reflect.DeepEqual(stored, got.Dispatch) {
				t.Fatalf("canceled-call receipt was not durable across reconnect: %v", err)
			}
			replayed, err := newSubmitter(t, reconnected, server, provider).Submit(ctx, request)
			if err != nil || replayed.Observation != nil || !reflect.DeepEqual(replayed.Dispatch, stored) || posts.Load() != 1 || tokens.Load() != 1 {
				t.Fatalf("reconnect regenerated credentials/POST or changed the original outcome: %v", err)
			}
			admitted, err := execution.New(reconnectedPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
			if err != nil || !reflect.DeepEqual(admitted.Admission, request.Admission) || admitted.Close != nil {
				t.Fatal("cancellation or replay changed original Admission")
			}
			t.Log("CPU07_CANCEL_WRITE PASS: original real TLS observation survived caller cancellation in a bounded PG write and reconnect")
		})
	}
}

// The only injected action is caller cancellation, after the actual client has
// finished observing HTTP. It never manufactures or changes a client result.
type cancelAfterHTTPObservation struct {
	delegate    biz.PipelineRunCreator
	cancel      context.CancelFunc
	trace       *canceledObservationWriteTrace
	observation biz.PipelineSubmissionObservation
	callErr     error
	returned    bool
}

func (creator *cancelAfterHTTPObservation) CreateRun(ctx context.Context, request biz.PipelineCreateRequest) (biz.PipelineSubmissionObservation, error) {
	observation, err := creator.delegate.CreateRun(ctx, request)
	creator.observation, creator.callErr, creator.returned = observation, err, true
	creator.cancel()
	creator.trace.canceled.Store(true)
	return observation, err
}

// This instrument records only context-liveness/deadline booleans at actual PG
// calls after cancellation. It records no query text, parameters or driver errors.
type canceledObservationWriteTrace struct {
	budget   time.Duration
	canceled atomic.Bool
	observed atomic.Bool
	invalid  atomic.Bool
}

func (trace *canceledObservationWriteTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	if trace.canceled.Load() {
		trace.observed.Store(true)
		deadline, bounded := ctx.Deadline()
		remaining := time.Until(deadline)
		if ctx.Err() != nil || !bounded || remaining <= 0 || remaining > trace.budget {
			trace.invalid.Store(true)
		}
	}
	return ctx
}

func (*canceledObservationWriteTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

// Lifecycle regressions share one fixed response, independent of the production
// request encoder. The separate existing wire test checks the full POST contract.
func writeConfirmedSubmitResponse(t testing.TB, w http.ResponseWriter, request biz.PipelineDispatchRequest) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{
		"run_id":                     "55555555-6666-4777-8888-999999999999",
		"experiment_id":              request.Admission.Snapshot.Environment.ExperimentID,
		"display_name":               "md-" + request.Admission.ExecutionID,
		"pipeline_version_reference": map[string]string{"pipeline_id": request.Admission.Snapshot.Implementation.PipelineID, "pipeline_version_id": request.Admission.Snapshot.Implementation.PipelineVersionID},
		"runtime_config":             map[string]any{"parameters": map[string]string{"execution_id": request.Admission.ExecutionID, "spec_hash": request.Admission.SpecHash, "environment_binding_id": request.Admission.Snapshot.Environment.BindingID, "namespace_uid": request.Admission.Snapshot.Environment.NamespaceUID}, "pipeline_root": request.Owner.PipelineRoot},
		"service_account":            request.Admission.Snapshot.Environment.Identities.KFPStepServiceAccount, "state": "PENDING", "created_at": "2026-09-30T09:00:00Z",
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		t.Error("CPU07_SUBMIT_FIXTURE: could not send fixed complete HTTP response")
	}
}
