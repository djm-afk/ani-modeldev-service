package trainer_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestObserveRejectsInvalidBindingBeforeRequest(t *testing.T) {
	cases := []struct {
		name   string
		change func(*biz.TrainJobBinding)
	}{
		{"invalid tenant", func(b *biz.TrainJobBinding) { b.TenantID = "not-a-tenant" }},
		{"nil tenant", func(b *biz.TrainJobBinding) { b.TenantID = "00000000-0000-0000-0000-000000000000" }},
		{"invalid execution", func(b *biz.TrainJobBinding) { b.ExecutionID = "not-an-execution" }},
		{"short spec digest", func(b *biz.TrainJobBinding) { b.SpecSHA256 = "aabb" }},
		{"nonhex spec digest", func(b *biz.TrainJobBinding) { b.SpecSHA256 = "z" + b.SpecSHA256[1:] }},
		{"invalid namespace", func(b *biz.TrainJobBinding) { b.Namespace = "../other" }},
		{"invalid resource name", func(b *biz.TrainJobBinding) { b.Name = "other/resource" }},
		{"missing namespace UID", func(b *biz.TrainJobBinding) { b.NamespaceUID = "" }},
		{"missing TrainJob UID", func(b *biz.TrainJobBinding) { b.UID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newObservationFixture()
			adapter, requests := observationAPI(t, fixture)
			binding := observationBinding()
			tc.change(&binding)
			got, err := adapter.ObserveTrainJob(context.Background(), binding)
			if !errors.Is(err, trainer.ErrInvalidBinding) || got != (biz.TrainJobObservation{}) {
				t.Fatalf("invalid binding must return no usable observation: got=%+v err=%v", got, err)
			}
			if requests.namespace.Load() != 0 || requests.job.Load() != 0 {
				t.Fatalf("invalid binding reached Kubernetes: namespace=%d job=%d", requests.namespace.Load(), requests.job.Load())
			}
		})
	}
}

func TestObserveRejectsForeignOrRecreatedResources(t *testing.T) {
	cases := []struct {
		name        string
		change      func(*observationFixture)
		jobRequests int32
	}{
		{"recreated namespace", func(f *observationFixture) { f.namespace["metadata"].(map[string]any)["uid"] = "another-namespace-uid" }, 0},
		{"wrong namespace name", func(f *observationFixture) { f.namespace["metadata"].(map[string]any)["name"] = "another-namespace" }, 0},
		{"wrong namespace kind", func(f *observationFixture) { f.namespace["kind"] = "Pod" }, 0},
		{"recreated TrainJob", func(f *observationFixture) { f.job["metadata"].(map[string]any)["uid"] = "another-job-uid" }, 1},
		{"foreign TrainJob namespace", func(f *observationFixture) { f.job["metadata"].(map[string]any)["namespace"] = "another-namespace" }, 1},
		{"foreign TrainJob name", func(f *observationFixture) { f.job["metadata"].(map[string]any)["name"] = "another-job" }, 1},
		{"wrong TrainJob kind", func(f *observationFixture) { f.job["kind"] = "JobSet" }, 1},
		{"wrong TrainJob API", func(f *observationFixture) { f.job["apiVersion"] = "trainer.kubeflow.org/v9" }, 1},
		{"foreign tenant", func(f *observationFixture) {
			f.annotations()["modeldev.ani.io/tenant-id"] = "33333333-3333-4333-8333-333333333333"
		}, 1},
		{"foreign execution", func(f *observationFixture) {
			f.annotations()["modeldev.ani.io/execution-id"] = "33333333-3333-4333-8333-333333333333"
		}, 1},
		{"different frozen spec", func(f *observationFixture) {
			f.annotations()["modeldev.ani.io/execution-spec-sha256"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}, 1},
		{"missing ownership", func(f *observationFixture) { delete(f.job["metadata"].(map[string]any), "annotations") }, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newObservationFixture()
			tc.change(fixture)
			adapter, requests := observationAPI(t, fixture)
			got, err := adapter.ObserveTrainJob(context.Background(), observationBinding())
			if !errors.Is(err, trainer.ErrBindingMismatch) || got != (biz.TrainJobObservation{}) {
				t.Fatalf("unbound resource must not report its Complete=True: got=%+v err=%v", got, err)
			}
			if requests.namespace.Load() != 1 || requests.job.Load() != tc.jobRequests {
				t.Fatalf("unexpected read boundary: namespace=%d job=%d, want 1/%d", requests.namespace.Load(), requests.job.Load(), tc.jobRequests)
			}
		})
	}
}

