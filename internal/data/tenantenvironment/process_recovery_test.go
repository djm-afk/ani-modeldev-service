package tenantenvironment

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// The process owns the real advisory-lock connection and Ensure state machine.
// Its external Kubernetes/KFP boundary outlives SIGKILL in the parent process.
func TestEnvironmentEnsureRecoveryChild(t *testing.T) {
	file := os.Getenv("MODELDEV_ENVIRONMENT_RECOVERY_CONFIG")
	if file == "" {
		return
	}
	raw, err := os.ReadFile(file)
	var c Config
	if err != nil || json.Unmarshal(raw, &c) != nil {
		t.Fatal("child platform input unavailable")
	}
	poolConfig, err := pgxpool.ParseConfig(os.Getenv("CPU_P01_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("child runtime reference invalid")
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = os.Getenv("MODELDEV_ENVIRONMENT_RECOVERY_SCHEMA")
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal("child database unavailable")
	}
	defer pool.Close()
	kube, err := dynamic.NewForConfig(&rest.Config{Host: os.Getenv("MODELDEV_ENVIRONMENT_RECOVERY_KUBE"), Timeout: 5 * time.Second, QPS: 1000, Burst: 1000})
	if err != nil {
		t.Fatal("child external Kubernetes boundary unavailable")
	}
	p := &Platform{config: c, kube: kube, images: &imageEnsurer{}, kfp: &experimentClient{endpoint: c.KFPEndpoint, token: PlatformTokenSource{File: c.KFPTokenFile}, http: &http.Client{Timeout: 5 * time.Second}}}
	u, err := biz.NewTenantEnvironment(NewRepository(pool), p, testCluster, c.KFPConnectionRef)
	if err != nil {
		t.Fatal(err)
	}
	row, err := u.Ensure(context.Background(), testTenant, "governance:recovery-test", uuid.NewString())
	if err != nil || row.State != biz.EnvironmentReady {
		t.Fatalf("child Ensure failed: state=%s phase=%s reason=%s error=%v", row.State, row.Phase, row.ReasonCode, err)
	}
}

func TestEnvironmentPostgresSIGKILLRecoversOriginalExternalObjects(t *testing.T) {
	for _, stage := range []string{"serviceaccount-create", "experiment-post"} {
		t.Run(stage, func(t *testing.T) {
			pool := postgres.Prepare(t)()
			repository := NewRepository(pool)
			p, _, kube := fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			created := make(chan struct{}, 1)
			resume := make(chan struct{})
			var mu sync.Mutex
			armed := true
			firstUID := ""
			posts := 0
			experiments := map[string]experiment{}
			block := func(r *http.Request, uid string) {
				mu.Lock()
				pause := armed
				if pause {
					armed = false
					firstUID = uid
				}
				mu.Unlock()
				if pause {
					created <- struct{}{}
					select {
					case <-r.Context().Done():
					case <-resume:
					case <-ctx.Done():
					}
				}
			}
			kubeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				var gvr schema.GroupVersionResource
				if len(parts) >= 3 && parts[0] == "api" {
					gvr.Version = parts[1]
					parts = parts[2:]
				} else if len(parts) >= 4 && parts[0] == "apis" {
					gvr.Group, gvr.Version = parts[1], parts[2]
					parts = parts[3:]
				} else {
					http.NotFound(w, r)
					return
				}
				ns := ""
				if len(parts) >= 3 && parts[0] == "namespaces" {
					ns = parts[1]
					parts = parts[2:]
				}
				gvr.Resource = parts[0]
				var result any
				var err error
				client := kube.Resource(gvr).Namespace(ns)
				switch r.Method {
				case http.MethodGet:
					if len(parts) == 1 {
						result, err = client.List(r.Context(), metav1.ListOptions{Limit: 100, Continue: r.URL.Query().Get("continue")})
					} else {
						result, err = client.Get(r.Context(), parts[1], metav1.GetOptions{})
					}
				case http.MethodPost:
					var object unstructured.Unstructured
					if json.NewDecoder(r.Body).Decode(&object) != nil {
						w.WriteHeader(400)
						return
					}
					result, err = client.Create(r.Context(), &object, metav1.CreateOptions{})
					if err == nil && stage == "serviceaccount-create" && gvr.Resource == "serviceaccounts" {
						block(r, string(result.(*unstructured.Unstructured).GetUID()))
					}
				default:
					w.WriteHeader(405)
					return
				}
				if err != nil {
					if status, ok := err.(*apierrors.StatusError); ok {
						w.WriteHeader(int(status.ErrStatus.Code))
						_ = json.NewEncoder(w).Encode(status.ErrStatus)
					} else {
						w.WriteHeader(500)
					}
					return
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer kubeServer.Close()
			kfpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasPrefix(r.URL.Path, "/apis/v2beta1/experiments") {
					if r.Method == http.MethodPost {
						var e experiment
						_ = json.NewDecoder(r.Body).Decode(&e)
						e.ID = uuid.NewString()
						e.StorageState = "AVAILABLE"
						mu.Lock()
						experiments[e.ID] = e
						posts++
						mu.Unlock()
						if stage == "experiment-post" {
							block(r, e.ID)
						}
						_ = json.NewEncoder(w).Encode(e)
						return
					}
					mu.Lock()
					defer mu.Unlock()
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
				for _, profile := range p.config.Profiles {
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
			defer kfpServer.Close()
			p.config.KFPEndpoint = kfpServer.URL
			configFile := filepath.Join(t.TempDir(), "public-platform.json")
			raw, _ := json.Marshal(p.config)
			if err := os.WriteFile(configFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			start := func() (*exec.Cmd, *bytes.Buffer, <-chan error) {
				command := exec.CommandContext(ctx, executable, "-test.run=^TestEnvironmentEnsureRecoveryChild$", "-test.v")
				command.Env = append(os.Environ(), "MODELDEV_ENVIRONMENT_RECOVERY_CONFIG="+configFile, "MODELDEV_ENVIRONMENT_RECOVERY_SCHEMA="+pool.Config().ConnConfig.RuntimeParams["search_path"], "MODELDEV_ENVIRONMENT_RECOVERY_KUBE="+kubeServer.URL)
				output := new(bytes.Buffer)
				command.Stdout, command.Stderr = output, output
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- command.Wait() }()
				return command, output, done
			}
			command, output, done := start()
			select {
			case <-created:
			case err := <-done:
				t.Fatalf("child exited before external commit: %v %s", err, output.String())
			case <-ctx.Done():
				t.Fatal("external commit was not reached")
			}
			interrupted, err := repository.Get(ctx, testTenant, testCluster)
			if err != nil || interrupted.State != biz.EnvironmentProvisioning || interrupted.NamespaceUID == "" {
				t.Fatal("original durable environment missing", err)
			}
			if stage == "experiment-post" && (interrupted.ExperimentIntent == nil || !interrupted.ExperimentIntent.Sent || interrupted.ExperimentID != "") {
				t.Fatal("POST was not preceded by durable uncertain intent")
			}
			if err = command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err == nil {
				t.Fatal("SIGKILL child unexpectedly exited successfully")
			}
			close(resume)
			// PostgreSQL must observe the killed process's closed lock socket.
			// An immediate explicit Ensure is allowed to report busy meanwhile.
			for {
				lock, held, lockErr := repository.TryLock(ctx, testTenant, testCluster)
				if lockErr != nil {
					t.Fatal("could not inspect killed process lock")
				}
				if held {
					lock.Release()
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("killed process retained its advisory lock")
				case <-time.After(10 * time.Millisecond):
				}
			}
			_, output, recovered := start()
			if err = <-recovered; err != nil {
				t.Fatalf("fresh process did not recover: %v %s", err, output.String())
			}
			ready, err := repository.Get(ctx, testTenant, testCluster)
			if err != nil || ready.State != biz.EnvironmentReady || ready.ID != interrupted.ID || ready.NamespaceUID != interrupted.NamespaceUID {
				t.Fatal("fresh process changed durable identity", err)
			}
			mu.Lock()
			observedUID, postCount := firstUID, posts
			mu.Unlock()
			if stage == "serviceaccount-create" {
				if len(ready.Receipts.Objects) == 0 || ready.Receipts.Objects[0].UID != observedUID {
					t.Fatal("created original SA UID was replaced")
				}
			} else if ready.ExperimentID != observedUID {
				t.Fatal("created original Experiment ID was replaced")
			}
			if postCount != 1 {
				t.Fatal("recovery created another Experiment")
			}
			t.Logf("SIGKILL_RECOVERY PASS stage=%s same_environment=true same_namespace_uid=true original_external_identity=true experiment_posts=%d", stage, postCount)
		})
	}
}
