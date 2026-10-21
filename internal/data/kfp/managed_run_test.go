package kfp_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
)

func TestManagedRunLegacyFieldStillRequiresFrozenRun(t *testing.T) {
	input := fixtureCreateRequest(t, fixtureAdmission(t))
	association := biz.ManagedStepAssociation{RunID: "55555555-6666-4777-8888-999999999999", PodName: "managed-prepare"}
	// Legacy direct-name compatibility only. The captured-response test below
	// demonstrates why production must resolve real Workflow node references.
	valid := `{"run_id":"55555555-6666-4777-8888-999999999999","experiment_id":"44444444-4444-4444-8444-444444444444","display_name":"md-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","pipeline_version_reference":{"pipeline_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","pipeline_version_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"},"runtime_config":{"parameters":{"execution_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","spec_hash":"972dee14e65202d5d4da7da199cf5f37139b535701a0cb1b3de4d8ef8a5170b9"},"pipeline_root":"s3://fixture-kfp-artifacts/managed-root"},"service_account":"cpu-managed-step","run_details":{"task_details":[{"run_id":"55555555-6666-4777-8888-999999999999","task_id":"managed-prepare-task","display_name":"prepare","pod_name":"managed-prepare"}]}}`
	valid = strings.ReplaceAll(valid, "972dee14e65202d5d4da7da199cf5f37139b535701a0cb1b3de4d8ef8a5170b9", input.Plan.SpecHash)
	var body atomic.Value
	body.Store(valid)
	var calls atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/apis/v2beta1/runs/"+association.RunID || r.Header.Get("Authorization") != "Bearer synthetic-fixture-token" {
			t.Error("managed Run verification did not use the authenticated fixed GetRun endpoint")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	t.Cleanup(peer.Close)
	client := fixtureClient(t, peer, tokenProviderFunc(func(_ context.Context, tenant string, env cpup01.EnvironmentBindingSnapshot) (string, error) {
		if tenant != input.Plan.TenantID || env != input.Plan.Environment {
			t.Error("wrong frozen identity")
		}
		return "synthetic-fixture-token", nil
	}))
	if err := client.VerifyManagedRun(context.Background(), input.Plan, association, "prepare"); err != nil {
		t.Fatalf("valid Run and KFP task association rejected: %v", err)
	}
	for _, invalid := range []string{
		strings.Replace(valid, `"pod_name":"managed-prepare"`, `"pod_name":"unrelated-pod"`, 1),
		strings.Replace(valid, `"display_name":"prepare"`, `"display_name":"train-wait"`, 1),
		strings.Replace(valid, input.Plan.SpecHash, strings.Repeat("f", 64), 1),
		strings.Replace(valid, `"service_account":"cpu-managed-step"`, `"service_account":"ordinary-trainer"`, 1),
		strings.TrimSuffix(valid, "}") + `,"run_details":{"task_details":[]}}`,
	} {
		body.Store(invalid)
		if err := client.VerifyManagedRun(context.Background(), input.Plan, association, "prepare"); err == nil {
			t.Error("unproven Run/task association accepted")
		}
	}
	if calls.Load() != 6 {
		t.Errorf("expected one GET for each verification, got %d", calls.Load())
	}
}

