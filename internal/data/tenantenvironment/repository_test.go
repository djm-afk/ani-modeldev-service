package tenantenvironment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
)

func registeredFixture() biz.TenantExecutionEnvironment {
	id := uuid.NewString()
	operation := uuid.NewString()
	raw := []byte(`{"schema":"platform"}`)
	return biz.TenantExecutionEnvironment{ID: id, TenantID: testTenant, ClusterID: testCluster, NamespaceName: "tenant-" + testTenant, State: biz.EnvironmentProvisioning, Phase: biz.EnvironmentNamespace, ProvisionSpecCanonical: raw, ProvisionSpecHash: biz.EnvironmentDigest(raw), KFPConnectionRef: "managed-kfp", ExperimentName: "md-env-" + id, FirstOperationID: operation, LastOperationID: operation, FirstActorID: "governance:user:42", LastActorID: "governance:user:42"}
}
func TestEnvironmentPostgresConcurrentNaturalIdentityAndAdvisoryLock(t *testing.T) {
	pool := postgres.Prepare(t)()
	repository := NewRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, e := repository.Get(ctx, testTenant, testCluster); !errors.Is(e, biz.ErrTenantEnvironmentNotRegistered) {
		t.Fatal("Get synthesized registration", e)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := repository.Register(ctx, registeredFixture())
			if e != nil {
				errs <- e
				return
			}
			ids <- v.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	identity := ""
	for id := range ids {
		if identity != "" && identity != id {
			t.Fatal("registration changed identity")
		}
		identity = id
	}
	var count int
	if e := pool.QueryRow(ctx, `SELECT count(*) FROM modeldev_tenant_execution_environments`).Scan(&count); e != nil || count != 1 {
		t.Fatal("natural unique registration", count, e)
	}
	first, held, e := repository.TryLock(ctx, testTenant, testCluster)
	if e != nil || !held {
		t.Fatal("first lock", e)
	}
	second, held, e := repository.TryLock(ctx, testTenant, testCluster)
	if second != nil || e != nil || held {
		t.Fatal("concurrent Ensure lock admitted")
	}
	first.Release()
	third, held, e := repository.TryLock(ctx, testTenant, testCluster)
	if e != nil || !held {
		t.Fatal("released lock was pooled", e)
	}
	third.Release()
	row, e := repository.Get(ctx, testTenant, testCluster)
	if e != nil {
		t.Fatal(e)
	}
	row.NamespaceUID = uuid.NewString()
	row.ExperimentID = uuid.NewString()
	row.BindingCanonical = []byte(`{"sealed":true}`)
	row.BindingDigest = biz.EnvironmentDigest(row.BindingCanonical)
	if e = repository.Save(ctx, &row); e != nil {
		t.Fatal("seal checkpoint", e)
	}
	old := row
	old.NamespaceUID = uuid.NewString()
	if repository.Save(ctx, &old) == nil {
		t.Fatal("database allowed namespace rebind")
	}
	old = row
	old.BindingCanonical = []byte(`{"sealed":false}`)
	old.BindingDigest = biz.EnvironmentDigest(old.BindingCanonical)
	if repository.Save(ctx, &old) == nil {
		t.Fatal("database allowed sealed binding mutation")
	}
	old = row
	old.RowVersion--
	if repository.Save(ctx, &old) == nil {
		t.Fatal("stale CAS overwrote environment")
	}
}

func TestEnvironmentPostgresVerticalEnsureSealPublishAndBusyRead(t *testing.T) {
	pool := postgres.Prepare(t)()
	repository := NewRepository(pool)
	platform, _, kube := fixture(t)
	experiments := map[string]experiment{}
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/apis/v2beta1/experiments") {
			if r.Method == http.MethodPost {
				var e experiment
				_ = json.NewDecoder(r.Body).Decode(&e)
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
		w.WriteHeader(404)
	}))
	defer server.Close()
	platform.kfp = &experimentClient{endpoint: server.URL, token: PlatformTokenSource{File: platform.config.KFPTokenFile}, http: server.Client()}
	// Simulate a successful Kubernetes create whose HTTP response was lost.
	lost := false
	kube.PrependReactor("create", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		if lost {
			return false, nil, nil
		}
		lost = true
		a := action.(ktesting.CreateAction)
		o := a.GetObject().(*unstructured.Unstructured)
		o.SetUID(types.UID(uuid.NewString()))
		o.SetResourceVersion("1")
		if e := kube.Tracker().Create(a.GetResource(), o, a.GetNamespace()); e != nil {
			return true, nil, e
		}
		return true, nil, errors.New("response lost")
	})
	usecase, e := biz.NewTenantEnvironment(repository, platform, testCluster, platform.config.KFPConnectionRef)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	first, e := usecase.Ensure(ctx, testTenant, "governance:user:42", uuid.NewString())
	if e != nil || first.State != biz.EnvironmentReady {
		t.Fatalf("vertical Ensure did not reach READY: %v state=%s phase=%s reason=%s", e, first.State, first.Phase, first.ReasonCode)
	}
	if posts != 1 || !lost {
		t.Fatal("external original create path not exercised")
	}
	read, e := usecase.Get(ctx, testTenant)
	if e != nil || read.ID != first.ID || read.BindingDigest != first.BindingDigest {
		t.Fatal("Get changed binding", e)
	}
	binding, e := repository.ReadReadyBinding(ctx, testTenant, testCluster)
	if e != nil || binding.Environment.BindingID != first.ID {
		t.Fatal("sealed binding unavailable", e)
	}
	lock, held, e := repository.TryLock(ctx, testTenant, testCluster)
	if e != nil || !held {
		t.Fatal(e)
	}
	busy, e := usecase.Ensure(ctx, testTenant, "governance:user:43", uuid.NewString())
	lock.Release()
	if e != nil || busy.State != biz.EnvironmentReady || busy.RowVersion != read.RowVersion {
		t.Fatal("busy call changed persistent READY", e)
	}
	again, e := usecase.Ensure(ctx, testTenant, "governance:user:44", uuid.NewString())
	if e != nil || again.State != biz.EnvironmentReady || again.ID != first.ID || again.BindingDigest != first.BindingDigest || posts != 1 {
		t.Fatalf("explicit retry rebound environment: %v %s/%s", e, again.State, again.ReasonCode)
	}
}

