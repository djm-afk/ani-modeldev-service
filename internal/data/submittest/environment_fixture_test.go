//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/catalogue"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/tenantenvironment"
	envfixture "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/tenantenvironment"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// The state machine and its READY publication commit through the same real PG
// schema as Accept. Only the three external-system transports are substituted.
func (f *completeFixture) ensureEnvironment(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if f.environment == nil {
		environment := envfixture.NewFixture(t, f.request.Admission)
		environment.Config.RegistryAuthority = f.request.Admission.Snapshot.TrainingImage.RegistryAuthority
		files := map[string]string{}
		for i, profile := range environment.Config.Profiles {
			implementation, mode := "builtin.mlp", "cpu"
			if strings.Contains(profile.ID, "sft-lora") {
				implementation = "builtin.sft_lora"
			}
			if strings.HasSuffix(profile.ID, "-gpu") {
				mode = "gpu"
			}
			material, err := catalogue.ReadImplementationMaterial(context.Background(), environment.MaterialsDirectory, implementation, mode)
			if err != nil {
				t.Fatal(err)
			}
			if material.Training != nil {
				material.Training.Command = append([]string{}, f.request.Admission.Snapshot.Program.Command...)
			}
			raw, err := material.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			name := implementation + "." + mode + ".json"
			if err := os.WriteFile(filepath.Join(environment.MaterialsDirectory, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
			environment.Config.Profiles[i].MaterialSHA256 = completeHash(raw)
			files[name] = completeHash(raw)
		}
		manifest, err := json.Marshal(map[string]any{"schema": "ani.modeldev.material-bundle.v1", "profiles": environment.Config.Profiles, "files": files})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(environment.MaterialsDirectory, "manifest.json"), manifest, 0600); err != nil {
			t.Fatal(err)
		}
		environment.Config.MaterialBundleDigest = completeHash(manifest)
		platform, err := tenantenvironment.NewPlatform(environment.Config, environment.Kube, submissionImages{reference: f.image})
		if err != nil {
			t.Fatal(err)
		}
		environment.Platform = platform
		environment.WriteConfig(t)
		f.environment = environment
	}
	f.request.Admission = f.environment.Ensure(t, pool, f.request.Admission)
	f.request.Owner = f.request.Admission.Snapshot.DispatchOwner
	ns := f.request.Admission.Snapshot.Environment.NamespaceName
	// These records are the explicit Kubernetes transport boundary consumed by
	// Step identity and the real webhook, copied from real Ensure's output.
	for _, collection := range []string{"namespaces", "serviceaccounts", "configmaps"} {
		gvr := schema.GroupVersionResource{Version: "v1", Resource: collection}
		if collection == "namespaces" {
			obj, err := f.environment.Kube.Resource(gvr).Get(context.Background(), ns, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			f.objects["/api/v1/namespaces/"+ns] = obj.Object
			continue
		}
		names := []string{"modeldev-step", "trainer-workload", "verifier"}
		if collection == "configmaps" {
			names = []string{"modeldev-step-owner", "ani-kfp-ca", "kfp-launcher", "metadata-grpc-configmap", "modeldev-execution-environment"}
		}
		for _, name := range names {
			obj, err := f.environment.Kube.Resource(gvr).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			f.objects["/api/v1/namespaces/"+ns+"/"+collection+"/"+name] = obj.Object
		}

	}
	refs := []any{}
	for _, binding := range f.request.Admission.Snapshot.ManagedImageAccess.PullBindings {
		refs = append(refs, map[string]any{"name": binding.SecretName})
	}
	for key, obj := range f.objects {
		if strings.HasPrefix(key, "/api/v1/namespaces/"+ns+"/pods/main-") {
			obj["spec"].(map[string]any)["imagePullSecrets"] = refs
		}
	}
	f.freezeWorkflowEnvironment()
}

func (f *completeFixture) freezeWorkflowEnvironment() {
	s := f.request.Admission.Snapshot
	root, _ := json.Marshal(map[string]any{"parameterValues": map[string]string{"execution_id": f.request.Admission.ExecutionID, "spec_hash": f.request.Admission.SpecHash, "environment_binding_id": s.Environment.BindingID, "namespace_uid": s.Environment.NamespaceUID}, "gcsOutputDirectory": s.DispatchOwner.PipelineRoot})
	workflow := f.objects["/apis/argoproj.io/v1alpha1/namespaces/"+s.Environment.NamespaceName+"/workflows/main-flow"]
	workflow["spec"] = map[string]any{"entrypoint": "entrypoint", "templates": []any{map[string]any{"name": "entrypoint", "dag": map[string]any{"tasks": []any{map[string]any{"name": "root-driver", "arguments": map[string]any{"parameters": []any{map[string]any{"name": "runtime-config", "value": string(root)}}}}}}}}}
}

func (f *completeFixture) mutationKube(t *testing.T) dynamic.Interface {
	t.Helper()
	client, err := dynamic.NewForConfig(&rest.Config{Host: f.kube.URL, BearerToken: "synthetic-kube-owner", RateLimiter: f.kubeRateLimiter, Timeout: 5 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.kube.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
