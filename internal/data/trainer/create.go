package trainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

var trainJobs = schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}

// CreateTraining is called only by the holder of the durable first creation
// permit. Reading an existing intent must use FindTraining, never another POST.
func (a *Adapter) CreateTraining(ctx context.Context, plan biz.TrainingPlan) (biz.TrainingHandle, error) {
	if ctx == nil {
		return biz.TrainingHandle{}, biz.ErrRuntimeConflict
	}
	if plan.Timing != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, plan.Timing.DeadlineAt)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return biz.TrainingHandle{}, err
		}
	}
	wanted, err := a.trainingRequest(ctx, plan, true)
	if err != nil {
		return biz.TrainingHandle{}, err
	}
	resource := a.client.Resource(trainJobs).Namespace(plan.Workspace.NamespaceName)
	existing, err := resource.Get(ctx, plan.Name, metav1.GetOptions{})
	if err == nil {
		return verifyTrainingObject(plan, wanted, existing)
	}
	if !apierrors.IsNotFound(err) {
		return biz.TrainingHandle{}, biz.ErrTrainingUnavailable
	}
	created, err := resource.Create(ctx, wanted, metav1.CreateOptions{})
	if err != nil {
		// A decoded Kubernetes Status is different from a proxy body, a partial
		// response, conflict or transport failure. Those stay unresolved.
		var response apierrors.APIStatus
		if errors.As(err, &response) && !apierrors.IsUnexpectedServerError(err) {
			status := response.Status()
			if status.APIVersion == "v1" && status.Kind == "Status" && status.Status == metav1.StatusFailure &&
				((status.Code == 400 && status.Reason == metav1.StatusReasonBadRequest) || (status.Code == 403 && status.Reason == metav1.StatusReasonForbidden) || (status.Code == 422 && status.Reason == metav1.StatusReasonInvalid)) {
				return biz.TrainingHandle{}, &biz.TrainingCreationRejection{RequestSHA256: plan.RequestSHA256, NamespaceUID: plan.Workspace.NamespaceUID, StatusCode: status.Code}
			}
		}
		return biz.TrainingHandle{}, biz.ErrTrainingUncertain
	}
	handle, err := verifyTrainingObject(plan, wanted, created)
	if err != nil {
		return biz.TrainingHandle{}, biz.ErrTrainingUncertain
	}
	return handle, nil
}

func (a *Adapter) FindTraining(ctx context.Context, plan biz.TrainingPlan) (biz.TrainingHandle, error) {
	_, handle, err := a.currentTraining(ctx, plan)
	return handle, err
}

func (a *Adapter) currentTraining(ctx context.Context, plan biz.TrainingPlan) (*unstructured.Unstructured, biz.TrainingHandle, error) {
	wanted, err := a.trainingRequest(ctx, plan, false)
	if err != nil {
		return nil, biz.TrainingHandle{}, err
	}
	actual, err := a.client.Resource(trainJobs).Namespace(plan.Workspace.NamespaceName).Get(ctx, plan.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, biz.TrainingHandle{}, biz.ErrTrainingNotFound
	}
	if err != nil {
		return nil, biz.TrainingHandle{}, biz.ErrTrainingUnavailable
	}
	handle, err := verifyTrainingObject(plan, wanted, actual)
	return actual, handle, err
}

func (a *Adapter) StopTraining(ctx context.Context, plan biz.TrainingPlan, handle biz.TrainingHandle) error {
	actual, current, err := a.readBoundTrainingForClose(ctx, plan, handle)
	if err != nil {
		return err
	}
	if current != handle || actual.GetResourceVersion() == "" {
		return biz.ErrRuntimeConflict
	}
	suspended, _, err := unstructured.NestedBool(actual.Object, "spec", "suspend")
	if err != nil {
		return biz.ErrRuntimeConflict
	}
	if suspended {
		return nil
	}
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": handle.TrainJobUID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": actual.GetResourceVersion()},
		{"op": "add", "path": "/spec/suspend", "value": true},
	})
	if err != nil {
		return biz.ErrTrainingUnavailable
	}
	result, err := a.client.Resource(trainJobs).Namespace(plan.Workspace.NamespaceName).Patch(ctx, plan.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return biz.ErrTrainingUnavailable
	}
	if result == nil || string(result.GetUID()) != handle.TrainJobUID {
		return biz.ErrRuntimeConflict
	}
	suspended, _, err = unstructured.NestedBool(result.Object, "spec", "suspend")
	if err != nil || !suspended {
		return biz.ErrTrainingUnavailable
	}
	// Acceptance closes no business state. ObserveTraining must still verify
	// controllers and every currently known or historical writer.
	return nil
}

