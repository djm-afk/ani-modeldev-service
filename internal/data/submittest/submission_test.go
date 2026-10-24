package submittest_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestSubmitLostResponsePersistsUncertaintyWithoutResendingAfterReconnect(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := dispatchRequest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admissionPool := openPool()
	if _, err := execution.New(admissionPool).Accept(ctx, request.Admission); err != nil {
		t.Fatalf("CPU07_SUBMIT_PREFLIGHT: real Admission setup failed; behavior NOT_RUN: %v", err)
	}
	admissionPool.Close()

	readerPool := openPool()
	reader := submission.New(readerPool)
	visibleAtPOST := make(chan biz.PipelineDispatch, 2)
	var calls, tokenCalls atomic.Int32
	// Only the external KFP endpoint is synthetic. The two submitters use real
	// repositories and the actual TLS HTTP adapter, not fake sender callbacks.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/apis/v2beta1/runs" || r.URL.RawQuery != "" {
			t.Error("CPU07_SUBMIT_BEHAVIOR: unexpected KFP operation")
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-submit-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("CPU07_SUBMIT_BEHAVIOR: missing managed credential or JSON header")
		}
		for _, header := range []string{"Idempotency-Key", "X-Idempotency-Key", "Cookie", "Kubeflow-Userid"} {
			if len(r.Header.Values(header)) != 0 {
				t.Errorf("CPU07_SUBMIT_BEHAVIOR: unexpected forwarded/retry header %s", header)
			}
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			t.Error("CPU07_SUBMIT_BEHAVIOR: cannot read fixture request")
		}
		var actual, expected map[string]any
		// Independent wire literal; only the input's spec hash varies because
		// this real reservation needs an unexpired fixture deadline. No sender
		// encoder, frozen-plan encoder or captured request builds expectations.
		expectedJSON := strings.ReplaceAll(`{
  "experiment_id":"44444444-4444-4444-8444-444444444444",
  "display_name":"md-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
  "pipeline_version_reference":{
    "pipeline_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
    "pipeline_version_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"
  },
  "runtime_config":{
    "parameters":{"execution_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","spec_hash":"FIXTURE_SPEC_HASH"},
    "pipeline_root":"s3://fixture-kfp-artifacts/frozen-submit-root"
  },
  "service_account":"cpu-managed-step"
}`, "FIXTURE_SPEC_HASH", request.Admission.SpecHash)
		if json.Unmarshal(body, &actual) != nil || json.Unmarshal([]byte(expectedJSON), &expected) != nil || !reflect.DeepEqual(actual, expected) {
			t.Error("CPU07_SUBMIT_BEHAVIOR: POST differs from the original frozen execution/root")
		}
		readContext, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer readCancel()
		visible, err := reader.Get(readContext, request.Admission.TenantID, request.Admission.ExecutionID)
		if err != nil {
			t.Errorf("CPU07_SUBMIT_BEHAVIOR: POST preceded independently readable reservation: %v", err)
		} else {
			assertOriginalDispatch(t, visible, request)
			if visible.State != biz.PipelineDispatchSubmitting || visible.UncertainAt != nil || len(visible.ConfirmedRuns) != 0 {
				t.Error("CPU07_SUBMIT_BEHAVIOR: first POST did not observe committed SUBMITTING")
			}
			select {
			case visibleAtPOST <- visible:
			default:
				t.Error("CPU07_SUBMIT_BEHAVIOR: repeated POST exceeded observation capacity")
			}
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error("CPU07_SUBMIT_FIXTURE: disconnect unavailable; behavior NOT_RUN")
			return
		}
		_ = connection.Close()
	}))
	t.Cleanup(server.Close)
	provider := tokenProviderFunc(func(_ context.Context, tenant string, binding cpup01.EnvironmentBindingSnapshot) (string, error) {
		tokenCalls.Add(1)
		if tenant != request.Admission.TenantID || binding != request.Admission.Snapshot.Environment {
			t.Error("CPU07_SUBMIT_BEHAVIOR: credentials did not use the original tenant/environment")
		}
		return "synthetic-submit-token", nil
	})
	firstPool, secondPool := openPool(), openPool()
	submitters := []*biz.PipelineSubmitter{
		newSubmitter(t, submission.New(firstPool), server, provider),
		newSubmitter(t, submission.New(secondPool), server, provider),
	}
	t.Log("CPU07_SUBMIT_PREFLIGHT PASS: real Admission committed, independent PG pools and TLS KFP clients ready")
	type result struct {
		value biz.PipelineSubmitResult
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, len(submitters))
	for _, submitter := range submitters {
		go func(submitter *biz.PipelineSubmitter) {
			<-start
			value, err := submitter.Submit(ctx, request)
			results <- result{value: value, err: err}
		}(submitter)
	}
	close(start)
	completed := make([]result, 0, len(submitters))
	for range submitters {
		select {
		case got := <-results:
			completed = append(completed, got)
		case <-ctx.Done():
			t.Fatal("CPU07_SUBMIT_BEHAVIOR: two real submitters did not finish within the bound")
		}
	}
	observations := 0
	for _, got := range completed {
		if got.value.Observation != nil {
			observations++
			if !errors.Is(got.err, biz.ErrPipelineSubmissionUncertain) || got.value.Observation.State != biz.PipelineSubmissionUncertain || got.value.Observation.RunID != "" || got.value.Dispatch.State != biz.PipelineDispatchUncertain {
				t.Fatalf("CPU07_SUBMIT_BEHAVIOR: lost response was not durably uncertain: %+v, %v", got.value, got.err)
			}
		} else if got.err != nil {
			t.Fatalf("CPU07_SUBMIT_BEHAVIOR: valid admitted submission failed before an observation: %v", got.err)
		}
		assertOriginalDispatch(t, got.value.Dispatch, request)
	}
	if observations != 1 || calls.Load() != 1 || tokenCalls.Load() != 1 {
		t.Fatalf("CPU07_SUBMIT_BEHAVIOR: want one observation/POST/credential generation, got %d/%d/%d", observations, calls.Load(), tokenCalls.Load())
	}
	var original biz.PipelineDispatch
	select {
	case original = <-visibleAtPOST:
	default:
		t.Fatal("CPU07_SUBMIT_BEHAVIOR: TLS handler never observed the committed reservation")
	}
	for _, got := range completed {
		if got.value.Dispatch.AttemptID != original.AttemptID || got.value.Dispatch.PlanHash != original.PlanHash || !got.value.Dispatch.ReservedAt.Equal(original.ReservedAt) {
			t.Fatal("CPU07_SUBMIT_BEHAVIOR: competing submitters changed the original reservation")
		}
	}
	firstPool.Close()
	secondPool.Close()
	readerPool.Close()
	server.CloseClientConnections()

	// Rebuild every caller/repository/client and connection. This is durable
	// reconnect evidence, not a claim that OS process-crash recovery was tested.
	restartedPool := openPool()
	restartedRepository := submission.New(restartedPool)
	stored, err := restartedRepository.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || stored.State != biz.PipelineDispatchUncertain || stored.UncertainAt == nil {
		t.Fatalf("CPU07_SUBMIT_BEHAVIOR: reconnect lost uncertainty: %+v, %v", stored, err)
	}
	assertOriginalDispatch(t, stored, request)
	if stored.AttemptID != original.AttemptID || stored.PlanHash != original.PlanHash || !stored.ReservedAt.Equal(original.ReservedAt) || !reflect.DeepEqual(stored.Plan, original.Plan) || len(stored.ConfirmedRuns) != 0 || stored.UncertainAt.Before(stored.ReservedAt) || stored.UncertainAt.Nanosecond()%1000 != 0 {
		t.Fatal("CPU07_SUBMIT_BEHAVIOR: reconnect changed reservation or observation facts")
	}
	replayed, err := newSubmitter(t, restartedRepository, server, provider).Submit(ctx, request)
	if err != nil || replayed.Observation != nil || !reflect.DeepEqual(replayed.Dispatch, stored) || calls.Load() != 1 || tokenCalls.Load() != 1 {
		t.Fatalf("CPU07_SUBMIT_BEHAVIOR: reconnect replay changed facts or repeated a send: %v", err)
	}
	admitted, err := execution.New(restartedPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(admitted.Admission, request.Admission) || admitted.Close != nil {
		t.Fatal("CPU07_SUBMIT_BEHAVIOR: submission changed the original Admission")
	}
}

