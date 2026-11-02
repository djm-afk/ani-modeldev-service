// Package tenantenvironment provides real environment provisioning for tests.
// PostgreSQL, the domain Ensure state machine and every Platform check remain
// production code. Only Kubernetes, KFP and Resource transport boundaries vary.
package tenantenvironment

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	adapter "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

type Fixture struct {
	Config                                                                               adapter.Config
	Platform                                                                             *adapter.Platform
	Kube                                                                                 *fake.FakeDynamicClient
	Server                                                                               *httptest.Server
	Directory, ConfigFile, ConfigSHA256, MaterialsDirectory, CAFile, KubernetesTokenFile string
	mu                                                                                   sync.Mutex
	experiments                                                                          map[string]map[string]any
	profiles                                                                             map[string]projection.Profile
	RunHandler                                                                           http.Handler
	TokenReviewHandler                                                                   http.Handler
}
type imageBoundary struct{ secret string }

func (i imageBoundary) EnsurePullSecret(context.Context, string, string, string, string, string) (string, error) {
	return i.secret, nil
}
func resource(kind string) schema.GroupVersionResource {
	switch kind {
	case "Namespace":
		return schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	case "ClusterRole":
		return schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}
	case "RoleBinding":
		return schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	case "ValidatingAdmissionPolicy":
		return schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicies"}
	case "ValidatingAdmissionPolicyBinding":
		return schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicybindings"}
	case "MutatingWebhookConfiguration":
		return schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	}
	panic("fixture unsupported bootstrap kind")
}
func write(t testing.TB, path string, raw []byte) string {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func NewFixture(t testing.TB, a biz.Admission) *Fixture {
	t.Helper()
	s := a.Snapshot
	directory := t.TempDir()
	f := &Fixture{Directory: directory, MaterialsDirectory: filepath.Join(directory, "materials"), experiments: map[string]map[string]any{}, profiles: map[string]projection.Profile{}}
	if err := os.Mkdir(f.MaterialsDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	token := write(t, filepath.Join(directory, "kfp-token"), []byte("synthetic-kfp-owner"))
	f.KubernetesTokenFile = write(t, filepath.Join(directory, "kube-token"), []byte("synthetic-kube-owner"))
	ak := write(t, filepath.Join(directory, "artifact-access"), []byte("synthetic-artifact-access"))
	sk := write(t, filepath.Join(directory, "artifact-secret"), []byte("synthetic-artifact-secret"))
	artifact, _ := url.Parse(s.DispatchOwner.PipelineRoot)
	if artifact.Host == "" {
		t.Fatal("fixture requires original artifact bucket")
	}
	c := adapter.Config{SchemaVersion: "ani.modeldev.tenant-environment-platform.v1", ClusterID: s.Environment.ClusterID, KFPConnectionRef: s.Environment.KFPConnectionRef, KFPTokenFile: token, ArtifactEndpoint: "https://s3.example.test", ArtifactRegion: "us-east-1", ArtifactBucket: artifact.Host, ArtifactAccessKeyFile: ak, ArtifactSecretKeyFile: sk, ArtifactBucketIsolationEvidenceSHA256: strings.Repeat("a", 64), KFPCAKey: "ca.crt", MetadataHost: "metadata.kubeflow.svc", MetadataPort: "8080", RegistryAuthority: "registry.example.test", PlatformProject: "platform", InputScope: cpup01.StorageScope{StorageConnectionID: s.Input.Object.StorageConnectionID, Bucket: s.Input.Object.Bucket, CredentialReference: "input-read"}, PublicationScope: s.PublicationScope, ModelScope: conformance.FinetuningSnapshotV1().Finetuning.BaseModel.Scope, Workspace: s.Workspace, Timing: cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: s.Timing.ExecutionTimeoutSeconds, ExecutionTimeoutMaxSeconds: s.Timing.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: s.Timing.StepTimeoutSeconds}, InputFilePath: "/inputs/data.csv", OutputDirectoryPath: "/outputs", TargetPlatform: cpup01.TrainingPlatform{OS: "linux", Architecture: "amd64"}, DispatchOwner: s.DispatchOwner, MaterialBundleDigest: strings.Repeat("b", 64), ControllerPrincipals: []string{"system:serviceaccount:kubeflow:argo"}, KFPAPIServiceAccount: adapter.Subject{Kind: "ServiceAccount", Name: "ml-pipeline", Namespace: "kubeflow"}, StepOwner: adapter.StepOwnerConfig{Target: "modeldev:443"}}
	if s.Finetuning != nil {
		c.ModelScope = s.Finetuning.BaseModel.Scope
	}
	if s.Kind == "FINETUNING" {
		c.InputFilePath, c.OutputDirectoryPath = "/workspace/inputs/data/finetune_data.jsonl", "/workspace/training"
	}
	ns := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "status": map[string]any{"phase": "Active"}}}
	ns.SetName("tenant-" + a.TenantID)
	ns.SetUID(types.UID(s.Environment.NamespaceUID))
	ns.SetLabels(map[string]string{"network.ani.io/tenant-id": a.TenantID, "network.ani.io/managed-by": "ani-network-service"})
	objects := []runtime.Object{ns}
	add := func(kind, name string, fields map[string]any) {
		gvr := resource(kind)
		o := &unstructured.Unstructured{Object: map[string]any{"kind": kind, "apiVersion": gvr.Group + "/" + gvr.Version}}
		o.SetName(name)
		o.SetUID(types.UID(uuid.NewString()))
		o.SetResourceVersion("1")
		for k, v := range fields {
			o.Object[k] = v
		}
		if kind == "ValidatingAdmissionPolicy" {
			o.SetGeneration(1)
		}
		c.Bootstrap = append(c.Bootstrap, adapter.BootstrapObject{Kind: kind, Name: name, UID: string(o.GetUID()), SHA256: adapter.BootstrapDigest(o)})
		objects = append(objects, o)
	}
	for _, name := range []string{"modeldev-runtime-v1", "modeldev-step-v1", "modeldev-webhook-read-v1", "modeldev-kfp-api-v1", "modeldev-kfp-access-v1"} {
		rules := []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"pods"}, "verbs": []any{"get"}}}
		if name == "modeldev-step-v1" {
			rules = append(rules, map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": []any{"get"}, "resourceNames": []any{"modeldev-kfp-artifacts"}})
		}
		add("ClusterRole", name, map[string]any{"rules": rules})
	}
	for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
		for _, name := range []string{"modeldev-bootstrap-v1", "modeldev-environment-config-v1", "modeldev-workload-guard-v1"} {
			if kind == "ValidatingAdmissionPolicyBinding" {
				name += "-binding"
			}
			fields := map[string]any{"spec": map[string]any{"failurePolicy": "Fail"}}
			if kind == "ValidatingAdmissionPolicy" {
				fields["status"] = map[string]any{"observedGeneration": int64(1), "typeChecking": map[string]any{}}
			}
			add(kind, name, fields)
		}
	}
	add("MutatingWebhookConfiguration", "ani-modeldev-webhook", map[string]any{"webhooks": []any{}})
	f.Kube = fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), nil, objects...)
	f.Kube.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		o := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		o.SetUID(types.UID(uuid.NewString()))
		o.SetResourceVersion("1")
		return false, nil, nil
	})
	for _, name := range []string{"modeldev-mlp-cpu", "modeldev-mlp-gpu", "modeldev-sft-lora-cpu", "modeldev-sft-lora-gpu"} {
		raw := []byte(`{"root":{}}`)
		file := write(t, filepath.Join(directory, name+".json"), raw)
		profile := projection.Profile{ID: name, PipelineID: uuid.NewString(), VersionID: uuid.NewString(), IRSHA256: biz.EnvironmentDigest(raw)}
		material := conformance.MaterialV21()
		implementation, mode := "builtin.mlp", "cpu"
		if strings.Contains(name, "sft-lora") {
			material = conformance.FinetuningMaterialV21()
			implementation = "builtin.sft_lora"
		}
		if strings.HasSuffix(name, "-gpu") {
			mode = "gpu"
		}
		material.Workspace = s.Workspace
		material.Implementation.PipelineID, material.Implementation.PipelineVersionID, material.Implementation.PipelineIRSHA256 = profile.PipelineID, profile.VersionID, profile.IRSHA256
		encoded, err := material.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		profile.MaterialSHA256 = biz.EnvironmentDigest(encoded)
		write(t, filepath.Join(f.MaterialsDirectory, implementation+"."+mode+".json"), encoded)
		c.Profiles = append(c.Profiles, profile)
		c.IRSources = append(c.IRSources, adapter.IRSource{ProfileID: name, File: file, SHA256: profile.IRSHA256})
		f.profiles[name] = profile
	}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.Server.Certificate().Raw})
	f.CAFile = write(t, filepath.Join(directory, "external-ca.pem"), ca)
	c.KFPEndpoint, c.KFPCAFile = f.Server.URL, f.CAFile
	c.KFPPublicCA, c.ModeldevPublicCA, c.S3PublicCA = string(ca), string(ca), string(ca)
	f.Config = c
	platform, err := adapter.NewPlatform(c, f.Kube, imageBoundary{secret: "ani-pull-" + strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal("real fixture platform", err)
	}
	f.Platform = platform
	f.WriteConfig(t)
	return f
}
func (f *Fixture) WriteConfig(t testing.TB) {
	t.Helper()
	raw, err := json.Marshal(f.Config)
	if err != nil {
		t.Fatal(err)
	}
	f.ConfigFile = write(t, filepath.Join(f.Directory, "platform.json"), raw)
	f.ConfigSHA256 = biz.EnvironmentDigest(raw)
}
func (f *Fixture) Ensure(t testing.TB, pool *pgxpool.Pool, a biz.Admission) biz.Admission {
	t.Helper()
	repo := adapter.NewRepository(pool)
	usecase, err := biz.NewTenantEnvironment(repo, f.Platform, f.Config.ClusterID, f.Config.KFPConnectionRef)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var row biz.TenantExecutionEnvironment
	operation := uuid.NewString()
	for {
		row, err = usecase.Ensure(ctx, a.TenantID, a.Actor, operation)
		if err != nil || row.State == biz.EnvironmentReady {
			break
		}
		if row.State != biz.EnvironmentProvisioning {
			break
		}
		// Transaction-scoped advisory locks are shared across isolated schemas.
		// An explicit busy Ensure may observe PROVISIONING without owning work.
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			err = ctx.Err()
		case <-timer.C:
		}
		if err != nil {
			break
		}
	}
	if err != nil || row.State != biz.EnvironmentReady {
		t.Fatalf("real fixture Ensure: state=%s phase=%s reason=%s error=%v", row.State, row.Phase, row.ReasonCode, err)
	}
	binding, err := repo.ReadReadyBinding(context.Background(), a.TenantID, f.Config.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	return Rebind(t, a, binding, row.BindingDigest)
}
func Ensure(t testing.TB, pool *pgxpool.Pool, a biz.Admission) biz.Admission {
	t.Helper()
	repo := adapter.NewRepository(pool)
	if binding, err := repo.ReadReadyBinding(context.Background(), a.TenantID, a.Snapshot.Environment.ClusterID); err == nil {
		row, _ := repo.Get(context.Background(), a.TenantID, a.Snapshot.Environment.ClusterID)
		return Rebind(t, a, binding, row.BindingDigest)
	}
	return NewFixture(t, a).Ensure(t, pool, a)
}
func Rebind(t testing.TB, a biz.Admission, b projection.Binding, digest string) biz.Admission {
	t.Helper()
	a.Snapshot.Environment = b.Snapshot(digest)
	a.Snapshot.DispatchOwner = b.DispatchOwner
	a.Snapshot.DispatchOwner.PipelineRoot += "/" + a.ExecutionID
	a.Snapshot.ManagedImageAccess = b.ManagedImageAccess
	a.Snapshot.PublicationScope = b.PublicationScope
	a.Snapshot.Workspace = b.Workspace
	if a.Snapshot.Finetuning != nil {
		a.Snapshot.Finetuning.BaseModel.Scope = b.ModelScope
	}
	wanted := "modeldev-mlp-" + a.Snapshot.ComputeOrigin.DeviceMode
	if a.Snapshot.Kind == "FINETUNING" {
		wanted = "modeldev-sft-lora-" + a.Snapshot.ComputeOrigin.DeviceMode
	}
	for _, profile := range b.Profiles {
		if profile.ID == wanted {
			a.Snapshot.Implementation.PipelineID = profile.PipelineID
			a.Snapshot.Implementation.PipelineVersionID = profile.VersionID
			a.Snapshot.Implementation.PipelineIRSHA256 = profile.IRSHA256
		}
	}
	plan, err := biz.CompileRuntimePlan(a.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshot.RuntimePlan = plan
	a.SpecHash, err = a.Snapshot.Digest()
	if err != nil {
		t.Fatal("rebound original fixture snapshot", err)
	}
	return a
}
func (f *Fixture) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/apis/v2beta1/runs") && f.RunHandler != nil {
		f.RunHandler.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/apis/authentication.k8s.io/v1/tokenreviews" && f.TokenReviewHandler != nil {
		f.TokenReviewHandler.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/apis/v2beta1/experiments") {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPost {
			var e map[string]any
			if json.NewDecoder(r.Body).Decode(&e) != nil {
				w.WriteHeader(400)
				return
			}
			e["experiment_id"], e["storage_state"] = uuid.NewString(), "AVAILABLE"
			f.experiments[e["experiment_id"].(string)] = e
			_ = json.NewEncoder(w).Encode(e)
			return
		}
		if id := strings.TrimPrefix(r.URL.Path, "/apis/v2beta1/experiments/"); id != r.URL.Path {
			if e, ok := f.experiments[id]; ok {
				_ = json.NewEncoder(w).Encode(e)
				return
			}
			w.WriteHeader(404)
			return
		}
		items := []any{}
		for _, e := range f.experiments {
			items = append(items, e)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"experiments": items})
		return
	}
	for _, profile := range f.profiles {
		if strings.HasSuffix(r.URL.Path, "/versions/"+profile.VersionID) {
			_ = json.NewEncoder(w).Encode(map[string]any{"pipeline_version_id": profile.VersionID, "pipeline_id": profile.PipelineID, "display_name": "ir-" + profile.IRSHA256[:16], "pipeline_spec": map[string]any{"root": map[string]any{}}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/pipelines/"+profile.PipelineID) {
			_ = json.NewEncoder(w).Encode(map[string]any{"pipeline_id": profile.PipelineID, "namespace": "ani-system", "display_name": profile.ID})
			return
		}
	}
	if r.URL.Path == "/preflight" {
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": true})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	gvr := schema.GroupVersionResource{}
	if len(parts) > 2 && parts[0] == "api" {
		gvr.Version = parts[1]
		parts = parts[2:]
	} else if len(parts) > 3 && parts[0] == "apis" {
		gvr.Group, gvr.Version = parts[1], parts[2]
		parts = parts[3:]
	} else {
		w.WriteHeader(404)
		return
	}
	namespace, name := "", ""
	if len(parts) >= 3 && parts[0] == "namespaces" {
		namespace = parts[1]
		parts = parts[2:]
	}
	if len(parts) == 0 {
		w.WriteHeader(404)
		return
	}
	gvr.Resource = parts[0]
	if len(parts) > 1 {
		name = parts[1]
	}
	client := f.Kube.Resource(gvr).Namespace(namespace)
	var result any
	var err error
	switch r.Method {
	case http.MethodGet:
		if name == "" {
			result, err = client.List(r.Context(), metav1.ListOptions{})
		} else {
			result, err = client.Get(r.Context(), name, metav1.GetOptions{})
		}
	case http.MethodPost:
		var o unstructured.Unstructured
		if json.NewDecoder(r.Body).Decode(&o.Object) != nil {
			w.WriteHeader(400)
			return
		}
		result, err = client.Create(r.Context(), &o, metav1.CreateOptions{})
	case http.MethodPatch:
		raw, _ := io.ReadAll(r.Body)
		result, err = client.Patch(r.Context(), name, types.PatchType(r.Header.Get("Content-Type")), raw, metav1.PatchOptions{})
	case http.MethodPut:
		var o unstructured.Unstructured
		if json.NewDecoder(r.Body).Decode(&o.Object) != nil {
			w.WriteHeader(400)
			return
		}
		result, err = client.Update(r.Context(), &o, metav1.UpdateOptions{})
	default:
		w.WriteHeader(405)
		return
	}
	if err != nil {
		var status metav1.Status
		if e, ok := err.(apierrors.APIStatus); ok {
			status = e.Status()
		} else {
			status = metav1.Status{Status: "Failure", Code: 500, Message: "external fixture unavailable"}
		}
		w.WriteHeader(int(status.Code))
		_ = json.NewEncoder(w).Encode(status)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
