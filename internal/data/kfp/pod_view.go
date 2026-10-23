package kfp

import (
	"context"
	"sort"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workloadview"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	stepPodsResource   = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	workflowsResource  = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}
	stepEventsResource = schema.GroupVersionResource{Version: "v1", Resource: "events"}
)

// PodReader reads the KFP orchestration plane of one already-authorized Run.
// It is a separate adapter from the KFP HTTP client on purpose: the Run API's
// pod_name is an Argo node ID and can never address a Kubernetes Pod.
type PodReader struct{ client dynamic.Interface }

func NewPodReader(client dynamic.Interface) *PodReader { return &PodReader{client: client} }

// ListStepPods returns the step Pods whose controller Workflow matches the
// frozen Run authority. A Workflow already removed by garbage collection does
// not invalidate its surviving Pods: when the controller UID still equals the
// authorized UID they are reported as ORPHANED and stay read-only. A Pod whose
// UID cannot be verified is skipped, never shown.
func (reader *PodReader) ListStepPods(ctx context.Context, scope biz.ExecutionWorkloadScope) ([]biz.ExecutionPod, error) {
	if ctx == nil || reader == nil || reader.client == nil || ctx.Err() != nil || !scope.PipelineReadable() || scope.NamespaceName == "" || scope.NamespaceUID == "" {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	namespace, err := reader.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, scope.NamespaceName, metav1.GetOptions{})
	if err != nil {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	if namespace.GetAPIVersion() != "v1" || namespace.GetKind() != "Namespace" || namespace.GetName() != scope.NamespaceName || string(namespace.GetUID()) != scope.NamespaceUID {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	workflow, err := reader.client.Resource(workflowsResource).Namespace(scope.NamespaceName).Get(ctx, scope.WorkflowName, metav1.GetOptions{})
	workflowPresent := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	if workflowPresent && string(workflow.GetUID()) != scope.WorkflowUID {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	candidates, err := reader.client.Resource(stepPodsResource).Namespace(scope.NamespaceName).List(ctx, metav1.ListOptions{
		LabelSelector: "workflows.argoproj.io/workflow=" + scope.WorkflowName,
	})
	if err != nil || candidates.GetContinue() != "" {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	availability := biz.PodAvailabilityLive
	if !workflowPresent {
		availability = biz.PodAvailabilityOrphaned
	}
	result := make([]biz.ExecutionPod, 0, len(candidates.Items))
	for index := range candidates.Items {
		pod := &candidates.Items[index]
		if pod.GetKind() != "Pod" || pod.GetNamespace() != scope.NamespaceName || pod.GetUID() == "" {
			return nil, biz.ErrWorkloadViewUnavailable
		}
		if !controllerIs(pod, "argoproj.io/v1alpha1", "Workflow", scope.WorkflowName, scope.WorkflowUID) {
			continue
		}
		view := workloadview.Pod(pod, biz.PodLayerPipeline, availability)
		view.StepName = stepName(pod)
		view.LogID = biz.StepLogID(scope.ExecutionID, view.UID)
		view.LogAvailable = true
		result = append(result, view)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StepName != result[j].StepName {
			return result[i].StepName < result[j].StepName
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// StepPod re-applies the same verification a list performs, so a log read can
// never address a Pod the list would have hidden.
func (reader *PodReader) StepPod(ctx context.Context, scope biz.ExecutionWorkloadScope, podUID string) (biz.ExecutionPod, error) {
	pods, err := reader.ListStepPods(ctx, scope)
	if err != nil {
		return biz.ExecutionPod{}, err
	}
	for _, pod := range pods {
		if pod.UID == podUID {
			return pod, nil
		}
	}
	return biz.ExecutionPod{}, biz.ErrTrainingLogNotFound
}

// ListWorkflowEvents reads events of the authorized Workflow and its step Pods.
func (reader *PodReader) ListWorkflowEvents(ctx context.Context, scope biz.ExecutionWorkloadScope) ([]biz.ExecutionEvent, error) {
	if ctx == nil || reader == nil || reader.client == nil || ctx.Err() != nil || !scope.PipelineReadable() {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	allowed := make(map[string]biz.ExecutionEvent, 8)
	workflow, err := reader.client.Resource(workflowsResource).Namespace(scope.NamespaceName).Get(ctx, scope.WorkflowName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	if err == nil {
		if string(workflow.GetUID()) != scope.WorkflowUID {
			return nil, biz.ErrWorkloadViewUnavailable
		}
		allowed[scope.WorkflowUID] = biz.ExecutionEvent{InvolvedKind: "Workflow", InvolvedName: scope.WorkflowName, InvolvedUID: scope.WorkflowUID}
	}
	pods, err := reader.ListStepPods(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, pod := range pods {
		allowed[pod.UID] = biz.ExecutionEvent{InvolvedKind: "Pod", InvolvedName: pod.Name, InvolvedUID: pod.UID}
	}
	if len(allowed) == 0 {
		return []biz.ExecutionEvent{}, nil
	}
	events, err := reader.client.Resource(stepEventsResource).Namespace(scope.NamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil || events.GetContinue() != "" {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	result := make([]biz.ExecutionEvent, 0, len(events.Items))
	for index := range events.Items {
		event := &events.Items[index]
		involved, known := allowed[workloadview.EventInvolvedUID(event)]
		if !known {
			continue
		}
		view, ok := workloadview.Event(event)
		if !ok {
			continue
		}
		view.InvolvedKind, view.InvolvedName, view.InvolvedUID = involved.InvolvedKind, involved.InvolvedName, involved.InvolvedUID
		result = append(result, view)
	}
	return result, nil
}

// stepName is the Argo node name recorded by the workflow controller. The Run
// API's pod_name is deliberately not used: it is a node ID, not a Pod name.
func stepName(pod *unstructured.Unstructured) string {
	if name := pod.GetAnnotations()["workflows.argoproj.io/node-name"]; name != "" {
		return name
	}
	return pod.GetLabels()["workflows.argoproj.io/node-name"]
}

func controllerIs(object *unstructured.Unstructured, apiVersion, kind, name, uid string) bool {
	if uid == "" || controllerUID(object) != uid {
		return false
	}
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			return owner.APIVersion == apiVersion && owner.Kind == kind && owner.Name == name && string(owner.UID) == uid
		}
	}
	return false
}

func controllerUID(object *unstructured.Unstructured) string {
	uid := ""
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			if uid != "" {
				return ""
			}
			uid = string(owner.UID)
		}
	}
	return uid
}
