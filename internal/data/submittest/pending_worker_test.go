package submittest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

// The TLS server substitutes only external KFP. Admission, pending lookup,
// worker, one-permit CAS, HTTP client and recorded Run all use real code/PG.
func TestDispatchWorkerRecoversAdmissionAndConcurrentWorkersSendOneRun(t *testing.T) {
	open := postgres.Prepare(t)
	request := dispatchRequest(t)
	request.Admission = admissionfixture.EnsureEnvironment(t, open(), request.Admission)
	request.Owner = request.Admission.Snapshot.DispatchOwner
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admittedPool := open()
	if _, err := execution.New(admittedPool).Accept(ctx, request.Admission); err != nil {
		t.Fatal("pending worker admission preflight", err)
	}
	admittedPool.Close()
	repository := submission.New(open())
	binding := admissionfixture.DispatchBinding{TenantID: request.Admission.TenantID, Environment: request.Admission.Snapshot.Environment, Owner: request.Owner}
	t.Log("PENDING_DISPATCH_PREFLIGHT PASS: real restricted PostgreSQL admission committed and original connection closed")
	pending, err := testPendingAdmissions(ctx, repository, binding)
	if err != nil || len(pending) != 1 || pending[0].ExecutionID != request.Admission.ExecutionID {
		t.Fatalf("PENDING_DISPATCH_BEHAVIOR: committed admission not discovered after restart: %v", err)
	}
	var posts atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/apis/v2beta1/runs" || r.Header.Get("Authorization") != "Bearer synthetic-pending-token" {
			t.Error("unexpected KFP creation operation or credential")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		persisted, err := repository.Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
		if err != nil || persisted.State != biz.PipelineDispatchSubmitting {
			t.Error("worker called KFP before committing reservation")
		}
		snapshot := request.Admission.Snapshot
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": "55555555-6666-4777-8888-999999999999", "experiment_id": snapshot.Environment.ExperimentID, "display_name": "md-" + request.Admission.ExecutionID, "pipeline_version_reference": map[string]string{"pipeline_id": snapshot.Implementation.PipelineID, "pipeline_version_id": snapshot.Implementation.PipelineVersionID}, "runtime_config": map[string]any{"parameters": map[string]string{"execution_id": request.Admission.ExecutionID, "spec_hash": request.Admission.SpecHash, "environment_binding_id": snapshot.Environment.BindingID, "namespace_uid": snapshot.Environment.NamespaceUID}, "pipeline_root": request.Owner.PipelineRoot}, "service_account": snapshot.Environment.Identities.KFPStepServiceAccount, "state": "PENDING"})
	}))
	t.Cleanup(peer.Close)
	provider := tokenProviderFunc(func(_ context.Context, tenant string, environment cpup01.EnvironmentBindingSnapshot) (string, error) {
		if tenant != binding.TenantID || environment != binding.Environment {
			t.Error("worker requested credential for another binding")
		}
		return "synthetic-pending-token", nil
	})
	submitter := newSubmitter(t, repository, peer, provider)
	// Synchronize actual repository reads so both workers observe this same
	// pending admission before either reserves. This wrapper invents no facts.
	gate := &pendingReadGate{repository: repository, ready: make(chan struct{})}
	workers := make([]*biz.DispatchWorker, 2)
	for index := range workers {
		workers[index], err = biz.NewDispatchWorker(gate, submitter, binding.Environment.ClusterID, testEnvironmentGate{binding.Environment}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
	}
	errors := make(chan error, 2)
	for _, worker := range workers {
		go func() { _, err := worker.DispatchOnce(ctx); errors <- err }()
	}
	for range workers {
		if err := <-errors; err != nil {
			t.Fatal("concurrent worker failed", err)
		}
	}
	if posts.Load() != 1 {
		t.Fatalf("same durable admission created %d KFP Runs", posts.Load())
	}
	if pending, err = testPendingAdmissions(ctx, repository, binding); err != nil || len(pending) != 0 {
		t.Fatalf("reserved admission remained eligible for a second send: %v", err)
	}
	restarted, err := biz.NewDispatchWorker(submission.New(open()), submitter, binding.Environment.ClusterID, testEnvironmentGate{binding.Environment}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := restarted.DispatchOnce(ctx); err != nil || count.Examined != 0 || posts.Load() != 1 {
		t.Fatalf("restart repeated KFP creation: count=%+v error=%v", count, err)
	}
	observed, err := submission.New(open()).Get(ctx, request.Admission.TenantID, request.Admission.ExecutionID)
	if err != nil || observed.State != biz.PipelineDispatchConfirmed || len(observed.ConfirmedRuns) != 1 {
		t.Fatalf("worker lost its durable KFP response: %v", err)
	}
	t.Log("PENDING_DISPATCH PASS: admission discovered after restart; concurrent workers emitted one actual TLS CreateRun and persisted one Run; restart emitted none")
}

func TestPendingDispatchScanExcludesOtherBindingsStoppedExpiredAndReserved(t *testing.T) {
	open := postgres.Prepare(t)
	request := dispatchRequest(t)
	request.Admission = admissionfixture.EnsureEnvironment(t, open(), request.Admission)
	request.Owner = request.Admission.Snapshot.DispatchOwner
	ctx := context.Background()
	pool := open()
	admissions := execution.New(pool)
	repository := submission.New(pool)
	if _, err := admissions.Accept(ctx, request.Admission); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"other-tenant", "other-environment", "stopped", "expired", "reserved"} {
		candidate := dispatchRequest(t)
		candidate.Admission.Snapshot.Environment = request.Admission.Snapshot.Environment
		candidate.Admission.ExecutionID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("pending-execution-"+kind)).String()
		candidate.Admission.OperationID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("pending-operation-"+kind)).String()
		switch kind {
		case "other-tenant":
			candidate.Admission.TenantID = "99999999-2222-4333-8444-555555555555"
		case "other-environment":
			candidate.Admission.Snapshot.Environment.NamespaceUID = "88888888-2222-4333-8444-555555555555"
		case "expired":
			candidate.Admission.AcceptedAt = time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
			candidate.Admission.Snapshot.DeadlineAt = candidate.Admission.AcceptedAt.Add(time.Hour)
		}
		candidate.Admission = admissionfixture.Bind(t, candidate.Admission)
		var err error
		if _, err = admissions.Accept(ctx, candidate.Admission); err != nil {
			t.Fatal("scan fixture admission", kind, err)
		}
		if kind == "stopped" {
			if _, err = admissions.ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: candidate.Admission.TenantID, ExecutionID: candidate.Admission.ExecutionID, OperationID: candidate.Admission.OperationID, SpecHash: candidate.Admission.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: candidate.Admission.Actor}); err != nil {
				t.Fatal(err)
			}
		}
		candidate.Owner = candidate.Admission.Snapshot.DispatchOwner
		if kind == "reserved" {
			if _, err = repository.Reserve(ctx, candidate); err != nil {
				t.Fatal(err)
			}
		}
	}
	binding := admissionfixture.DispatchBinding{TenantID: request.Admission.TenantID, Environment: request.Admission.Snapshot.Environment, Owner: request.Owner}
	pending, err := testPendingAdmissions(ctx, repository, binding)
	if err != nil || len(pending) != 2 {
		t.Fatalf("PENDING_DISPATCH_SCOPE: scan did not isolate one authorized eligible admission: count=%+v error=%v", len(pending), err)
	}
}

