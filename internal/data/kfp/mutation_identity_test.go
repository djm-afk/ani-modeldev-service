package kfp_test

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCreateRunRejectsReplacedOrInactiveNamespaceBeforeBearerAndPost(t *testing.T) {
	for _, mode := range []string{"replacement", "terminating", "foreign tenant"} {
		t.Run(mode, func(t *testing.T) {
			var calls, tokens atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer server.Close()
			client := fixtureClient(t, server, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
				tokens.Add(1)
				return "synthetic-token", nil
			}))
			identity := fixtureMutationIdentity()
			nsAPI := identity.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
			ns, err := nsAPI.Get(context.Background(), "tenant-11111111-2222-4333-8444-555555555555", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "replacement":
				ns.SetUID(types.UID("99999999-9999-4999-8999-999999999999"))
			case "terminating":
				_ = unstructured.SetNestedField(ns.Object, "Terminating", "status", "phase")
			case "foreign tenant":
				ns.SetLabels(map[string]string{"network.ani.io/tenant-id": "99999999-9999-4999-8999-999999999999", "network.ani.io/managed-by": "ani-network-service"})
			}
			if _, err = nsAPI.Update(context.Background(), ns, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			client.WithMutationIdentity(identity)
			observation, err := client.CreateRun(context.Background(), fixtureCreateRequest(t, fixtureAdmission(t)))
			if err == nil || observation.State != biz.PipelineSubmissionNotSent || calls.Load() != 0 || tokens.Load() != 0 {
				t.Fatalf("foreign original sent: %+v %v posts=%d tokens=%d", observation, err, calls.Load(), tokens.Load())
			}
		})
	}
}
