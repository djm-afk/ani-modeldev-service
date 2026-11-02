package resolutiontest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// File loading, source pins and per-implementation static configuration were
// retired. Admission now reads the real READY registry and checks live UIDs.
func TestDynamicFactsRequiresExactRegisteredTenantAndImplementation(t *testing.T) {
	f := prepareResolution(t)
	for _, tc := range []struct {
		tenant, id string
		want       error
		reason     string
	}{
		{f.request.IdentityRef.TenantID, "builtin.mlp", nil, ""},
		{f.request.IdentityRef.TenantID, "builtin.sft_lora", nil, ""},
		{uuid.NewString(), "builtin.mlp", nil, "ENVIRONMENT_NOT_REGISTERED"},
		{"invalid", "builtin.mlp", biz.ErrInvalidAdmission, ""},
		{f.request.IdentityRef.TenantID, "future.script", biz.ErrInvalidAdmission, ""},
	} {
		facts, err := f.factReader.reader.ReadAdmissionFacts(f.ctx, tc.tenant, tc.id)
		var environment *biz.EnvironmentFailure
		reasonOK := tc.reason != "" && errors.As(err, &environment) && environment.Reason == tc.reason
		if (tc.reason != "" && !reasonOK) || (tc.reason == "" && !errors.Is(err, tc.want)) || ((tc.want != nil || tc.reason != "") && !reflect.DeepEqual(facts, biz.TenantAdmissionFacts{})) {
			t.Fatal("wrong environment authority selected", tc.tenant, tc.id, err)
		}
	}
	requireNoResolvedExecution(t, f)
}
func TestDynamicFactsAndFinalizeRejectRecreatedNamespace(t *testing.T) {
	f := prepareResolution(t)
	candidate, err := f.resolver.Prepare(f.ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	ns := f.environment.Kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	original, err := ns.Get(f.ctx, f.facts.Environment.NamespaceName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	original.SetUID(types.UID(uuid.NewString()))
	if _, err = ns.Update(f.ctx, original, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	facts, err := f.factReader.reader.ReadAdmissionFacts(f.ctx, f.request.IdentityRef.TenantID, f.request.Intent.ImplementationID)
	if err == nil || !reflect.DeepEqual(facts, biz.TenantAdmissionFacts{}) {
		t.Fatal("new namespace UID returned trusted facts", err)
	}
	resolved, err := f.resolver.Finalize(f.ctx, candidate, nil)
	if err == nil || !reflect.DeepEqual(resolved, biz.AdmissionResolution{}) {
		t.Fatal("Finalize accepted replaced namespace", err)
	}
	requireNoResolvedExecution(t, f)
}
func TestDynamicFactsCancellationReturnsNoFactsOrCandidate(t *testing.T) {
	f := prepareResolution(t)
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	facts, err := f.factReader.reader.ReadAdmissionFacts(ctx, f.request.IdentityRef.TenantID, f.request.Intent.ImplementationID)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(facts, biz.TenantAdmissionFacts{}) {
		t.Fatal("canceled read returned facts", err)
	}
	candidate, err := f.resolver.Prepare(ctx, f.request)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(candidate, cpup01.PreparedCandidate{}) {
		t.Fatal("canceled Prepare returned candidate", err)
	}
	requireNoResolvedExecution(t, f)
}
