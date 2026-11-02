package tenantenvironment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const testTenant = "11111111-2222-4333-8444-555555555555"
const testCluster = "22222222-3333-4444-8555-666666666666"

type imageEnsurer struct{ calls int }

func (i *imageEnsurer) EnsurePullSecret(context.Context, string, string, string, string, string) (string, error) {
	i.calls++
	return "ani-pull-" + strings.Repeat("a", 32), nil
}
func fixture(t *testing.T) (*Platform, *biz.TenantExecutionEnvironment, *fake.FakeDynamicClient) {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	ak := filepath.Join(dir, "access")
	sk := filepath.Join(dir, "secret")
	for _, file := range []string{token, ak, sk} {
		if e := os.WriteFile(file, []byte("test-token"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	c := Config{ClusterID: testCluster, RegistryAuthority: "registry.example.test", PlatformProject: "platform", ArtifactEndpoint: "https://s3.example.test", ArtifactRegion: "region", ArtifactBucket: "internal-artifacts", ArtifactAccessKeyFile: ak, ArtifactSecretKeyFile: sk, KFPTokenFile: token, KFPConnectionRef: "managed-kfp", KFPCAKey: "ca.crt", KFPPublicCA: "public-ca", MetadataHost: "metadata.kubeflow", MetadataPort: "8080", InputScope: snapshot.PublicationScope, PublicationScope: snapshot.PublicationScope, ModelScope: snapshot.PublicationScope, Workspace: snapshot.Workspace, Timing: cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 1800, ExecutionTimeoutMaxSeconds: 3600, StepTimeoutSeconds: 300}, InputFilePath: "/inputs/dataset-manifest.json", OutputDirectoryPath: "/workspace/training", TargetPlatform: cpup01.TrainingPlatform{OS: "linux", Architecture: "amd64"}, DispatchOwner: cpup01.DispatchOwnerSnapshot{Reference: "managed-owner", RevisionSHA256: strings.Repeat("a", 64)}, MaterialBundleDigest: strings.Repeat("b", 64), ControllerPrincipals: []string{"system:serviceaccount:kubeflow:argo"}, KFPAPIServiceAccount: Subject{Kind: "ServiceAccount", Name: "ml-pipeline", Namespace: "kubeflow"}, StepOwner: StepOwnerConfig{Target: "modeldev:443"}}
	for _, name := range []string{"modeldev-mlp-cpu", "modeldev-mlp-gpu", "modeldev-sft-lora-cpu", "modeldev-sft-lora-gpu"} {
		raw := []byte(`{"root":{}}`)
		file := filepath.Join(dir, name+".json")
		if e := os.WriteFile(file, raw, 0600); e != nil {
			t.Fatal(e)
		}
		hash := biz.EnvironmentDigest(raw)
		c.Profiles = append(c.Profiles, projection.Profile{ID: name, PipelineID: uuid.NewString(), VersionID: uuid.NewString(), IRSHA256: hash, MaterialSHA256: strings.Repeat("d", 64)})
		c.IRSources = append(c.IRSources, IRSource{ProfileID: name, File: file, SHA256: hash})
	}
	row := &biz.TenantExecutionEnvironment{ID: uuid.NewString(), TenantID: testTenant, ClusterID: testCluster, NamespaceName: "tenant-" + testTenant, NamespaceUID: uuid.NewString(), LastOperationID: uuid.NewString(), KFPConnectionRef: c.KFPConnectionRef}
	ns := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "status": map[string]any{"phase": "Active"}}}
	ns.SetName(row.NamespaceName)
	ns.SetUID(types.UID(row.NamespaceUID))
	ns.SetLabels(map[string]string{"network.ani.io/tenant-id": testTenant, "network.ani.io/managed-by": "ani-network-service"})
	objects := []runtime.Object{ns}
	add := func(kind, name string, fields map[string]any) {
		o := &unstructured.Unstructured{Object: map[string]any{"kind": kind, "apiVersion": resource(kind).Group + "/" + resource(kind).Version}}
		o.SetName(name)
		o.SetUID(types.UID(uuid.NewString()))
		o.SetResourceVersion("1")
		for k, v := range fields {
			o.Object[k] = v
		}
		c.Bootstrap = append(c.Bootstrap, BootstrapObject{Kind: kind, Name: name, UID: string(o.GetUID()), SHA256: objectDigest(o)})
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
			o := objects[len(objects)-1].(*unstructured.Unstructured)
			if kind == "ValidatingAdmissionPolicy" {
				o.SetGeneration(1)
				c.Bootstrap[len(c.Bootstrap)-1].SHA256 = objectDigest(o)
			}
		}
	}
	add("MutatingWebhookConfiguration", "ani-modeldev-webhook", map[string]any{"webhooks": []any{}})
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), nil, objects...)
	client.PrependReactor("create", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		create := a.(ktesting.CreateAction)
		o := create.GetObject().(*unstructured.Unstructured)
		o.SetUID(types.UID(uuid.NewString()))
		o.SetResourceVersion("1")
		return false, nil, nil
	})
	p := &Platform{config: c, kube: client, images: &imageEnsurer{}}
	return p, row, client
}
func TestEnvironmentResourcesAndProjectionUseOriginalObjects(t *testing.T) {
	p, row, kube := fixture(t)
	ctx := context.Background()
	checkpoint := func() error { return nil }
	if e := p.Resources(ctx, row, checkpoint); e != nil {
		t.Fatal(e)
	}
	row.ExperimentID = uuid.NewString()
	canonical, e := p.Seal(ctx, row)
	if e != nil {
		t.Fatal(e)
	}
	row.BindingCanonical = canonical
	row.BindingDigest = biz.EnvironmentDigest(canonical)
	if e = p.Publish(ctx, row, checkpoint); e != nil {
		t.Fatal(e)
	}
	if e = p.VerifyPublication(ctx, row); e != nil {
		t.Fatal(e)
	}
	b, e := projection.Decode(canonical)
	if e != nil || b.Environment.BindingID != row.ID || b.InputScope.ApprovedPrefix != "inputs/"+testTenant || b.ModelScope.ApprovedPrefix != testTenant || b.DispatchOwner.PipelineRoot != "s3://internal-artifacts/artifacts/"+testTenant {
		t.Fatal("binding not complete", e)
	}
	for _, forbidden := range []string{"access_key", "secret_key", "test-token", "binding_digest", "resourceVersion"} {
		if strings.Contains(string(canonical), forbidden) {
			t.Fatalf("projection leaked or self referenced %s", forbidden)
		}
	}
	// A retry refreshes values on the original Secret without replacing identity.
	before, _ := p.get(ctx, resource("Secret"), row.NamespaceName, "modeldev-kfp-artifacts")
	if e = os.WriteFile(p.config.ArtifactAccessKeyFile, []byte("rotated-value"), 0600); e != nil {
		t.Fatal(e)
	}
	row.LastOperationID = uuid.NewString()
	if e = p.Resources(ctx, row, checkpoint); e != nil {
		t.Fatal(e)
	}
	after, _ := p.get(ctx, resource("Secret"), row.NamespaceName, "modeldev-kfp-artifacts")
	if before.GetUID() != after.GetUID() {
		t.Fatal("rotation replaced Secret")
	}
	trainer, _ := p.get(ctx, resource("ServiceAccount"), row.NamespaceName, "trainer-workload")
	refs, _, _ := unstructured.NestedSlice(trainer.Object, "imagePullSecrets")
	if len(refs) != 0 || trainer.Object["automountServiceAccountToken"] != false {
		t.Fatal("trainer inherited control identity")
	}
	for _, a := range kube.Actions() {
		if a.GetResource().Resource == "namespaces" && a.GetVerb() != "get" {
			t.Fatal("namespace mutated")
		}
	}
}
func TestEnvironmentWrongNamespaceHasZeroChildWrites(t *testing.T) {
	for _, mode := range []string{"missing", "foreign", "inactive", "recreated"} {
		t.Run(mode, func(t *testing.T) {
			p, row, kube := fixture(t)
			ns, _ := p.get(context.Background(), resource("Namespace"), "", row.NamespaceName)
			switch mode {
			case "missing":
				_ = kube.Resource(resource("Namespace")).Delete(context.Background(), row.NamespaceName, metav1.DeleteOptions{})
			case "foreign":
				ns.SetLabels(map[string]string{"network.ani.io/tenant-id": "other"})
			case "inactive":
				_ = unstructured.SetNestedField(ns.Object, "Terminating", "status", "phase")
			case "recreated":
				ns.SetUID(types.UID(uuid.NewString()))
			}
			if mode != "missing" {
				_, _ = kube.Resource(resource("Namespace")).Update(context.Background(), ns, metav1.UpdateOptions{})
			}
			kube.ClearActions()
			e := p.VerifyNamespace(context.Background(), row)
			if e == nil {
				t.Fatal("wrong namespace accepted")
			}
			for _, a := range kube.Actions() {
				if a.GetVerb() != "get" {
					t.Fatal("namespace failure reached writer")
				}
			}
		})
	}
}
func TestEnvironmentExperimentRecoversUnknownPostAndReadsRotatingToken(t *testing.T) {
	p, row, _ := fixture(t)
	posts := 0
	tokens := []string{}
	created := false
	id := uuid.NewString()
	intent := desiredIntent(row)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		result := experiment{ID: id, Name: intent.Name, Description: intent.Description, Namespace: intent.Namespace, StorageState: "AVAILABLE"}
		if r.Method == "POST" {
			posts++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			var request struct {
				Name        string `json:"display_name"`
				Description string `json:"description"`
				Namespace   string `json:"namespace"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, "invalid v2beta1 body", http.StatusBadRequest)
				return
			}
			if len(request.Description) > 255 {
				t.Errorf("description length %d exceeds original MySQL varchar(255)", len(request.Description))
				http.Error(w, "description too long", http.StatusBadRequest)
				return
			}
			var description map[string]string
			if err := json.Unmarshal([]byte(request.Description), &description); err != nil || len(description) != 6 {
				t.Errorf("description must contain exactly six string fields: %v", err)
				http.Error(w, "invalid ownership description", http.StatusBadRequest)
				return
			}
			for key, value := range map[string]string{
				"s": "ani.modeldev.tenant-environment.v1", "o": "ani-modeldev-service",
				"e": row.ID, "t": row.TenantID, "c": row.ClusterID, "n": row.NamespaceUID,
			} {
				if description[key] != value {
					t.Errorf("description field %q lost its original complete value", key)
					http.Error(w, "incorrect ownership description", http.StatusBadRequest)
					return
				}
			}
			digest := sha256.Sum256(body)
			if request.Name != row.ExperimentName || request.Namespace != row.NamespaceName || row.ExperimentIntent.BodyDigest != hex.EncodeToString(digest[:]) {
				t.Error("original namespace/name or SHA256 of exact HTTP body changed")
				http.Error(w, "incorrect original intent", http.StatusBadRequest)
				return
			}
			created = true
			w.WriteHeader(503)
			return
		}
		if strings.Contains(r.URL.Path, "experiments/") {
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		// The deployed KFP v2beta1 Filter.Predicate uses operation, not v1beta1 op.
		var filter struct {
			Predicates []struct {
				Key         string `json:"key"`
				Operation   string `json:"operation"`
				StringValue string `json:"string_value"`
			} `json:"predicates"`
		}
		decoder := json.NewDecoder(strings.NewReader(r.URL.Query().Get("filter")))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&filter); err != nil {
			t.Errorf("KFP v2beta1 filter rejected: %v", err)
			http.Error(w, "invalid v2beta1 filter", http.StatusBadRequest)
			return
		}
		if len(filter.Predicates) != 1 || filter.Predicates[0].Key != "display_name" || filter.Predicates[0].Operation != "EQUALS" || filter.Predicates[0].StringValue != intent.Name || r.URL.Query().Get("namespace") != intent.Namespace || r.URL.Query().Get("page_size") != "100" {
			t.Error("KFP query lost the original exact name/namespace predicate")
			http.Error(w, "incorrect original intent query", http.StatusBadRequest)
			return
		}
		if created {
			_ = json.NewEncoder(w).Encode(map[string]any{"experiments": []experiment{result}})
		} else {
			_, _ = w.Write([]byte(`{"experiments":[]}`))
		}
	}))
	defer server.Close()
	p.kfp = &experimentClient{endpoint: server.URL, token: PlatformTokenSource{File: p.config.KFPTokenFile}, http: server.Client()}
	if e := p.Experiment(context.Background(), row, func() error { return nil }); e != nil || row.ExperimentID != id || posts != 1 {
		t.Fatalf("unknown create not recovered: %v posts=%d", e, posts)
	}
	if e := os.WriteFile(p.config.KFPTokenFile, []byte("new-token"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := p.Experiment(context.Background(), row, func() error { return nil }); e != nil {
		t.Fatal(e)
	}
	if tokens[len(tokens)-1] != "Bearer new-token" {
		t.Fatal("cached projected token")
	}
}

func TestEnvironmentExperimentPaginationAmbiguityAndUncertainZero(t *testing.T) {
	for _, mode := range []string{"unique-second-page", "duplicate", "foreign", "uncertain-zero", "continue-loop"} {
		t.Run(mode, func(t *testing.T) {
			p, row, _ := fixture(t)
			row.ExperimentName = "md-env-" + row.ID
			row.ExperimentIntent = desiredIntent(row)
			row.ExperimentIntent.Sent = true
			wanted := row.ExperimentIntent
			id := uuid.NewString()
			posts, pages := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					posts++
					w.WriteHeader(500)
					return
				}
				candidate := experiment{ID: id, Name: wanted.Name, Description: wanted.Description, Namespace: wanted.Namespace, StorageState: "AVAILABLE"}
				if strings.Contains(r.URL.Path, "experiments/") {
					_ = json.NewEncoder(w).Encode(candidate)
					return
				}
				pages++
				switch mode {
				case "unique-second-page":
					if r.URL.Query().Get("page_token") == "" {
						_ = json.NewEncoder(w).Encode(map[string]any{"experiments": []experiment{}, "next_page_token": "second"})
					} else {
						_ = json.NewEncoder(w).Encode(map[string]any{"experiments": []experiment{candidate}})
					}
				case "duplicate":
					other := candidate
					other.ID = uuid.NewString()
					_ = json.NewEncoder(w).Encode(map[string]any{"experiments": []experiment{candidate, other}})
				case "foreign":
					candidate.Description = `{"foreign":true}`
					_ = json.NewEncoder(w).Encode(map[string]any{"experiments": []experiment{candidate}})
				case "uncertain-zero":
					_, _ = w.Write([]byte(`{"experiments":[]}`))
				case "continue-loop":
					_, _ = w.Write([]byte(`{"experiments":[],"next_page_token":"again"}`))
				}
			}))
			defer server.Close()
			p.kfp = &experimentClient{endpoint: server.URL, token: PlatformTokenSource{File: p.config.KFPTokenFile}, http: server.Client()}
			e := p.Experiment(context.Background(), row, func() error { return nil })
			if posts != 0 {
				t.Fatal("unknown original result was POSTed again without uniqueness proof")
			}
			if mode == "unique-second-page" {
				if e != nil || row.ExperimentID != id || pages != 2 {
					t.Fatal("complete pagination failed", e, pages)
				}
			} else if e == nil {
				t.Fatal("ambiguous or incomplete experiment accepted")
			}
		})
	}
}
func TestEnvironmentProjectionRejectsUnknownAndUIDReplacement(t *testing.T) {
	p, row, kube := fixture(t)
	if e := p.Resources(context.Background(), row, func() error { return nil }); e != nil {
		t.Fatal(e)
	}
	row.ExperimentID = uuid.NewString()
	raw, e := p.Seal(context.Background(), row)
	if e != nil {
		t.Fatal(e)
	}
	raw = append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, e = projection.Decode(raw); e == nil {
		t.Fatal("unknown binding field accepted")
	}
	original, _ := p.get(context.Background(), resource("ServiceAccount"), row.NamespaceName, "modeldev-step")
	original.SetUID(types.UID(uuid.NewString()))
	_, _ = kube.Resource(resource("ServiceAccount")).Namespace(row.NamespaceName).Update(context.Background(), original, metav1.UpdateOptions{})
	if e = p.Resources(context.Background(), row, func() error { return nil }); e == nil || e.Error() != "ENVIRONMENT_OBJECT_CONFLICT" {
		t.Fatal("replacement SA adopted", e)
	}
}

func TestEnvironmentIRPreservesOriginalBytesAndKFPPlatformDocumentShape(t *testing.T) {
	p, _, _ := fixture(t)
	raw := []byte("root: {}\n---\nplatforms:\n  kubernetes: {}\n")
	file := filepath.Join(t.TempDir(), "ir.yaml")
	if e := os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	hash := biz.EnvironmentDigest(raw)
	p.config.IRSources = []IRSource{{ProfileID: "test", File: file, SHA256: hash}}
	actual, e := p.readIR("test", hash)
	if e != nil {
		t.Fatal(e)
	}
	m, ok := actual.(map[string]any)
	if !ok || m["pipeline_spec"] == nil || m["platform_spec"] == nil || len(m) != 2 {
		t.Fatal("KFP v2.16 document shape lost")
	}
	if e = os.WriteFile(file, append(raw, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = p.readIR("test", hash); e == nil {
		t.Fatal("raw IR byte change accepted")
	}
}

func TestEnvironmentAllFixedCreateKindsRecoverLostResponse(t *testing.T) {
	for _, resourceName := range []string{"serviceaccounts", "rolebindings", "configmaps", "secrets"} {
		t.Run(resourceName, func(t *testing.T) {
			p, row, kube := fixture(t)
			lost, creates := false, 0
			kube.PrependReactor("create", resourceName, func(a ktesting.Action) (bool, runtime.Object, error) {
				creates++
				if lost {
					return false, nil, nil
				}
				lost = true
				create := a.(ktesting.CreateAction)
				o := create.GetObject().(*unstructured.Unstructured)
				o.SetUID(types.UID(uuid.NewString()))
				o.SetResourceVersion("1")
				if e := kube.Tracker().Create(a.GetResource(), o, a.GetNamespace()); e != nil {
					return true, nil, e
				}
				return true, nil, errors.New("response lost")
			})
			if e := p.Resources(context.Background(), row, func() error { return nil }); e != nil {
				t.Fatal(e)
			}
			originalCreates := creates
			row.LastOperationID = uuid.NewString()
			if e := p.Resources(context.Background(), row, func() error { return nil }); e != nil {
				t.Fatal(e)
			}
			if !lost || creates != originalCreates {
				t.Fatal("retry created replacement fixed object")
			}
		})
	}
}
