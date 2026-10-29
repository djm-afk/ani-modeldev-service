package biz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

type capturingEnsurer struct {
	reference string
	err       error
	calls     int
	scope     [5]string
}

func (e *capturingEnsurer) EnsurePullSecret(_ context.Context, tenant, cluster, namespace, namespaceUID, registry string) (string, error) {
	e.calls++
	e.scope = [5]string{tenant, cluster, namespace, namespaceUID, registry}
	if e.err != nil {
		return "", e.err
	}
	return e.reference, nil
}

func trainingPlan() TrainingPlan {
	snapshot := conformance.SnapshotV1()
	return TrainingPlan{TenantID: snapshot.IdentityRef.TenantID, Snapshot: snapshot}
}

// The Ensure call uses exactly the frozen scope, once, and only the
// non-sensitive reference is compared.
func TestEnsurePullSecretUsesTheFrozenScope(t *testing.T) {
	plan := trainingPlan()
	ensurer := &capturingEnsurer{reference: plan.Snapshot.TrainingImage.PullSecretRef}
	runtime := &ManagedRuntime{images: ensurer}
	if err := runtime.ensurePullSecret(context.Background(), plan); err != nil {
		t.Fatalf("matching reference rejected: %v", err)
	}
	environment := plan.Snapshot.Environment
	want := [5]string{plan.TenantID, environment.ClusterID, environment.NamespaceName, environment.NamespaceUID, plan.Snapshot.TrainingImage.RegistryAuthority}
	if ensurer.calls != 1 || ensurer.scope != want {
		t.Fatalf("ensure scope %v (calls=%d) is not the frozen one %v", ensurer.scope, ensurer.calls, want)
	}
}

// Every failure closes before the workload exists and is never retried here.
func TestEnsurePullSecretFailsClosed(t *testing.T) {
	plan := trainingPlan()
	context := context.Background()

	if err := (&ManagedRuntime{}).ensurePullSecret(context, plan); !errors.Is(err, ErrRuntimeNotReady) {
		t.Fatalf("missing ensurer must fail closed: %v", err)
	}
	different := &capturingEnsurer{reference: "ani-pull-" + strings.Repeat("b", 32)}
	if err := (&ManagedRuntime{images: different}).ensurePullSecret(context, plan); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("different Secret reference accepted: %v", err)
	}
	if different.calls != 1 {
		t.Fatalf("conflicting answer was retried: %d", different.calls)
	}
	resourceError := errors.New("resource unavailable")
	failing := &capturingEnsurer{err: resourceError}
	before := string(plan.Snapshot.RuntimePlan.ExpectedSpec)
	if err := (&ManagedRuntime{images: failing}).ensurePullSecret(context, plan); !errors.Is(err, resourceError) {
		t.Fatalf("Resource failure not propagated: %v", err)
	}
	if failing.calls != 1 {
		t.Fatalf("Resource failure was retried: %d", failing.calls)
	}
	if string(plan.Snapshot.RuntimePlan.ExpectedSpec) != before {
		t.Fatal("plan mutated by a failed ensure")
	}
}

// The finetuning chain never enters the shared pull Secret path.
func TestEnsurePullSecretSkipsFinetuning(t *testing.T) {
	ensurer := &capturingEnsurer{reference: "ani-pull-" + strings.Repeat("a", 32)}
	runtime := &ManagedRuntime{images: ensurer}
	snapshot := conformance.FinetuningSnapshotV1()
	if err := runtime.ensurePullSecret(context.Background(), TrainingPlan{TenantID: snapshot.IdentityRef.TenantID, Snapshot: snapshot}); err != nil {
		t.Fatalf("finetuning ensure path rejected: %v", err)
	}
	if ensurer.calls != 0 {
		t.Fatal("finetuning requested a shared pull Secret")
	}
}
