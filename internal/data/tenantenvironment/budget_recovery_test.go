package tenantenvironment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	ktesting "k8s.io/client-go/testing"
)

// Delay only the external RoleBinding GET boundary. Each completed request stays
// below the production five-second upstream limit, but their aggregate exhausts
// the real, unchanged 25-second Ensure budget after durable object receipts exist.
type budgetRecoveryKube struct {
	dynamic.Interface
	enabled   atomic.Bool
	completed atomic.Int32
	canceled  atomic.Int32
}

func (k *budgetRecoveryKube) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return budgetRecoveryNamespaceable{NamespaceableResourceInterface: k.Interface.Resource(gvr), boundary: k, delayed: gvr.Resource == "rolebindings"}
}

type budgetRecoveryNamespaceable struct {
	dynamic.NamespaceableResourceInterface
	boundary *budgetRecoveryKube
	delayed  bool
}

func (k budgetRecoveryNamespaceable) Namespace(namespace string) dynamic.ResourceInterface {
	return budgetRecoveryResource{ResourceInterface: k.NamespaceableResourceInterface.Namespace(namespace), boundary: k.boundary, delayed: k.delayed}
}

type budgetRecoveryResource struct {
	dynamic.ResourceInterface
	boundary *budgetRecoveryKube
	delayed  bool
}

func (k budgetRecoveryResource) Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	if k.delayed && k.boundary.enabled.Load() {
		timer := time.NewTimer(4 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			k.boundary.canceled.Add(1)
			return nil, ctx.Err()
		case <-timer.C:
			k.boundary.completed.Add(1)
		}
	}
	return k.ResourceInterface.Get(ctx, name, options, subresources...)
}