func TestObserveKeepsUnavailableConditionsUnknown(t *testing.T) {
	cases := []struct {
		name       string
		conditions []any
	}{
		{"missing status", nil},
		{"empty conditions", []any{}},
		{"explicit unknown", []any{observationCondition("Complete", "Unknown", 7)}},
		{"unrelated true condition", []any{observationCondition("Created", "True", 7)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newObservationFixture()
			if tc.conditions == nil {
				delete(fixture.job, "status")
			} else {
				fixture.job["status"] = map[string]any{"conditions": tc.conditions}
			}
			adapter, _ := observationAPI(t, fixture)
			got, err := adapter.ObserveTrainJob(context.Background(), observationBinding())
			if err != nil {
				t.Fatalf("read bound resource with unavailable conditions: %v", err)
			}
			if got.Suspended != biz.TrainingConditionUnknown || got.Complete != biz.TrainingConditionUnknown || got.Failed != biz.TrainingConditionUnknown {
				t.Fatalf("unavailable current condition became a confirmed state: %+v", got)
			}
			binding := observationBinding()
			if got.TrainJobUID != binding.UID || got.NamespaceUID != binding.NamespaceUID || got.Generation != 7 || got.ObservedAt.IsZero() {
				t.Fatalf("unknown conditions lost observed resource identity: %+v", got)
			}
		})
	}
}

func TestObservePreservesUpstreamControllerReportsAndTheirGenerationMetadata(t *testing.T) {
	// Trainer v2.1.0/pkg/controller/trainjob_controller.go:171-200 emits Suspended
	// without observedGeneration; pkg/runtime/framework/plugins/jobset/jobset.go:285-298
	// copies JobSet conditions. JobSet v0.10.1/pkg/controllers/jobset_controller.go:935-945 emits Completed
	// without that field. These are source-derived API payloads, not live evidence.
	// The former stale/future/unversioned Unknown cases assumed this optional field
	// was always a TrainJob generation. Preserve the reports instead of that premise.
	cases := []struct {
		name              string
		payload           string
		suspended         biz.TrainingConditionStatus
		complete          biz.TrainingConditionStatus
		failed            biz.TrainingConditionStatus
		suspendedMetadata biz.TrainingConditionMetadata
		completeMetadata  biz.TrainingConditionMetadata
		failedMetadata    biz.TrainingConditionMetadata
	}{
		{
			name:      "Trainer suspended without observed generation",
			payload:   `[{"type":"Suspended","status":"True","reason":"Suspended","message":"TrainJob is suspended","lastTransitionTime":"2026-09-30T09:00:00Z"}]`,
			suspended: biz.TrainingConditionTrue, complete: biz.TrainingConditionUnknown, failed: biz.TrainingConditionUnknown,
			suspendedMetadata: biz.TrainingConditionMetadata{Present: true},
		},
		{
			name:      "Trainer resumed without observed generation",
			payload:   `[{"type":"Suspended","status":"False","reason":"Resumed","message":"TrainJob is resumed","lastTransitionTime":"2026-09-30T09:00:00Z"}]`,
			suspended: biz.TrainingConditionFalse, complete: biz.TrainingConditionUnknown, failed: biz.TrainingConditionUnknown,
			suspendedMetadata: biz.TrainingConditionMetadata{Present: true},
		},
		{
			name:      "JobSet completion copied into TrainJob",
			payload:   `[{"type":"Complete","status":"True","reason":"AllJobsCompleted","message":"jobset completed successfully","lastTransitionTime":"2026-09-30T09:00:00Z"}]`,
			suspended: biz.TrainingConditionUnknown, complete: biz.TrainingConditionTrue, failed: biz.TrainingConditionUnknown,
			completeMetadata: biz.TrainingConditionMetadata{Present: true},
		},
		{
			name:      "JobSet failure copied into TrainJob",
			payload:   `[{"type":"Failed","status":"True","reason":"FailedJobs","message":"jobset failed due to one or more job failures","lastTransitionTime":"2026-09-30T09:00:00Z"}]`,
			suspended: biz.TrainingConditionUnknown, complete: biz.TrainingConditionUnknown, failed: biz.TrainingConditionTrue,
			failedMetadata: biz.TrainingConditionMetadata{Present: true},
		},
		{
			name:      "explicit zero generation remains present",
			payload:   `[{"type":"Complete","status":"True","observedGeneration":0,"reason":"Reported","lastTransitionTime":"2026-09-30T09:00:00Z"}]`,
			suspended: biz.TrainingConditionUnknown, complete: biz.TrainingConditionTrue, failed: biz.TrainingConditionUnknown,
			completeMetadata: biz.TrainingConditionMetadata{Present: true, HasObservedGeneration: true},
		},
		{
			name:      "lower generation does not erase reported true",
			payload:   `[{"type":"Complete","status":"True","observedGeneration":6,"reason":"Reported","lastTransitionTime":"2026-09-30T09:00:00Z"}]`,
			suspended: biz.TrainingConditionUnknown, complete: biz.TrainingConditionTrue, failed: biz.TrainingConditionUnknown,
			completeMetadata: biz.TrainingConditionMetadata{Present: true, HasObservedGeneration: true, ObservedGeneration: 6},
		},
		{
			name:      "higher generation does not erase reported false",
			payload:   `[{"type":"Complete","status":"False","observedGeneration":8,"reason":"Reported","lastTransitionTime":"2026-09-30T09:00:00Z"}]`,
			suspended: biz.TrainingConditionUnknown, complete: biz.TrainingConditionFalse, failed: biz.TrainingConditionUnknown,
			completeMetadata: biz.TrainingConditionMetadata{Present: true, HasObservedGeneration: true, ObservedGeneration: 8},
		},
		{
			name:      "explicit unknown remains a reported condition",
			payload:   `[{"type":"Complete","status":"Unknown","reason":"Reported","lastTransitionTime":"2026-09-30T09:00:00Z"}]`,
			suspended: biz.TrainingConditionUnknown, complete: biz.TrainingConditionUnknown, failed: biz.TrainingConditionUnknown,
			completeMetadata: biz.TrainingConditionMetadata{Present: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var conditions []any
			if err := json.Unmarshal([]byte(tc.payload), &conditions); err != nil {
				t.Fatalf("decode source-derived condition fixture: %v", err)
			}
			fixture := newObservationFixture()
			fixture.conditions(conditions)
			adapter, _ := observationAPI(t, fixture)
			got, err := adapter.ObserveTrainJob(context.Background(), observationBinding())
			if err != nil {
				t.Fatalf("observe reported upstream condition: %v", err)
			}
			if got.Suspended != tc.suspended || got.Complete != tc.complete || got.Failed != tc.failed || got.SuspendedMetadata != tc.suspendedMetadata || got.CompleteMetadata != tc.completeMetadata || got.FailedMetadata != tc.failedMetadata {
				t.Fatalf("lost controller report or inferred TrainJob freshness: got=%+v", got)
			}
			if got.Generation != 7 || got.TrainJobUID != observationBinding().UID {
				t.Fatalf("condition metadata changed the bound TrainJob identity: %+v", got)
			}
		})
	}
}