// readBoundTrainingForClose identifies the original computation independently
// of the current Runtime and PVC. Those objects govern new computation; their
// deletion or drift must not prevent revoking an already bound TrainJob.
func (a *Adapter) readBoundTrainingForClose(ctx context.Context, plan biz.TrainingPlan, handle biz.TrainingHandle) (*unstructured.Unstructured, biz.TrainingHandle, error) {
	if a == nil || a.client == nil || ctx == nil || ctx.Err() != nil || !validTrainingPlan(plan) ||
		handle.TrainJobUID == "" || handle.NamespaceUID != plan.Workspace.NamespaceUID || handle.PVCUID != plan.Workspace.PVCUID {
		return nil, biz.TrainingHandle{}, biz.ErrRuntimeConflict
	}
	namespace, err := a.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, plan.Workspace.NamespaceName, metav1.GetOptions{})
	if err != nil {
		return nil, biz.TrainingHandle{}, biz.ErrTrainingUnavailable
	}
	if !liveIdentity(namespace, "v1", "Namespace", "", plan.Workspace.NamespaceName, handle.NamespaceUID) {
		return nil, biz.TrainingHandle{}, biz.ErrRuntimeConflict
	}
	actual, err := a.client.Resource(trainJobs).Namespace(plan.Workspace.NamespaceName).Get(ctx, plan.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, biz.TrainingHandle{}, biz.ErrTrainingNotFound
	}
	if err != nil {
		return nil, biz.TrainingHandle{}, biz.ErrTrainingUnavailable
	}
	if actual == nil || actual.GetAPIVersion() != "trainer.kubeflow.org/v1alpha1" || actual.GetKind() != "TrainJob" ||
		actual.GetNamespace() != plan.Workspace.NamespaceName || actual.GetName() != plan.Name || string(actual.GetUID()) != handle.TrainJobUID {
		return nil, biz.TrainingHandle{}, biz.ErrRuntimeConflict
	}
	for key, expected := range map[string]string{
		"modeldev.ani.io/tenant-id":               plan.TenantID,
		"modeldev.ani.io/execution-id":            plan.ExecutionID,
		"modeldev.ani.io/execution-spec-sha256":   plan.SpecHash,
		"modeldev.ani.io/training-request-sha256": plan.RequestSHA256,
		"modeldev.ani.io/workspace-uid":           handle.PVCUID,
	} {
		if actual.GetAnnotations()[key] != expected {
			return nil, biz.TrainingHandle{}, biz.ErrRuntimeConflict
		}
	}
	return actual, handle, nil
}

