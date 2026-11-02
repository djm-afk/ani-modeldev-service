package trainer_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestObserveSuspendedTrainJobDoesNotTreatFirstTrueConditionAsCompletion(t *testing.T) {
	// This server is an external Kubernetes API substitute. It proves neither
	// real CRD availability nor target-cluster application identity permissions.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("read-only observation used method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/api/v1/namespaces/tenant-11111111-1111-4111-8111-111111111111":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"tenant-11111111-1111-4111-8111-111111111111","uid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}`)
		case "/apis/trainer.kubeflow.org/v1alpha1/namespaces/tenant-11111111-1111-4111-8111-111111111111/trainjobs/md-22222222-2222-4222-8222-222222222222":
			fmt.Fprint(w, `{
  "apiVersion":"trainer.kubeflow.org/v1alpha1",
  "kind":"TrainJob",
  "metadata":{
    "name":"md-22222222-2222-4222-8222-222222222222",
    "namespace":"tenant-11111111-1111-4111-8111-111111111111",
    "uid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
    "generation":7,
    "annotations":{
      "modeldev.ani.io/tenant-id":"11111111-1111-4111-8111-111111111111",
      "modeldev.ani.io/execution-id":"22222222-2222-4222-8222-222222222222",
      "modeldev.ani.io/execution-spec-sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    }
  },
  "status":{"conditions":[
    {"type":"Suspended","status":"True","observedGeneration":7,"reason":"Suspended","lastTransitionTime":"2026-09-30T09:00:00Z"},
    {"type":"Complete","status":"False","observedGeneration":7,"reason":"NotComplete","lastTransitionTime":"2026-09-30T09:00:00Z"},
    {"type":"Failed","status":"False","observedGeneration":7,"reason":"NotFailed","lastTransitionTime":"2026-09-30T09:00:00Z"}
  ]}
}`)
		default:
			t.Errorf("unexpected Kubernetes API path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("construct official API client: %v", err)
	}
	adapter := trainer.New(client)
	observation, err := adapter.ObserveTrainJob(context.Background(), biz.TrainJobBinding{
		TenantID:     "11111111-1111-4111-8111-111111111111",
		ExecutionID:  "22222222-2222-4222-8222-222222222222",
		SpecSHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Namespace:    "tenant-11111111-1111-4111-8111-111111111111",
		NamespaceUID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Name:         "md-22222222-2222-4222-8222-222222222222",
		UID:          "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
	})
	if err != nil {
		t.Fatalf("observe bound TrainJob: %v", err)
	}
	if observation.Suspended != biz.TrainingConditionTrue || observation.Complete != biz.TrainingConditionFalse || observation.Failed != biz.TrainingConditionFalse {
		t.Fatalf("Suspended=True must not imply completion or failure: %+v", observation)
	}
	if observation.TrainJobUID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || observation.NamespaceUID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || observation.Generation != 7 {
		t.Fatalf("observation lost the exact resource identity/generation: %+v", observation)
	}
}
