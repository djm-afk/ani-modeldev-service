package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestDynamicKFPBootstrapUsesCurrentPodMetadata(t *testing.T) {
	var calls atomic.Int32
	client := bootstrapKubernetes(t, &calls, false)
	configFile := bootstrapConfigFile(t)
	for _, step := range []string{"prepare", "train-wait", "collect", "publish", "close"} {
		args := bootstrapArgs(configFile, step)
		if step == "close" {
			args = append(args, "--publication-ref", "")
		}
		if step == "prepare" {
			args = append(args, "--pvc-name", "allocated-workspace")
		}
		got, err := bootstrapInvocation(context.Background(), args, client)
		if err != nil {
			t.Fatalf("dynamic KFP %s bootstrap failed: %v", step, err)
		}
		a := got.claim.GetAssociation()
		if got.claim.GetIdentity().GetOperationId() != "" || got.claim.GetIdentity().GetExecutionId() != "22222222-2222-4222-8222-222222222222" || got.claim.GetIdentity().GetExecutionSpecHash() != strings.Repeat("a", 64) {
			t.Fatalf("bootstrap must preserve explicit execution/spec and leave operation for owner resolution: %+v", got.claim.Identity)
		}
		if a.GetPodName() != "current-step-pod" || a.GetPodUid() != "44444444-4444-4444-8444-444444444444" || a.GetNamespaceName() != "cpu-execution" || a.GetNamespaceUid() != "55555555-5555-4555-8555-555555555555" || a.GetWorkflowName() != "observed-workflow" || a.GetWorkflowUid() != "66666666-6666-4666-8666-666666666666" {
			t.Fatalf("bootstrap did not use actual Pod/controller identity: %+v", a)
		}
		if got.claim.Step == modeldevv1.PipelineStep_PIPELINE_STEP_UNSPECIFIED {
			t.Fatalf("missing real KFP task/step: %+v", got)
		}
		if step == "close" && (got.publicationRef == nil || *got.publicationRef != "") {
			t.Fatal("empty candidate must reach the close runner's failure path")
		}
		if step == "prepare" && got.config.PVCName != "allocated-workspace" {
			t.Fatal("prepare must pass the allocated PVC name for server validation")
		}
	}
	if calls.Load() != 5 {
		t.Fatalf("all dynamic steps must GET their current Pod, calls=%d", calls.Load())
	}
}

func TestDynamicKFPBootstrapAcceptsCloseOnlyWithoutPublicationCandidate(t *testing.T) {
	var calls atomic.Int32
	client := bootstrapKubernetes(t, &calls, false)
	args := append(bootstrapArgs(bootstrapConfigFile(t), "close"), "--close-only")
	got, err := bootstrapInvocation(context.Background(), args, client)
	if err != nil || !got.closeOnly || got.publicationRef != nil || got.claim.GetStep() != modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE || calls.Load() != 1 {
		t.Fatalf("failure finalizer must use current Pod identity without a missing publication output: %v", err)
	}
}

func TestDynamicKFPBootstrapLeavesTaskIdentityToVerifiedOwner(t *testing.T) {
	var calls atomic.Int32
	client := bootstrapKubernetes(t, &calls, false)
	args := bootstrapArgs(bootstrapConfigFile(t), "publish")
	_, err := bootstrapInvocation(context.Background(), args, client)
	if err != nil || calls.Load() != 1 {
		t.Fatalf("a real KFP task ID must be resolved by the authenticated owner, not a raw launcher placeholder: %v", err)
	}
}

func TestDynamicKFPBootstrapRejectsExplicitInvalidTaskIdentity(t *testing.T) {
	for _, value := range []string{"", "actual-task-id", "contains space", strings.Repeat("a", 257), "任务"} {
		t.Run(value, func(t *testing.T) {
			var calls atomic.Int32
			client := bootstrapKubernetes(t, &calls, false)
			args := bootstrapArgs(bootstrapConfigFile(t), "publish")
			args = append(args, "--task-id", value)
			if _, err := bootstrapInvocation(context.Background(), args, client); err == nil || calls.Load() != 0 {
				t.Fatal("explicit task identity must remain strictly validated before current Pod lookup")
			}
		})
	}
}

func TestDynamicKFPBootstrapRejectsCloseOnlyOutsideFinalizer(t *testing.T) {
	for _, step := range []string{"prepare", "train-wait", "collect", "publish"} {
		t.Run(step, func(t *testing.T) {
			var calls atomic.Int32
			client := bootstrapKubernetes(t, &calls, false)
			args := append(bootstrapArgs(bootstrapConfigFile(t), step), "--close-only")
			if _, err := bootstrapInvocation(context.Background(), args, client); err == nil || calls.Load() != 0 {
				t.Fatal("close-only cannot change another step's role or reach Kubernetes")
			}
		})
	}
	for _, flag := range []string{"--publication-ref", "--publication-ref-file"} {
		t.Run(flag, func(t *testing.T) {
			var calls atomic.Int32
			client := bootstrapKubernetes(t, &calls, false)
			args := append(bootstrapArgs(bootstrapConfigFile(t), "close"), "--close-only", flag, "")
			if _, err := bootstrapInvocation(context.Background(), args, client); err == nil || calls.Load() != 0 {
				t.Fatal("close-only cannot ambiguously accept publication inputs")
			}
		})
	}
}