func TestObserveRejectsAmbiguousConditions(t *testing.T) {
	cases := []struct {
		name   string
		change func(*observationFixture)
	}{
		{"missing generation", func(f *observationFixture) { delete(f.job["metadata"].(map[string]any), "generation") }},
		{"invalid status value", func(f *observationFixture) { f.conditions([]any{observationCondition("Complete", "Succeeded", 7)}) }},
		{"malformed status type", func(f *observationFixture) {
			c := observationCondition("Complete", "True", 7)
			c["status"] = 1
			f.conditions([]any{c})
		}},
		{"negative observed generation", func(f *observationFixture) { f.conditions([]any{observationCondition("Complete", "True", -1)}) }},
		{"null observed generation", func(f *observationFixture) {
			c := observationCondition("Complete", "True", 7)
			c["observedGeneration"] = nil
			f.conditions([]any{c})
		}},
		{"fractional observed generation", func(f *observationFixture) {
			c := observationCondition("Complete", "True", 7)
			c["observedGeneration"] = 7.5
			f.conditions([]any{c})
		}},
		{"string observed generation", func(f *observationFixture) {
			c := observationCondition("Complete", "True", 7)
			c["observedGeneration"] = "7"
			f.conditions([]any{c})
		}},
		{"conditions not list", func(f *observationFixture) { f.job["status"] = map[string]any{"conditions": "Complete"} }},
		{"condition not object", func(f *observationFixture) { f.conditions([]any{"Complete"}) }},
		{"duplicate completion", func(f *observationFixture) {
			f.conditions([]any{observationCondition("Complete", "True", 7), observationCondition("Complete", "False", 7)})
		}},
		{"conflicting terminal states", func(f *observationFixture) {
			f.conditions([]any{observationCondition("Complete", "True", 7), observationCondition("Failed", "True", 7)})
		}},
		{"conflicting reports with unrelated generations", func(f *observationFixture) {
			f.conditions([]any{observationCondition("Complete", "True", 6), observationCondition("Failed", "True", 8)})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newObservationFixture()
			tc.change(fixture)
			adapter, _ := observationAPI(t, fixture)
			got, err := adapter.ObserveTrainJob(context.Background(), observationBinding())
			if !errors.Is(err, trainer.ErrInvalidObservation) || got != (biz.TrainJobObservation{}) {
				t.Fatalf("ambiguous controller state must not yield success: got=%+v err=%v", got, err)
			}
		})
	}
}

