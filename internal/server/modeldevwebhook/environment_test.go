package modeldevwebhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server/modeldevwebhook"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

type dynamicIdentities struct{ environments map[string]*identities }

func (r *dynamicIdentities) Namespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	if e := r.environments[name]; e != nil {
		return e.Namespace(ctx, name)
	}
	return nil, errors.New("not found")
}
func (r *dynamicIdentities) Environment(ctx context.Context, ns string) (*corev1.ConfigMap, error) {
	if e := r.environments[ns]; e != nil {
		return e.Environment(ctx, ns)
	}
	return nil, errors.New("not found")
}
func (r *dynamicIdentities) ServiceAccount(ctx context.Context, ns, name string) (*corev1.ServiceAccount, error) {
	if e := r.environments[ns]; e != nil {
		return e.ServiceAccount(ctx, ns, name)
	}
	return nil, errors.New("not found")
}
func (r *dynamicIdentities) Workflow(ctx context.Context, ns, name string) (*unstructured.Unstructured, error) {
	if e := r.environments[ns]; e != nil {
		return e.Workflow(ctx, ns, name)
	}
	return nil, errors.New("not found")
}
func TestDynamicEnvironmentAppearsWithoutWebhookRestartAndIsReadOnEveryRequest(t *testing.T) {
	config, a := fixtureConfig(t)
	reader := &dynamicIdentities{environments: map[string]*identities{a.namespace.Name: a}}
	handler, err := modeldevwebhook.New(config, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	body := request(t, config, "modeldev-mlp-cpu", "workspace-name")
	if response := call(t, server, body); !response.Allowed {
		t.Fatal(response.Result)
	}
	_, b := fixtureConfig(t)
	binding, err := projection.Decode([]byte(b.environment.Data["binding.json"]))
	if err != nil {
		t.Fatal(err)
	}
	tenant := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	namespace := "tenant-" + tenant
	uid := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	environmentID := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	binding.TenantID = tenant
	binding.Environment.NamespaceName = namespace
	binding.Environment.NamespaceUID = uid
	binding.Environment.BindingID = environmentID
	binding.InputScope.ApprovedPrefix = "inputs/" + tenant
	binding.PublicationScope.ApprovedPrefix = "executions/" + tenant
	binding.ModelScope.ApprovedPrefix = tenant
	binding.DispatchOwner.PipelineRoot = "s3://test-artifacts/artifacts/" + tenant
	raw, err := binding.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	b.namespace.Name = namespace
	b.namespace.UID = types.UID(uid)
	b.namespace.Labels[modeldevwebhook.NamespaceTenantLabel] = tenant
	b.environment.Namespace = namespace
	b.environment.Labels["modeldev.ani.io/environment-id"] = environmentID
	b.environment.Labels["modeldev.ani.io/tenant-id"] = tenant
	b.environment.Annotations["modeldev.ani.io/namespace-uid"] = uid
	b.environment.Data["binding.json"] = string(raw)
	b.environment.Data["binding.sha256"] = projection.Digest(raw)
	for _, sa := range b.accounts {
		sa.Namespace = namespace
		sa.Labels["modeldev.ani.io/environment-id"] = environmentID
		sa.Labels["modeldev.ani.io/tenant-id"] = tenant
		sa.Annotations["modeldev.ani.io/namespace-uid"] = uid
	}
	b.workflow.SetNamespace(namespace)
	templates, _, _ := unstructured.NestedSlice(b.workflow.Object, "spec", "templates")
	task := templates[0].(map[string]any)["dag"].(map[string]any)["tasks"].([]any)[0].(map[string]any)
	parameter := task["arguments"].(map[string]any)["parameters"].([]any)[0].(map[string]any)
	runtimeConfig, _ := json.Marshal(map[string]any{"gcsOutputDirectory": binding.DispatchOwner.PipelineRoot + "/22222222-2222-4222-8222-222222222222", "parameterValues": map[string]string{"execution_id": "22222222-2222-4222-8222-222222222222", "spec_hash": strings.Repeat("a", 64), "environment_binding_id": environmentID, "namespace_uid": uid}})
	parameter["value"] = string(runtimeConfig)
	_ = unstructured.SetNestedSlice(b.workflow.Object, templates, "spec", "templates")
	body.Request.Namespace = namespace
	changePod(t, &body, func(p *corev1.Pod) { p.Namespace = namespace })
	if response := call(t, server, body); response.Allowed {
		t.Fatal("unregistered live projection accepted")
	}
	reader.environments[namespace] = b
	if response := call(t, server, body); !response.Allowed {
		t.Fatal("new environment required restart", response.Result)
	}
	original := b.environment.Data["binding.sha256"]
	b.environment.Data["binding.sha256"] = strings.Repeat("0", 64)
	if call(t, server, body).Allowed {
		t.Fatal("cached projection hid digest drift")
	}
	b.environment.Data["binding.sha256"] = original
	if !call(t, server, body).Allowed {
		t.Fatal("negative projection cache survived recovery")
	}
	b.accounts["modeldev-step"].UID = types.UID(environmentID)
	if call(t, server, body).Allowed {
		t.Fatal("original SA UID not checked")
	}
}
func TestDriverAndStepBothRejectOldRootParametersAndUnexpectedPullReferences(t *testing.T) {
	for _, driver := range []bool{false, true} {
		t.Run(map[bool]string{false: "step", true: "root-driver"}[driver], func(t *testing.T) {
			config, reader := fixtureConfig(t)
			handler, err := modeldevwebhook.New(config, reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			body := request(t, config, "modeldev-mlp-cpu", "workspace-name")
			if driver {
				changePod(t, &body, func(p *corev1.Pod) {
					delete(p.Labels, modeldevwebhook.ProfileLabel)
					delete(p.Labels, modeldevwebhook.RoleLabel)
					p.Spec.Containers[0].Image = config.Driver.Image
					p.Spec.Containers[0].Command = []string{"driver"}
					p.Spec.Containers[0].Args = []string{"--type", "ROOT_DAG", "--run_id", runID}
				})
			}
			if !call(t, server, body).Allowed {
				t.Fatal("valid original owner refused")
			}
			changePod(t, &body, func(p *corev1.Pod) {
				p.Spec.ImagePullSecrets = append(p.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: "ani-registry-pull-prefix-spoof"})
			})
			if call(t, server, body).Allowed {
				t.Fatal("prefix-spoof pull ref accepted")
			}
			changePod(t, &body, func(p *corev1.Pod) { p.Spec.ImagePullSecrets = p.Spec.ImagePullSecrets[:1] })
			templates, _, _ := unstructured.NestedSlice(reader.workflow.Object, "spec", "templates")
			task := templates[0].(map[string]any)["dag"].(map[string]any)["tasks"].([]any)[0].(map[string]any)
			parameter := task["arguments"].(map[string]any)["parameters"].([]any)[0].(map[string]any)
			parameter["value"] = strings.Replace(parameter["value"].(string), namespaceUID, workflowUID, 1)
			_ = unstructured.SetNestedSlice(reader.workflow.Object, templates, "spec", "templates")
			if call(t, server, body).Allowed {
				t.Fatal("old root Namespace UID accepted")
			}
		})
	}
}