func TestDynamicKFPBootstrapRejectsDifferentPodUIDAndAmbiguousInputs(t *testing.T) {
	t.Run("wrong Pod UID", func(t *testing.T) {
		var calls atomic.Int32
		client := bootstrapKubernetes(t, &calls, true)
		if _, err := bootstrapInvocation(context.Background(), bootstrapArgs(bootstrapConfigFile(t), "close"), client); err == nil {
			t.Fatal("Downward API UID mismatch must reject before business RPC")
		}
	})
	t.Run("static and dynamic identities conflict", func(t *testing.T) {
		var calls atomic.Int32
		client := bootstrapKubernetes(t, &calls, false)
		configFile := bootstrapConfigFile(t)
		data, err := os.ReadFile(configFile)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if json.Unmarshal(data, &config) != nil {
			t.Fatal("invalid test owner JSON")
		}
		config["context_file"] = filepath.Join(t.TempDir(), "do-not-read.json")
		data, err = json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if os.WriteFile(configFile, data, 0600) != nil {
			t.Fatal("write owner config")
		}
		if _, err := bootstrapInvocation(context.Background(), bootstrapArgs(configFile, "prepare"), client); err == nil {
			t.Fatal("static context and dynamic identities must be mutually exclusive")
		}
		if calls.Load() != 0 {
			t.Fatal("ambiguous owner input reached Kubernetes")
		}
	})
}

func bootstrapConfigFile(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "owner.json")
	contents := `{"tenant_id":"11111111-1111-4111-8111-111111111111","namespace_uid":"55555555-5555-4555-8555-555555555555","token_file":"/var/run/modeldev/token","grpc_target":"modeldev.example.test:443","grpc_server_name":"modeldev.example.test","grpc_ca_file":"/owner/ca.pem","poll_interval_seconds":1,"timeout_seconds":300}`
	if err := os.WriteFile(name, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func bootstrapArgs(config, step string) []string {
	return []string{step, "--config", config, "--execution-id", "22222222-2222-4222-8222-222222222222", "--spec-hash", strings.Repeat("a", 64), "--run-id", "33333333-3333-4333-8333-333333333333"}
}

func bootstrapKubernetes(t *testing.T, calls *atomic.Int32, wrongUID bool) dynamic.Interface {
	t.Helper()
	t.Setenv("ANI_POD_NAME", "current-step-pod")
	t.Setenv("ANI_POD_UID", "44444444-4444-4444-8444-444444444444")
	t.Setenv("ANI_POD_NAMESPACE", "cpu-execution")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/cpu-execution/pods/current-step-pod" || r.Header.Get("Authorization") != "Bearer synthetic-kubernetes-projected-token" {
			t.Errorf("unexpected bootstrap request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		uid := "44444444-4444-4444-8444-444444444444"
		if wrongUID {
			uid = "77777777-7777-4777-8777-777777777777"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": "cpu-execution", "name": "current-step-pod", "uid": uid, "ownerReferences": []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": "observed-workflow", "uid": "66666666-6666-4666-8666-666666666666", "controller": true}}}})
	}))
	t.Cleanup(server.Close)
	client, err := dynamic.NewForConfig(&rest.Config{Host: server.URL, BearerToken: "synthetic-kubernetes-projected-token", TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCurrentKFPBootstrapAcceptsOnlySmallPublicationReference(t *testing.T) {
	var calls atomic.Int32
	client := bootstrapKubernetes(t, &calls, false)
	ref := `{"schema":"ani.modeldev.publication-ref.v3","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size_bytes":"123"}`
	args := append(bootstrapArgs(bootstrapConfigFile(t), "register"), "--publication-ref", ref)
	got, err := bootstrapInvocation(context.Background(), args, client)
	if err != nil || got.config.PublicationRef != ref || calls.Load() != 1 {
		t.Fatalf("current small ref did not reach the reader role: %v", err)
	}
	for _, flag := range []string{"--candidate-json", "--candidate-file"} {
		args := append(bootstrapArgs(bootstrapConfigFile(t), "register"), flag, ref)
		if _, err := bootstrapInvocation(context.Background(), args, client); err == nil {
			t.Fatal("removed inline-candidate flag accepted")
		}
	}
	args = append(bootstrapArgs(bootstrapConfigFile(t), "register"), "--publication-ref", strings.Repeat("x", 1025))
	if _, err := bootstrapInvocation(context.Background(), args, client); err == nil {
		t.Fatal("oversized reference silently accepted/cleared")
	}
}

func TestDynamicKFPBootstrapRejectsRetiredOwnerTaskID(t *testing.T) {
	var calls atomic.Int32
	client := bootstrapKubernetes(t, &calls, false)
	config := bootstrapConfigFile(t)
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		t.Fatal("config")
	}
	values["task_id"] = "untrusted-old-task"
	raw, err = json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrapInvocation(context.Background(), bootstrapArgs(config, "publish"), client); err == nil || calls.Load() != 0 {
		t.Fatal("retired static TaskID reached workload authentication")
	}
}