func (a *Adapter) trainingRequest(ctx context.Context, plan biz.TrainingPlan, requireExitRetention bool) (*unstructured.Unstructured, error) {
	if a == nil || a.client == nil || ctx == nil || ctx.Err() != nil || !validTrainingPlan(plan) {
		return nil, biz.ErrRuntimeConflict
	}
	workspace, snapshot := plan.Workspace, plan.Snapshot
	namespace, err := a.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, workspace.NamespaceName, metav1.GetOptions{})
	if err != nil {
		return nil, biz.ErrTrainingUnavailable
	}
	if !liveIdentity(namespace, "v1", "Namespace", "", workspace.NamespaceName, workspace.NamespaceUID) {
		return nil, biz.ErrRuntimeConflict
	}
	pvc, err := a.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(workspace.NamespaceName).Get(ctx, workspace.PVCName, metav1.GetOptions{})
	if err != nil {
		return nil, biz.ErrTrainingUnavailable
	}
	phase, _, _ := unstructured.NestedString(pvc.Object, "status", "phase")
	storageClass, _, _ := unstructured.NestedString(pvc.Object, "spec", "storageClassName")
	if !liveIdentity(pvc, "v1", "PersistentVolumeClaim", workspace.NamespaceName, workspace.PVCName, workspace.PVCUID) || phase != "Bound" || storageClass != snapshot.Workspace.StorageClass {
		return nil, biz.ErrRuntimeConflict
	}
	wantedRuntime, err := a.runtimeRequest(ctx, plan, false)
	if err != nil {
		return nil, err
	}
	runtime, err := a.client.Resource(trainingRuntimes).Namespace(workspace.NamespaceName).Get(ctx, snapshot.RuntimePlan.Name, metav1.GetOptions{})
	if err != nil {
		return nil, biz.ErrTrainingUnavailable
	}
	binding, err := verifyRuntimeTemplate(plan, wantedRuntime, runtime)
	if err != nil || binding != plan.RuntimeBinding {
		return nil, biz.ErrRuntimeConflict
	}

	vector, vectorErr := biz.TrainingResourceVector(snapshot)
	if vectorErr != nil {
		return nil, biz.ErrRuntimeConflict
	}
	resources := map[string]any{}
	for key, value := range vector {
		resources[key] = value
	}
	annotations := map[string]any{"modeldev.ani.io/tenant-id": plan.TenantID, "modeldev.ani.io/execution-id": plan.ExecutionID, "modeldev.ani.io/execution-spec-sha256": plan.SpecHash, "modeldev.ani.io/training-request-sha256": plan.RequestSHA256, "modeldev.ani.io/workspace-uid": workspace.PVCUID, "modeldev.ani.io/runtime-spec-sha256": snapshot.RuntimePlan.ExpectedSpecSHA256}
	wanted := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainJob",
		"metadata": map[string]any{"name": plan.Name, "namespace": workspace.NamespaceName, "annotations": annotations},
		"spec": map[string]any{
			"runtimeRef": map[string]any{"name": snapshot.RuntimePlan.Name, "kind": snapshot.RuntimePlan.Kind, "apiGroup": "trainer.kubeflow.org"},
			"managedBy":  "trainer.kubeflow.org/trainjob-controller", "suspend": false,
			"trainer": map[string]any{"image": snapshot.Program.ImageDigest, "command": stringValues(snapshot.Program.Command), "args": stringValues(snapshot.Program.ResolvedArgs), "numNodes": int64(1), "numProcPerNode": int64(1), "resourcesPerNode": map[string]any{
				"requests": resources,
				"limits":   resources,
			}},
		},
	}}
	annotations["modeldev.ani.io/training-admitted-at"] = plan.Timing.AdmittedAt.Format(time.RFC3339Nano)
	annotations["modeldev.ani.io/training-deadline-at"] = plan.Timing.DeadlineAt.Format(time.RFC3339Nano)
	annotations["modeldev.ani.io/runtime-uid"] = plan.RuntimeBinding.UID
	annotations["modeldev.ani.io/create-operation-id"] = plan.OperationID

	return wanted, nil
}

func verifyTrainingObject(plan biz.TrainingPlan, wanted, actual *unstructured.Unstructured) (biz.TrainingHandle, error) {
	if actual == nil || actual.GetAPIVersion() != wanted.GetAPIVersion() || actual.GetKind() != "TrainJob" || actual.GetNamespace() != plan.Workspace.NamespaceName || actual.GetName() != plan.Name || actual.GetUID() == "" || actual.GetDeletionTimestamp() != nil {
		return biz.TrainingHandle{}, biz.ErrRuntimeConflict
	}
	for key, value := range wanted.GetAnnotations() {
		if actual.GetAnnotations()[key] != value {
			return biz.TrainingHandle{}, biz.ErrRuntimeConflict
		}
	}
	expectedSpec, _, _ := unstructured.NestedMap(wanted.Object, "spec")
	actualSpec, found, err := unstructured.NestedMap(actual.Object, "spec")
	if err != nil || !found {
		return biz.TrainingHandle{}, biz.ErrRuntimeConflict
	}
	// The one mutable field is suspension. All computation, template, identity
	// and workspace fields must remain the complete original request.
	if suspended, ok := actualSpec["suspend"].(bool); ok {
		expectedSpec["suspend"] = suspended
	}
	expectedBytes, _ := json.Marshal(expectedSpec)
	actualBytes, _ := json.Marshal(actualSpec)
	if string(expectedBytes) != string(actualBytes) {
		return biz.TrainingHandle{}, biz.ErrRuntimeConflict
	}
	return biz.TrainingHandle{NamespaceUID: plan.Workspace.NamespaceUID, TrainJobUID: string(actual.GetUID()), PVCUID: plan.Workspace.PVCUID}, nil
}