func dispatchRequest(t *testing.T) biz.PipelineDispatchRequest {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	snapshot.DeadlineAt = acceptedAt.Add(time.Hour)
	intent := cpup01.Intent{Name: "submit-integration-fixture", Kind: "GENERAL_TRAINING", ImplementationID: snapshot.Implementation.ImplementationID, ComputePresetID: snapshot.ComputeOrigin.ComputePresetID, MLP: &cpup01.MLPParameters{}, DatasetVersionID: snapshot.Input.InputVersionID}
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal("CPU07_SUBMIT_PREFLIGHT: invalid intent fixture; behavior NOT_RUN")
	}
	specHash := ""
	return biz.PipelineDispatchRequest{
		Admission: admissionfixture.Bind(t, biz.Admission{
			TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:synthetic-fixture",
			OperationID: "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", ExecutionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
			Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: acceptedAt,
		}),
		Owner: biz.PipelineOwnerConfiguration{Reference: "cpu07-submit-owner", RevisionSHA256: strings.Repeat("a", 64), PipelineRoot: "s3://fixture-kfp-artifacts/frozen-submit-root"},
	}
}

func assertOriginalDispatch(t *testing.T, dispatch biz.PipelineDispatch, request biz.PipelineDispatchRequest) {
	t.Helper()
	plan := dispatch.Plan
	if dispatch.AttemptID == "" || len(dispatch.PlanHash) != 64 || dispatch.ReservedAt.IsZero() ||
		plan.TenantID != request.Admission.TenantID || plan.ExecutionID != request.Admission.ExecutionID || plan.OperationID != request.Admission.OperationID || plan.SpecHash != request.Admission.SpecHash ||
		plan.Owner != request.Owner || plan.Environment != request.Admission.Snapshot.Environment || plan.PipelineID != request.Admission.Snapshot.Implementation.PipelineID || plan.PipelineVersionID != request.Admission.Snapshot.Implementation.PipelineVersionID ||
		plan.DisplayName != "md-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" || !plan.DeadlineAt.Equal(request.Admission.Snapshot.DeadlineAt) {
		t.Error("CPU07_SUBMIT_BEHAVIOR: dispatch does not preserve the original full plan")
	}
}

type tokenProviderFunc func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error)

func (provider tokenProviderFunc) BearerToken(ctx context.Context, tenant string, binding cpup01.EnvironmentBindingSnapshot) (string, error) {
	return provider(ctx, tenant, binding)
}

func newSubmitter(t *testing.T, repository *submission.Repository, server *httptest.Server, provider kfp.TokenProvider) *biz.PipelineSubmitter {
	t.Helper()
	certificates := x509.NewCertPool()
	certificates.AddCert(server.Certificate())
	client, err := kfp.New(kfp.Config{ConnectionRef: "kfp-managed-v1", Endpoint: server.URL, RootCAs: certificates, Timeout: 3 * time.Second}, provider)
	if err != nil {
		t.Fatal("CPU07_SUBMIT_PREFLIGHT: fixture client construction failed; behavior NOT_RUN")
	}
	submitter, err := biz.NewPipelineSubmitter(repository, client, 3*time.Second)
	if err != nil {
		t.Fatal("CPU07_SUBMIT_PREFLIGHT: submitter construction failed; behavior NOT_RUN")
	}
	return submitter
}