func TestEnvironmentPostgresNativeDenialKeepsBlockedWithoutExperiment(t *testing.T) {
	pool := postgres.Prepare(t)()
	repository := NewRepository(pool)
	platform, _, kube := fixture(t)
	kfpRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		kfpRequests++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	platform.kfp = &experimentClient{endpoint: server.URL, token: PlatformTokenSource{File: platform.config.KFPTokenFile}, http: server.Client()}
	kube.PrependReactor("create", "rolebindings", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "rolebindings"}, "modeldev-runtime", nil)
	})
	usecase, err := biz.NewTenantEnvironment(repository, platform, testCluster, platform.config.KFPConnectionRef)
	if err != nil {
		t.Fatal(err)
	}
	row, err := usecase.Ensure(context.Background(), testTenant, "governance:user:42", uuid.NewString())
	if err != nil || row.State != biz.EnvironmentBlocked || row.Phase != biz.EnvironmentResources || row.ReasonCode != "ENVIRONMENT_AUTHORIZATION_UNVERIFIED" {
		t.Fatalf("native refusal did not persist finite BLOCKED: state=%s phase=%s reason=%s error=%v", row.State, row.Phase, row.ReasonCode, err)
	}
	read, err := usecase.Get(context.Background(), testTenant)
	if err != nil || read.ID != row.ID || read.State != biz.EnvironmentBlocked || read.ReasonCode != row.ReasonCode || len(read.BindingCanonical) != 0 || read.ExperimentID != "" || kfpRequests != 0 {
		t.Fatalf("denied Ensure exposed a binding or advanced to KFP: state=%s requests=%d error=%v", read.State, kfpRequests, err)
	}
	if _, err = repository.ReadReadyBinding(context.Background(), testTenant, testCluster); err == nil {
		t.Fatal("native-denied environment exposed a READY binding")
	}
	for _, action := range kube.Actions() {
		if action.GetResource().Resource == "secrets" && action.GetVerb() != "get" {
			t.Fatal("native refusal was followed by an artifact credential write")
		}
	}
}