func validTrainingPlan(plan biz.TrainingPlan) bool {
	w, s := plan.Workspace, plan.Snapshot
	if !validBinding(biz.TrainJobBinding{TenantID: plan.TenantID, ExecutionID: plan.ExecutionID, SpecSHA256: plan.SpecHash, Namespace: w.NamespaceName, NamespaceUID: w.NamespaceUID, Name: plan.Name, UID: w.PVCUID}) || plan.Name != "md-"+plan.ExecutionID || len(plan.RequestSHA256) != 64 {
		return false
	}
	if _, err := hex.DecodeString(plan.RequestSHA256); err != nil {
		return false
	}
	if w.Mode != "EXECUTION_PVC" || w.Mode != s.Workspace.Mode || w.NamespaceName != s.Environment.NamespaceName || w.NamespaceUID != s.Environment.NamespaceUID || len(validation.IsDNS1123Subdomain(w.PVCName)) != 0 || len(validation.IsDNS1123Subdomain(s.Environment.Identities.TrainerServiceAccount)) != 0 {
		return false
	}
	if w.InputSubpath != s.Workspace.InputSubpath || w.TrainingSubpath != s.Workspace.TrainingSubpath || w.ReportsSubpath != s.Workspace.ReportsSubpath || w.PublicationSubpath != s.Workspace.PublicationSubpath || w.InputSubpath == w.TrainingSubpath {
		return false
	}
	for _, subpath := range []string{w.InputSubpath, w.TrainingSubpath} {
		if subpath == "" || path.IsAbs(subpath) || path.Clean(subpath) != subpath || subpath == ".." || strings.HasPrefix(subpath, "../") {
			return false
		}
	}
	if s.RuntimePlan.APIGroup != "trainer.kubeflow.org" || s.RuntimePlan.Kind != "TrainingRuntime" || len(validation.IsDNS1123Subdomain(s.RuntimePlan.Name)) != 0 || len(s.RuntimePlan.TargetJobs) != 1 || len(validation.IsDNS1123Label(s.RuntimePlan.TargetJobs[0])) != 0 {
		return false
	}
	if len(s.Program.Command) == 0 || !strings.Contains(s.Program.ImageDigest, "@sha256:") || s.Resources.Nodes != 1 || s.Resources.ProcessesPerNode != 1 || s.Resources.RequestMillicpu <= 0 || s.Resources.LimitMillicpu < s.Resources.RequestMillicpu || s.Resources.RequestMemoryBytes <= 0 || s.Resources.LimitMemoryBytes < s.Resources.RequestMemoryBytes {
		return false
	}
	if plan.Timing == nil || plan.Timing.AdmittedAt.IsZero() || plan.RuntimeBinding.UID == "" || plan.RuntimeBinding.NamespaceUID != w.NamespaceUID || plan.RuntimeBinding.Name != s.RuntimePlan.Name || plan.RuntimeBinding.ObservedSpecSHA256 != s.RuntimePlan.ExpectedSpecSHA256 {
		return false
	}
	deadline, err := s.Timing.DeadlineFor(plan.Timing.AdmittedAt)
	if err != nil || !deadline.Equal(plan.Timing.DeadlineAt) || !deadline.After(plan.Timing.AdmittedAt) {
		return false
	}
	if s.Kind == "FINETUNING" && (s.Finetuning == nil || w.InputSubpath != "inputs" || w.TrainingSubpath != "training" || w.ReportsSubpath != "reports" || w.PublicationSubpath != "publication") {
		return false
	}
	if s.IdentityRef.TenantID != plan.TenantID || s.IdentityRef.ExecutionID != plan.ExecutionID || s.IdentityRef.OperationID != plan.OperationID {
		return false
	}
	specHash, err := s.Digest()
	if err != nil || specHash != plan.SpecHash {
		return false
	}
	expected := plan
	expected.RequestSHA256 = ""
	raw, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != plan.RequestSHA256 {
		return false
	}

	return true
}

func stringValues(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

func liveIdentity(object *unstructured.Unstructured, apiVersion, kind, namespace, name, uid string) bool {
	return object != nil && object.GetAPIVersion() == apiVersion && object.GetKind() == kind && object.GetNamespace() == namespace && object.GetName() == name && string(object.GetUID()) == uid && object.GetDeletionTimestamp() == nil
}

var _ biz.TrainingRuntime = (*Adapter)(nil)
