package trainer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kjson "k8s.io/apimachinery/pkg/util/json"
)

var trainingRuntimes = schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainingruntimes"}

func (a *Adapter) CreateRuntime(ctx context.Context, plan biz.TrainingPlan) (biz.RuntimeTemplateBinding, error) {
	wanted, err := a.runtimeRequest(ctx, plan, true)
	if err != nil {
		p := plan.Snapshot.RuntimePlan
		return biz.RuntimeTemplateBinding{}, &biz.RuntimeCreationNotSent{NamespaceUID: plan.Workspace.NamespaceUID, Name: p.Name, ExpectedSpecSHA256: p.ExpectedSpecSHA256, Reason: "PRECONDITION_FAILED_BEFORE_CREATE"}
	}
	resource := a.client.Resource(trainingRuntimes).Namespace(wanted.GetNamespace())
	actual, err := resource.Get(ctx, wanted.GetName(), metav1.GetOptions{})
	if err == nil {
		return verifyRuntimeTemplate(plan, wanted, actual)
	}
	if !apierrors.IsNotFound(err) {
		p := plan.Snapshot.RuntimePlan
		return biz.RuntimeTemplateBinding{}, &biz.RuntimeCreationNotSent{NamespaceUID: plan.Workspace.NamespaceUID, Name: p.Name, ExpectedSpecSHA256: p.ExpectedSpecSHA256, Reason: "PRECONDITION_FAILED_BEFORE_CREATE"}
	}
	actual, err = resource.Create(ctx, wanted, metav1.CreateOptions{})
	if err != nil {
		return biz.RuntimeTemplateBinding{}, biz.ErrTrainingUncertain
	}
	binding, err := verifyRuntimeTemplate(plan, wanted, actual)
	if err != nil {
		return biz.RuntimeTemplateBinding{}, biz.ErrTrainingUncertain
	}
	return binding, nil
}

func (a *Adapter) FindRuntime(ctx context.Context, plan biz.TrainingPlan) (biz.RuntimeTemplateBinding, error) {
	wanted, err := a.runtimeRequest(ctx, plan, false)
	if err != nil {
		return biz.RuntimeTemplateBinding{}, err
	}
	actual, err := a.client.Resource(trainingRuntimes).Namespace(wanted.GetNamespace()).Get(ctx, wanted.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return biz.RuntimeTemplateBinding{}, biz.ErrTrainingNotFound
	}
	if err != nil {
		return biz.RuntimeTemplateBinding{}, biz.ErrTrainingUnavailable
	}
	return verifyRuntimeTemplate(plan, wanted, actual)
}

func (a *Adapter) runtimeRequest(ctx context.Context, plan biz.TrainingPlan, creating bool) (*unstructured.Unstructured, error) {
	if a == nil || a.client == nil || ctx == nil || ctx.Err() != nil {
		return nil, biz.ErrRuntimeConflict
	}
	s := plan.Snapshot
	compiled, err := biz.CompileRuntimePlan(s)
	if err != nil || !reflect.DeepEqual(compiled, s.RuntimePlan) ||
		s.IdentityRef.TenantID != plan.TenantID || s.IdentityRef.ExecutionID != plan.ExecutionID || s.IdentityRef.OperationID != plan.OperationID ||
		plan.Workspace.NamespaceName != s.Environment.NamespaceName || plan.Workspace.NamespaceUID != s.Environment.NamespaceUID ||
		plan.Workspace.PVCName != biz.ExpectedWorkspacePVCName(plan.ExecutionID) || plan.Workspace.PVCUID == "" {
		return nil, biz.ErrRuntimeConflict
	}
	digest, err := s.Digest()
	if err != nil || digest != plan.SpecHash {
		return nil, biz.ErrRuntimeConflict
	}
	namespace, err := a.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, plan.Workspace.NamespaceName, metav1.GetOptions{})
	if err != nil {
		return nil, biz.ErrTrainingUnavailable
	}
	if !liveIdentity(namespace, "v1", "Namespace", "", plan.Workspace.NamespaceName, plan.Workspace.NamespaceUID) {
		return nil, biz.ErrRuntimeConflict
	}
	if creating {
		pvc, err := a.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(plan.Workspace.NamespaceName).Get(ctx, plan.Workspace.PVCName, metav1.GetOptions{})
		if err != nil {
			return nil, biz.ErrTrainingUnavailable
		}
		phase, _, _ := unstructured.NestedString(pvc.Object, "status", "phase")
		if !liveIdentity(pvc, "v1", "PersistentVolumeClaim", plan.Workspace.NamespaceName, plan.Workspace.PVCName, plan.Workspace.PVCUID) || phase != "Bound" {
			return nil, biz.ErrRuntimeConflict
		}
	}
	var spec map[string]any
	if kjson.Unmarshal(s.RuntimePlan.ExpectedSpec, &spec) != nil || spec == nil {
		return nil, biz.ErrRuntimeConflict
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainingRuntime",
		"metadata": map[string]any{"namespace": s.RuntimePlan.Namespace, "name": s.RuntimePlan.Name, "annotations": map[string]any{
			"modeldev.ani.io/tenant-id": plan.TenantID, "modeldev.ani.io/execution-id": plan.ExecutionID,
			"modeldev.ani.io/create-operation-id": plan.OperationID, "modeldev.ani.io/execution-spec-sha256": plan.SpecHash,
			"modeldev.ani.io/runtime-spec-sha256": s.RuntimePlan.ExpectedSpecSHA256, "modeldev.ani.io/workspace-uid": plan.Workspace.PVCUID,
		}}, "spec": spec,
	}}, nil
}

func verifyRuntimeTemplate(plan biz.TrainingPlan, wanted, actual *unstructured.Unstructured) (biz.RuntimeTemplateBinding, error) {
	if actual == nil || actual.GetAPIVersion() != wanted.GetAPIVersion() || actual.GetKind() != "TrainingRuntime" || actual.GetNamespace() != wanted.GetNamespace() || actual.GetName() != wanted.GetName() || actual.GetUID() == "" || actual.GetDeletionTimestamp() != nil || len(actual.GetOwnerReferences()) != 0 {
		return biz.RuntimeTemplateBinding{}, biz.ErrRuntimeConflict
	}
	for key, value := range wanted.GetAnnotations() {
		if actual.GetAnnotations()[key] != value {
			return biz.RuntimeTemplateBinding{}, biz.ErrRuntimeConflict
		}
	}
	spec, found, err := unstructured.NestedMap(actual.Object, "spec")
	if err != nil || !found {
		return biz.RuntimeTemplateBinding{}, biz.ErrRuntimeConflict
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(spec) != nil {
		return biz.RuntimeTemplateBinding{}, biz.ErrRuntimeConflict
	}
	raw := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if !bytes.Equal(raw, plan.Snapshot.RuntimePlan.ExpectedSpec) {
		return biz.RuntimeTemplateBinding{}, biz.ErrRuntimeConflict
	}
	digest := sha256.Sum256(raw)
	return biz.RuntimeTemplateBinding{NamespaceUID: plan.Workspace.NamespaceUID, Name: actual.GetName(), UID: string(actual.GetUID()), ObservedSpecSHA256: hex.EncodeToString(digest[:])}, nil
}