func TestObservePreservesAPIReadErrors(t *testing.T) {
	cases := []struct {
		name            string
		namespaceStatus int
		jobStatus       int
		isError         func(error) bool
		jobRequests     int32
	}{
		{"namespace absent", http.StatusNotFound, 0, apierrors.IsNotFound, 0},
		{"TrainJob absent", 0, http.StatusNotFound, apierrors.IsNotFound, 1},
		{"TrainJob forbidden", 0, http.StatusForbidden, apierrors.IsForbidden, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newObservationFixture()
			fixture.namespaceStatus = tc.namespaceStatus
			fixture.jobStatus = tc.jobStatus
			adapter, requests := observationAPI(t, fixture)
			got, err := adapter.ObserveTrainJob(context.Background(), observationBinding())
			if !tc.isError(err) || got != (biz.TrainJobObservation{}) {
				t.Fatalf("API error became an observation or lost its error classification: got=%+v err=%v", got, err)
			}
			if requests.namespace.Load() != 1 || requests.job.Load() != tc.jobRequests {
				t.Fatalf("unexpected reads following API error: namespace=%d job=%d", requests.namespace.Load(), requests.job.Load())
			}
		})
	}
}

type observationFixture struct {
	namespace       map[string]any
	job             map[string]any
	namespaceStatus int
	jobStatus       int
}

type observationRequests struct {
	namespace atomic.Int32
	job       atomic.Int32
}

func observationBinding() biz.TrainJobBinding {
	return biz.TrainJobBinding{
		TenantID:     "11111111-1111-4111-8111-111111111111",
		ExecutionID:  "22222222-2222-4222-8222-222222222222",
		SpecSHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Namespace:    "tenant-11111111-1111-4111-8111-111111111111",
		NamespaceUID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Name:         "md-22222222-2222-4222-8222-222222222222",
		UID:          "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	}
}

func observationCondition(kind, status string, generation int) map[string]any {
	return map[string]any{"type": kind, "status": status, "observedGeneration": generation, "reason": "FixtureCondition", "lastTransitionTime": "2026-09-30T09:00:00Z"}
}

func newObservationFixture() *observationFixture {
	binding := observationBinding()
	f := &observationFixture{
		namespace: map[string]any{
			"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{"name": binding.Namespace, "uid": binding.NamespaceUID},
		},
		job: map[string]any{
			"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainJob",
			"metadata": map[string]any{
				"name": binding.Name, "namespace": binding.Namespace, "uid": binding.UID, "generation": 7,
				"annotations": map[string]any{
					"modeldev.ani.io/tenant-id":             binding.TenantID,
					"modeldev.ani.io/execution-id":          binding.ExecutionID,
					"modeldev.ani.io/execution-spec-sha256": binding.SpecSHA256,
				},
			},
		},
	}
	f.conditions([]any{observationCondition("Suspended", "False", 7), observationCondition("Complete", "True", 7), observationCondition("Failed", "False", 7)})
	return f
}

func (f *observationFixture) annotations() map[string]any {
	return f.job["metadata"].(map[string]any)["annotations"].(map[string]any)
}

func (f *observationFixture) conditions(conditions []any) {
	f.job["status"] = map[string]any{"conditions": conditions}
}

func observationAPI(t *testing.T, fixture *observationFixture) (*trainer.Adapter, *observationRequests) {
	t.Helper()
	requests := &observationRequests{}
	// HTTP is the external API seam. No Kubernetes fake client or private
	// adapter helper replaces identity checks or condition interpretation.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("read-only observer issued %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var object map[string]any
		var status int
		switch r.URL.Path {
		case "/api/v1/namespaces/tenant-11111111-1111-4111-8111-111111111111":
			requests.namespace.Add(1)
			object, status = fixture.namespace, fixture.namespaceStatus
		case "/apis/trainer.kubeflow.org/v1alpha1/namespaces/tenant-11111111-1111-4111-8111-111111111111/trainjobs/md-22222222-2222-4222-8222-222222222222":
			requests.job.Add(1)
			object, status = fixture.job, fixture.jobStatus
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != 0 {
			reason := "NotFound"
			if status == http.StatusForbidden {
				reason = "Forbidden"
			}
			object = map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": reason, "code": status}
			w.WriteHeader(status)
		}
		if err := json.NewEncoder(w).Encode(object); err != nil {
			t.Errorf("encode API fixture: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	client, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("construct official API client: %v", err)
	}
	return trainer.New(client), requests
}