func TestManagedRunPreservesRealKFP216ChildReferencesWithoutInventingPodOrState(t *testing.T) {
	var plan biz.PipelineDispatchPlan
	planRaw, err := os.ReadFile("testdata/cpu-p01-plan-2.16.json")
	if err != nil || json.Unmarshal(planRaw, &plan) != nil {
		t.Fatalf("load captured real dispatch plan: %v", err)
	}
	body, err := os.ReadFile("testdata/cpu-p01-run-2.16.json")
	if err != nil {
		t.Fatal(err)
	}
	var response atomic.Value
	response.Store(body)
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/apis/v2beta1/runs/df5b7167-ac26-4b60-bdca-7652b8036d87" || r.Header.Get("Authorization") != "Bearer synthetic-captured-run-token" {
			t.Error("real response replay must still use authenticated frozen GetRun")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response.Load().([]byte))
	}))
	t.Cleanup(peer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	client, err := kfp.New(kfp.Config{ConnectionRef: plan.Environment.KFPConnectionRef, Endpoint: peer.URL, RootCAs: roots, Timeout: 3 * time.Second}, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "synthetic-captured-run-token", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	run, err := client.GetManagedRun(context.Background(), plan, "df5b7167-ac26-4b60-bdca-7652b8036d87")
	if err != nil {
		t.Fatalf("captured KFP 2.16 response rejected: %v", err)
	}
	if run.State != "FAILED" || len(run.Tasks) != 19 {
		t.Fatalf("captured Run facts changed: %+v", run)
	}
	tasks := make(map[string]kfp.ManagedTask)
	for _, task := range run.Tasks {
		tasks[task.ID] = task
	}
	executor := tasks["25f4d3fc-cb4d-476d-becf-2b60bd887c03"]
	if executor.Name != "executor" || executor.State != "SUCCEEDED" || executor.PodName != "" || len(executor.ChildTasks) != 1 || executor.ChildTasks[0].TaskID != "" || executor.ChildTasks[0].PodName != "general-cpu-rnggt-1467617326" {
		t.Fatalf("KFP child reference must retain the raw node ID, not invent the real long Pod name: %+v", executor)
	}
	named := tasks["35e10c3b-d56f-4260-bdb0-1069ea06f76d"]
	if len(named.ChildTasks) != 2 || named.ChildTasks[0].PodName != "general-cpu-rnggt-807657059" || named.ChildTasks[1].PodName != "general-cpu-rnggt-844071179" || named.PodName != "" {
		t.Fatalf("logical task must retain dependency references without treating them as its own Pod: %+v", named)
	}
	prepared := tasks["8638c484-57e0-4af9-bbb9-02ac7a5c45d6"]
	if prepared.Name != "prepare" || prepared.State != "" {
		t.Fatalf("omitted state must remain unknown, never fabricated as SKIPPED: %+v", prepared)
	}
	var alternate map[string]any
	if err := json.Unmarshal(body, &alternate); err != nil {
		t.Fatal(err)
	}
	childTaskID := "d392ca08-5997-45a5-9c42-87065b788968"
	alternate["run_details"].(map[string]any)["task_details"].([]any)[2].(map[string]any)["child_tasks"] = []any{map[string]any{"task_id": childTaskID}}
	changed, err := json.Marshal(alternate)
	if err != nil {
		t.Fatal(err)
	}
	response.Store(changed)
	withTaskID, err := client.GetManagedRun(context.Background(), plan, "df5b7167-ac26-4b60-bdca-7652b8036d87")
	if err != nil || len(withTaskID.Tasks[2].ChildTasks) != 1 || withTaskID.Tasks[2].ChildTasks[0].TaskID != childTaskID || withTaskID.Tasks[2].ChildTasks[0].PodName != "" {
		t.Fatalf("official child task ID alternative must remain distinct from a node reference: %+v; %v", withTaskID, err)
	}
}

func TestManagedRunRejectsMalformedTaskIdentitiesAndChildReferences(t *testing.T) {
	var plan biz.PipelineDispatchPlan
	planRaw, err := os.ReadFile("testdata/cpu-p01-plan-2.16.json")
	if err != nil || json.Unmarshal(planRaw, &plan) != nil {
		t.Fatal("captured plan unavailable")
	}
	raw, err := os.ReadFile("testdata/cpu-p01-run-2.16.json")
	if err != nil {
		t.Fatal(err)
	}
	var response atomic.Value
	response.Store(raw)
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response.Load().([]byte))
	}))
	t.Cleanup(peer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	client, err := kfp.New(kfp.Config{ConnectionRef: plan.Environment.KFPConnectionRef, Endpoint: peer.URL, RootCAs: roots, Timeout: 3 * time.Second}, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "synthetic-malformed-response-token", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing task run", func(task map[string]any) { delete(task, "run_id") }},
		{"different task run", func(task map[string]any) { task["run_id"] = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" }},
		{"missing task id", func(task map[string]any) { delete(task, "task_id") }},
		{"empty task id", func(task map[string]any) { task["task_id"] = "" }},
		{"control task id", func(task map[string]any) { task["task_id"] = "task\nother" }},
		{"duplicate task id", func(task map[string]any) { task["task_id"] = "1376f8c4-de12-4348-9fc0-5dabcc3f8ee2" }},
		{"missing task name", func(task map[string]any) { delete(task, "display_name") }},
		{"empty task name", func(task map[string]any) { task["display_name"] = "" }},
		{"control task name", func(task map[string]any) { task["display_name"] = "executor\nprepare" }},
		{"children not array", func(task map[string]any) { task["child_tasks"] = map[string]any{"pod_name": "node"} }},
		{"null children", func(task map[string]any) { task["child_tasks"] = nil }},
		{"null child", func(task map[string]any) { task["child_tasks"] = []any{nil} }},
		{"empty child", func(task map[string]any) { task["child_tasks"] = []any{map[string]any{}} }},
		{"both child references", func(task map[string]any) {
			task["child_tasks"] = []any{map[string]any{"pod_name": "node", "task_id": "task"}}
		}},
		{"unknown child reference", func(task map[string]any) { task["child_tasks"] = []any{map[string]any{"other": "node"}} }},
		{"empty node reference", func(task map[string]any) { task["child_tasks"] = []any{map[string]any{"pod_name": ""}} }},
		{"invalid node reference", func(task map[string]any) { task["child_tasks"] = []any{map[string]any{"pod_name": "../other-node"}} }},
		{"wrong node reference type", func(task map[string]any) { task["child_tasks"] = []any{map[string]any{"pod_name": 3}} }},
		{"empty child task id", func(task map[string]any) { task["child_tasks"] = []any{map[string]any{"task_id": ""}} }},
		{"duplicate child reference", func(task map[string]any) {
			task["child_tasks"] = []any{map[string]any{"pod_name": "node"}, map[string]any{"pod_name": "node"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			tasks := body["run_details"].(map[string]any)["task_details"].([]any)
			test.edit(tasks[2].(map[string]any))
			changed, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			response.Store(changed)
			if _, err := client.GetManagedRun(context.Background(), plan, "df5b7167-ac26-4b60-bdca-7652b8036d87"); !errors.Is(err, kfp.ErrManagedRunUnverified) {
				t.Fatalf("malformed upstream task must be refused before workload mapping: %v", err)
			}
		})
	}
}
