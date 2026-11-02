package stepidentity_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const (
	audience           = "ani-modeldev-managed-step"
	stepToken          = "managed-step-test-token"
	controlToken       = "control-workload-test-token"
	namespaceName      = "tenant-77777777-7777-4777-8777-777777777777"
	namespaceUID       = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	podName            = "cpu-main-prepare"
	podUID             = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	workflowName       = "cpu-main"
	workflowUID        = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	serviceAccount     = "modeldev-step"
	serviceAccountUID  = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	tokenReviewPath    = "/apis/authentication.k8s.io/v1/tokenreviews"
	namespacePath      = "/api/v1/namespaces/" + namespaceName
	podPath            = namespacePath + "/pods/" + podName
	serviceAccountPath = namespacePath + "/serviceaccounts/" + serviceAccount
	workflowPath       = "/apis/argoproj.io/v1alpha1/namespaces/" + namespaceName + "/workflows/" + workflowName
)

func TestVerifyManagedStepUsesAudienceBoundPodIdentityAndLiveObjects(t *testing.T) {
	fixture := newIdentityFixture(t)
	verifier := fixture.verifier(t)
	plan, association := managedStepInputs()
	for attempt := 0; attempt < 2; attempt++ {
		if err := verifier.Verify(context.Background(), stepToken, plan, association); err != nil {
			t.Fatalf("verify current audience-bound managed Pod: %v", err)
		}
	}
	// Neither a prior successful call nor the self-reported association permits
	// skipping current authentication/resource reads on the next callback.
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, path := range []string{tokenReviewPath, namespacePath, podPath, serviceAccountPath, workflowPath} {
		if fixture.calls[path] != 2 {
			t.Errorf("expected two current API checks for %s, got %d", path, fixture.calls[path])
		}
	}
}