func TestEnvironmentPostgresEnsureBudgetExhaustionResumesReceipts(t *testing.T) {
	pool := postgres.Prepare(t)()
	repository := NewRepository(pool)
	platform, _, kube := fixture(t)
	boundary := &budgetRecoveryKube{Interface: kube}
	boundary.enabled.Store(true)
	platform.kube = boundary

	var mu sync.Mutex
	experiments := map[string]experiment{}
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/apis/v2beta1/experiments") {
			mu.Lock()
			defer mu.Unlock()
			if r.Method == http.MethodPost {
				var e experiment
				if json.NewDecoder(r.Body).Decode(&e) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				e.ID = uuid.NewString()
				e.StorageState = "AVAILABLE"
				experiments[e.ID] = e
				posts++
				_ = json.NewEncoder(w).Encode(e)
				return
			}
			if strings.Contains(r.URL.Path, "experiments/") {
				id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				_ = json.NewEncoder(w).Encode(experiments[id])
				return
			}
			items := []experiment{}
			for _, e := range experiments {
				items = append(items, e)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"experiments": items})
			return
		}
		for _, profile := range platform.config.Profiles {
			if strings.HasSuffix(r.URL.Path, "/versions/"+profile.VersionID) {
				_ = json.NewEncoder(w).Encode(map[string]any{"pipeline_version_id": profile.VersionID, "pipeline_id": profile.PipelineID, "display_name": "ir-" + profile.IRSHA256[:16], "pipeline_spec": map[string]any{"root": map[string]any{}}})
				return
			}
			if strings.HasSuffix(r.URL.Path, "/pipelines/"+profile.PipelineID) {
				_ = json.NewEncoder(w).Encode(map[string]any{"pipeline_id": profile.PipelineID, "namespace": "ani-system", "display_name": profile.ID})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	platform.config.KFPEndpoint = server.URL
	platform.kfp = &experimentClient{endpoint: server.URL, token: PlatformTokenSource{File: platform.config.KFPTokenFile}, http: server.Client()}
	usecase, err := biz.NewTenantEnvironment(repository, platform, testCluster, platform.config.KFPConnectionRef)
	if err != nil {
		t.Fatal(err)
	}

	// Background has no caller deadline: expiration must come from Ensure itself.
	firstOperation := uuid.NewString()
	started := time.Now()
	_, err = usecase.Ensure(context.Background(), testTenant, "governance:budget-recovery", firstOperation)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) || elapsed < 25*time.Second || elapsed >= 30*time.Second {
		t.Fatalf("production Ensure budget not observed: elapsed=%s error=%v", elapsed, err)
	}
	if boundary.completed.Load() < 2 || boundary.canceled.Load() != 1 {
		t.Fatalf("external requests did not exhaust the aggregate budget: completed=%d canceled=%d", boundary.completed.Load(), boundary.canceled.Load())
	}

	readContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	interrupted, err := repository.Get(readContext, testTenant, testCluster)
	if err != nil || interrupted.State != biz.EnvironmentProvisioning || interrupted.Phase != biz.EnvironmentResources || interrupted.NamespaceUID == "" || len(interrupted.BindingCanonical) != 0 || interrupted.ExperimentID != "" {
		t.Fatalf("budget expiration lost resumable persistent state: state=%s phase=%s error=%v", interrupted.State, interrupted.Phase, err)
	}
	originals := map[string]biz.EnvironmentObjectReceipt{}
	pending := 0
	for _, r := range interrupted.Receipts.Objects {
		if r.Confirmed {
			originals[r.Kind+"/"+r.Name] = r
		} else {
			pending++
		}
	}
	if len(originals) < 3 || pending != 1 {
		t.Fatalf("expected confirmed receipts plus one durable create intent: confirmed=%d pending=%d", len(originals), pending)
	}
	if _, err = repository.ReadReadyBinding(readContext, testTenant, testCluster); err == nil {
		t.Fatal("unfinished budget-exhausted environment exposed a READY binding")
	}
	lock, held, err := repository.TryLock(readContext, testTenant, testCluster)
	if err != nil || !held {
		t.Fatal("expired Ensure leaked its dedicated advisory lock", err)
	}
	lock.Release()

	// The next explicit operation uses the same database and external objects.
	boundary.enabled.Store(false)
	secondOperation := uuid.NewString()
	ready, err := usecase.Ensure(context.Background(), testTenant, "governance:budget-recovery", secondOperation)
	if err != nil || ready.State != biz.EnvironmentReady || ready.Phase != biz.EnvironmentComplete {
		t.Fatalf("explicit Ensure did not resume to READY: state=%s phase=%s reason=%s error=%v", ready.State, ready.Phase, ready.ReasonCode, err)
	}
	if ready.ID != interrupted.ID || ready.NamespaceUID != interrupted.NamespaceUID || ready.ProvisionSpecHash != interrupted.ProvisionSpecHash || ready.FirstOperationID != firstOperation || ready.LastOperationID != secondOperation {
		t.Fatal("resuming the durable registration changed frozen identity or operation provenance")
	}
	for _, r := range ready.Receipts.Objects {
		if original, ok := originals[r.Kind+"/"+r.Name]; ok {
			if !r.Confirmed || r.UID != original.UID || r.SpecDigest != original.SpecDigest {
				t.Fatalf("recovery replaced original receipt %s/%s", r.Kind, r.Name)
			}
			delete(originals, r.Kind+"/"+r.Name)
		}
	}
	if len(originals) != 0 {
		t.Fatal("recovery discarded an original confirmed receipt")
	}
	creates := map[string]int{}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "create" {
			o := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
			key := o.GetKind() + "/" + o.GetNamespace() + "/" + o.GetName()
			creates[key]++
			if creates[key] != 1 {
				t.Fatalf("recovery recreated an existing external object %s", key)
			}
		}
	}
	projection, err := repository.ReadReadyBinding(context.Background(), testTenant, testCluster)
	if err != nil || projection.Environment.BindingID != interrupted.ID || projection.Environment.NamespaceUID != interrupted.NamespaceUID || biz.EnvironmentDigest(ready.BindingCanonical) != ready.BindingDigest {
		t.Fatal("resumed seal/projection/CAS did not expose the original environment", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || len(experiments) != 1 {
		t.Fatalf("recovery created duplicate experiments: posts=%d count=%d", posts, len(experiments))
	}
	t.Logf("EN02_BUDGET_RECOVERY PASS elapsed=%s completed_bounded_requests=%d canceled_requests=%d persisted_receipts=%d confirmed_receipts=%d pending_intents=%d original_environment=true original_namespace_uid=true original_object_uids=true advisory_lock_released=true sealed_ready=true experiment_posts=%d", elapsed, boundary.completed.Load(), boundary.canceled.Load(), len(interrupted.Receipts.Objects), len(interrupted.Receipts.Objects)-pending, pending, posts)
}