type pendingReadGate struct {
	repository biz.PendingAdmissionRepository
	count      atomic.Int32
	once       sync.Once
	ready      chan struct{}
}

func (gate *pendingReadGate) ListPendingDispatchTenants(ctx context.Context, cluster, after string, limit int) ([]string, error) {
	return gate.repository.ListPendingDispatchTenants(ctx, cluster, after, limit)
}
func (gate *pendingReadGate) DispatchDiscoveryTime(ctx context.Context) (time.Time, error) {
	return gate.repository.DispatchDiscoveryTime(ctx)
}
func (gate *pendingReadGate) ListPendingAdmissions(ctx context.Context, cluster, tenant string, cursor biz.DiscoveryCursor, limit int) (biz.PendingAdmissionPage, error) {
	page, err := gate.repository.ListPendingAdmissions(ctx, cluster, tenant, cursor, limit)
	if err != nil {
		return page, err
	}
	if gate.count.Add(1) == 2 {
		gate.once.Do(func() { close(gate.ready) })
	}
	select {
	case <-gate.ready:
		return page, nil
	case <-ctx.Done():
		return page, ctx.Err()
	}
}
func testPendingAdmissions(ctx context.Context, r *submission.Repository, binding admissionfixture.DispatchBinding) ([]biz.Admission, error) {
	before, err := r.DispatchDiscoveryTime(ctx)
	if err != nil {
		return nil, err
	}
	page, err := r.ListPendingAdmissions(ctx, binding.Environment.ClusterID, binding.TenantID, biz.DiscoveryCursor{SweepBefore: before}, 4)
	if err != nil {
		return nil, err
	}
	out := []biz.Admission{}
	for _, row := range page.Rows {
		if row.DecodeProblem != "" {
			return nil, biz.ErrPersistence
		}
		out = append(out, row.Admission)
	}
	return out, nil
}

type testEnvironmentGate struct {
	environment cpup01.EnvironmentBindingSnapshot
}

func (g testEnvironmentGate) VerifyDispatchAdmission(ctx context.Context, a biz.Admission) error {
	if a.Snapshot.Environment != g.environment {
		return biz.ErrAdmissionEnvironmentNotReady
	}
	return ctx.Err()
}