func TestVerifyManagedStepRejectsUnboundOrSubstitutedIdentity(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		value  any
		fields []string
	}{
		{"unauthenticated token", tokenReviewPath, false, []string{"status", "authenticated"}},
		{"wrong audience", tokenReviewPath, []any{"kubernetes.default.svc"}, []string{"status", "audiences"}},
		{"training credential", tokenReviewPath, "system:serviceaccount:" + namespaceName + ":cpu-training", []string{"status", "user", "username"}},
		{"unbound service account token", tokenReviewPath, map[string]any{}, []string{"status", "user", "extra"}},
		{"another bound pod", tokenReviewPath, []any{"other-pod"}, []string{"status", "user", "extra", "authentication.kubernetes.io/pod-name"}},
		{"ambiguous pod uid", tokenReviewPath, []any{podUID, "other-pod"}, []string{"status", "user", "extra", "authentication.kubernetes.io/pod-uid"}},
		{"recreated namespace", namespacePath, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", []string{"metadata", "uid"}},
		{"recreated pod", podPath, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", []string{"metadata", "uid"}},
		{"training pod", podPath, "cpu-training", []string{"spec", "serviceAccountName"}},
		{"extra pull reference", podPath, []any{map[string]any{"name": "ani-pull-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, map[string]any{"name": "ani-registry-pull-impostor"}}, []string{"spec", "imagePullSecrets"}},
		{"recreated service account", serviceAccountPath, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", []string{"metadata", "uid"}},
		{"self-reported workflow only", podPath, []any{}, []string{"metadata", "ownerReferences"}},
		{"recreated workflow", workflowPath, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", []string{"metadata", "uid"}},
		{"deleting callback pod", podPath, "2026-10-03T01:00:00Z", []string{"metadata", "deletionTimestamp"}},
	}
	for _, testcase := range cases {
		t.Run(testcase.name, func(t *testing.T) {
			fixture := newIdentityFixture(t)
			if err := unstructured.SetNestedField(fixture.objects[testcase.path], testcase.value, testcase.fields...); err != nil {
				t.Fatal(err)
			}
			plan, association := managedStepInputs()
			err := fixture.verifier(t).Verify(context.Background(), stepToken, plan, association)
			if !errors.Is(err, stepidentity.ErrUnverified) {
				t.Fatalf("unverified callback must be rejected with sanitized error, got %v", err)
			}
		})
	}
}

func TestVerifyManagedStepRejectsDifferentFrozenNamespaceAndSanitizesAPIErrors(t *testing.T) {
	t.Run("different frozen namespace", func(t *testing.T) {
		fixture := newIdentityFixture(t)
		plan, association := managedStepInputs()
		association.NamespaceUID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		err := fixture.verifier(t).Verify(context.Background(), stepToken, plan, association)
		if !errors.Is(err, stepidentity.ErrUnverified) {
			t.Fatalf("different frozen namespace must reject, got %v", err)
		}
	})
	t.Run("API error with credential", func(t *testing.T) {
		fixture := newIdentityFixture(t)
		fixture.failPath = tokenReviewPath
		plan, association := managedStepInputs()
		err := fixture.verifier(t).Verify(context.Background(), stepToken, plan, association)
		if !errors.Is(err, stepidentity.ErrUnverified) || strings.Contains(err.Error(), stepToken) || strings.Contains(err.Error(), controlToken) {
			t.Fatalf("API errors must not leak credentials, got %v", err)
		}
	})
}

func managedStepInputs() (biz.PipelineDispatchPlan, biz.ManagedStepAssociation) {
	return biz.PipelineDispatchPlan{TenantID: "77777777-7777-4777-8777-777777777777", ManagedImageAccess: cpup01.ManagedImageAccessSnapshot{PullBindings: []cpup01.ManagedImagePullBinding{{RegistryAuthority: "registry.example.test", SecretName: "ani-pull-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}, Environment: cpup01.EnvironmentBindingSnapshot{
			NamespaceName: namespaceName, NamespaceUID: namespaceUID,
			Identities: cpup01.RuntimeIdentityRefs{KFPStepServiceAccount: serviceAccount, KFPStepServiceAccountUID: serviceAccountUID},
		}}, biz.ManagedStepAssociation{
			RunID:         "11111111-1111-4111-8111-111111111111",
			NamespaceName: namespaceName, NamespaceUID: namespaceUID,
			WorkflowName: workflowName, WorkflowUID: workflowUID,
			PodName: podName, PodUID: podUID,
		}
}

type identityFixture struct {
	objects  map[string]map[string]any
	failPath string
	mu       sync.Mutex
	calls    map[string]int
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	fixture := &identityFixture{objects: make(map[string]map[string]any), calls: make(map[string]int)}
	for path, source := range map[string]string{
		tokenReviewPath:    `{"apiVersion":"authentication.k8s.io/v1","kind":"TokenReview","status":{"authenticated":true,"audiences":["ani-modeldev-managed-step"],"user":{"username":"system:serviceaccount:tenant-77777777-7777-4777-8777-777777777777:modeldev-step","uid":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","extra":{"authentication.kubernetes.io/pod-name":["cpu-main-prepare"],"authentication.kubernetes.io/pod-uid":["bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"]}}}}`,
		namespacePath:      `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"tenant-77777777-7777-4777-8777-777777777777","uid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","labels":{"network.ani.io/tenant-id":"77777777-7777-4777-8777-777777777777","network.ani.io/managed-by":"ani-network-service"}}}`,
		podPath:            `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"tenant-77777777-7777-4777-8777-777777777777","name":"cpu-main-prepare","uid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","ownerReferences":[{"apiVersion":"argoproj.io/v1alpha1","kind":"Workflow","name":"cpu-main","uid":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","controller":true}]},"spec":{"serviceAccountName":"modeldev-step","imagePullSecrets":[{"name":"ani-pull-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}`,
		serviceAccountPath: `{"apiVersion":"v1","kind":"ServiceAccount","metadata":{"namespace":"tenant-77777777-7777-4777-8777-777777777777","name":"modeldev-step","uid":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"},"imagePullSecrets":[{"name":"ani-pull-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`,
		workflowPath:       `{"apiVersion":"argoproj.io/v1alpha1","kind":"Workflow","metadata":{"namespace":"tenant-77777777-7777-4777-8777-777777777777","name":"cpu-main","uid":"cccccccc-cccc-4ccc-8ccc-cccccccccccc"}}`,
	} {
		var object map[string]any
		if err := json.Unmarshal([]byte(source), &object); err != nil {
			t.Fatal(err)
		}
		fixture.objects[path] = object
	}
	return fixture
}

func (fixture *identityFixture) verifier(t *testing.T) *stepidentity.Verifier {
	t.Helper()
	// Only the external Kubernetes API is substituted. The official dynamic
	// client sends real HTTPS requests and the production verifier decides.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.calls[r.URL.Path]++
		fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+controlToken {
			t.Error("Kubernetes API must use the control workload credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == fixture.failPath {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "message": "test-only error " + stepToken + " " + controlToken, "reason": "Forbidden", "code": 403})
			return
		}
		if r.URL.Path == tokenReviewPath {
			var request map[string]any
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Error("TokenReview must POST a JSON request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			spec, ok := request["spec"].(map[string]any)
			if request["apiVersion"] != "authentication.k8s.io/v1" || request["kind"] != "TokenReview" || !ok || spec["token"] != stepToken || !reflect.DeepEqual(spec["audiences"], []any{audience}) {
				t.Error("TokenReview must explicitly request the managed-step audience and caller token")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		} else if r.Method != http.MethodGet {
			t.Errorf("resource identity inspection used method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		object, ok := fixture.objects[r.URL.Path]
		if !ok {
			t.Errorf("unexpected Kubernetes API path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewEncoder(w).Encode(object); err != nil {
			t.Errorf("write Kubernetes response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	client, err := dynamic.NewForConfig(&rest.Config{
		Host: server.URL, BearerToken: controlToken,
		TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})},
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := stepidentity.New(client, audience)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}
