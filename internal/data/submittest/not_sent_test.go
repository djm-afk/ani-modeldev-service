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

func TestSubmitProviderDenialPersistsNotSentWithoutRetryAfterReconnect(t *testing.T) {
	openPool := postgres.Prepare(t)
	request := dispatchRequest(t)
	request.Admission = admissionfixture.EnsureEnvironment(t, openPool(), request.Admission)
	request.Owner = request.Admission.Snapshot.DispatchOwner
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	readerPool, writerPool := openPool(), openPool()
	if _, err := execution.New(readerPool).Accept(ctx, request.Admission); err != nil {
		t.Fatalf("CPU07_NOT_SENT_PREFLIGHT: real Admission failed; behavior NOT_RUN: %v", err)
	}
	reader := submission.New(readerPool)
	var posts, tokens atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		posts.Add(1)
		t.Error("CPU07_NOT_SENT_BEHAVIOR: denied credentials reached HTTP")
	}))
	t.Cleanup(server.Close)
	reservedBeforeDenial := make(chan biz.PipelineDispatch, 1)
	provider := tokenProviderFunc(func(_ context.Context, tenant string, environment cpup01.EnvironmentBindingSnapshot) (string, error) {
		tokens.Add(1)
		if tenant != request.Admission.TenantID || environment != request.Admission.Snapshot.Environment {
			t.Error("CPU07_NOT_SENT_BEHAVIOR: provider received another tenant/environment")
		}
		visible, err := reader.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
		if err != nil || visible.State != biz.PipelineDispatchSubmitting || visible.NotSentAt != nil || visible.UncertainAt != nil || len(visible.ConfirmedRuns) != 0 {
			t.Errorf("CPU07_NOT_SENT_BEHAVIOR: provider called before committed SUBMITTING: %v", err)
		} else {
			select {
			case reservedBeforeDenial <- visible:
			default:
				t.Error("CPU07_NOT_SENT_BEHAVIOR: provider was called again")
			}
			t.Log("CPU07_NOT_SENT_PREFLIGHT PASS: provider observed real committed SUBMITTING before denying; no HTTP sent")
		}
		return "", errors.New("synthetic-private-provider-denial")
	})
	submitter := newSubmitter(t, submission.New(writerPool), server, provider)
	got, err := submitter.Submit(ctx, request)
	if got.Observation == nil || got.Observation.State != biz.PipelineSubmissionNotSent || got.Observation.RunID != "" || posts.Load() != 0 || tokens.Load() != 1 {
		t.Fatalf("CPU07_NOT_SENT_BEHAVIOR: provider denial did not remain a no-send observation: %v", err)
	}
	if !errors.Is(err, biz.ErrPipelineSubmissionNotSent) || got.Dispatch.State != biz.PipelineDispatchNotSent || got.Dispatch.NotSentAt == nil {
		t.Fatalf("CPU07_NOT_SENT_BEHAVIOR: local denial was not durably NOT_SENT: %v", err)
	}
	var original biz.PipelineDispatch
	select {
	case original = <-reservedBeforeDenial:
	default:
		t.Fatal("CPU07_NOT_SENT_BEHAVIOR: provider did not observe the original reservation")
	}
	assertOriginalDispatch(t, got.Dispatch, request)
	if got.Dispatch.AttemptID != original.AttemptID || got.Dispatch.PlanHash != original.PlanHash || !got.Dispatch.ReservedAt.Equal(original.ReservedAt) || !reflect.DeepEqual(got.Dispatch.Plan, original.Plan) ||
		got.Dispatch.NotSentAt.Before(original.ReservedAt) || got.Dispatch.NotSentAt.IsZero() || got.Dispatch.NotSentAt.Nanosecond()%1000 != 0 || got.Dispatch.UncertainAt != nil || len(got.Dispatch.ConfirmedRuns) != 0 {
		t.Fatal("CPU07_NOT_SENT_BEHAVIOR: local denial changed the attempt or invented other observations")
	}
	writerPool.Close()
	readerPool.Close()
	restartedPool := openPool()
	restarted := submission.New(restartedPool)
	stored, err := restarted.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(stored, got.Dispatch) {
		t.Fatalf("CPU07_NOT_SENT_BEHAVIOR: reconnect lost committed no-send facts: %v", err)
	}
	replayed, err := newSubmitter(t, restarted, server, provider).Submit(ctx, request)
	if err != nil || replayed.Observation != nil || !reflect.DeepEqual(replayed.Dispatch, stored) || posts.Load() != 0 || tokens.Load() != 1 {
		t.Fatalf("CPU07_NOT_SENT_BEHAVIOR: reconnect regenerated credentials/POST or changed facts: %v", err)
	}
	admitted, err := execution.New(restartedPool).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || !reflect.DeepEqual(admitted.Admission, request.Admission) || admitted.Close != nil {
		t.Fatal("CPU07_NOT_SENT_BEHAVIOR: no-send observation changed Admission or canceled the execution")
	}
}
