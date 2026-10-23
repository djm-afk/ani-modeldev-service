package trainer

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/workloadview"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

var (
	podsResource    = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	jobsResource    = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	jobSetsResource = schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}
	eventsResource  = schema.GroupVersionResource{Version: "v1", Resource: "events"}
	podGroups       = schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}
)

// WorkerLogID is the single place a Training worker log ID is derived. The ID
// stays an opaque canonical UUID; container selection is a separate field.
func WorkerLogID(executionID, podUID string) string {
	return biz.TrainerLogID(executionID, podUID)
}

// ListWorkerPods reads the verified worker layer of one execution. Candidates
// are narrowed by Job labels and then authorized by the complete owner chain
// Pod -> Job -> JobSet -> TrainJob. A candidate that fails any link is skipped
// rather than reported, so a foreign Pod in the same namespace never appears.
func (a *Adapter) ListWorkerPods(ctx context.Context, scope biz.ExecutionWorkloadScope) ([]biz.ExecutionPod, error) {
	if ctx == nil || a == nil || a.client == nil || ctx.Err() != nil {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	if len(validation.IsDNS1123Subdomain(scope.NamespaceName)) != 0 || scope.NamespaceUID == "" ||
		len(validation.IsDNS1035Label(scope.TrainJobName)) != 0 || scope.TrainJobUID == "" {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	namespace, err := a.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, scope.NamespaceName, metav1.GetOptions{})
	if err != nil {
		return nil, biz.ErrTrainingUnavailable
	}
	if namespace.GetAPIVersion() != "v1" || namespace.GetKind() != "Namespace" || namespace.GetName() != scope.NamespaceName || string(namespace.GetUID()) != scope.NamespaceUID {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	jobset, err := a.client.Resource(jobSetsResource).Namespace(scope.NamespaceName).Get(ctx, scope.TrainJobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return []biz.ExecutionPod{}, nil
	}
	if err != nil {
		return nil, biz.ErrTrainingUnavailable
	}
	if !controllerIs(jobset, "trainer.kubeflow.org/v1alpha1", "TrainJob", scope.TrainJobName, scope.TrainJobUID) || jobset.GetKind() != "JobSet" {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	jobs, err := a.client.Resource(jobsResource).Namespace(scope.NamespaceName).List(ctx, metav1.ListOptions{
		LabelSelector: "jobset.sigs.k8s.io/jobset-uid=" + string(jobset.GetUID()),
	})
	if err != nil || jobs.GetContinue() != "" {
		return nil, biz.ErrTrainingUnavailable
	}
	jobUIDs := make(map[string]string, len(jobs.Items))
	for index := range jobs.Items {
		job := &jobs.Items[index]
		if !controllerIs(job, "trainer.kubeflow.org/v1alpha1", "TrainJob", scope.TrainJobName, scope.TrainJobUID) {
			continue
		}
		if job.GetKind() != "Job" || job.GetNamespace() != scope.NamespaceName || job.GetUID() == "" {
			return nil, biz.ErrWorkloadViewUnavailable
		}
		jobUIDs[job.GetName()] = string(job.GetUID())
	}
	if len(jobUIDs) == 0 {
		return []biz.ExecutionPod{}, nil
	}
	names := make([]string, 0, len(jobUIDs))
	for name := range jobUIDs {
		names = append(names, name)
	}
	sort.Strings(names)
	pods, err := a.client.Resource(podsResource).Namespace(scope.NamespaceName).List(ctx, metav1.ListOptions{
		LabelSelector: "batch.kubernetes.io/job-name in (" + strings.Join(names, ",") + ")",
	})
	if err != nil || pods.GetContinue() != "" {
		return nil, biz.ErrTrainingUnavailable
	}
	result := make([]biz.ExecutionPod, 0, len(pods.Items))
	for index := range pods.Items {
		pod := &pods.Items[index]
		ownerName, ownerUID, found := controllerOf(pod)
		if !found {
			continue
		}
		expected, known := jobUIDs[ownerName]
		if !known || ownerUID != expected || !controllerIs(pod, "batch/v1", "Job", ownerName, expected) {
			continue
		}
		if pod.GetKind() != "Pod" || pod.GetNamespace() != scope.NamespaceName || pod.GetUID() == "" {
			return nil, biz.ErrWorkloadViewUnavailable
		}
		view := workloadview.Pod(pod, biz.PodLayerTrainer, biz.PodAvailabilityLive)
		view.JobIndex = jobIndex(pod)
		view.Attempt = jobAttempt(pod)
		view.LogID = WorkerLogID(scope.ExecutionID, view.UID)
		view.LogAvailable = true
		result = append(result, view)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].JobIndex != result[j].JobIndex {
			return result[i].JobIndex < result[j].JobIndex
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// ListTrainingWorkloadEvents reads events of the verified worker/controller
// objects, plus the PodGroup only when the frozen plan schedules one.
func (a *Adapter) ListTrainingWorkloadEvents(ctx context.Context, scope biz.ExecutionWorkloadScope, pods []biz.ExecutionPod) ([]biz.ExecutionEvent, error) {
	if ctx == nil || a == nil || a.client == nil || ctx.Err() != nil || scope.NamespaceName == "" || scope.NamespaceUID == "" {
		return nil, biz.ErrWorkloadViewUnavailable
	}
	allowed := make(map[string]biz.ExecutionEvent, len(pods)+4)
	for _, pod := range pods {
		allowed[pod.UID] = biz.ExecutionEvent{InvolvedKind: "Pod", InvolvedName: pod.Name, InvolvedUID: pod.UID}
	}
	collect := func(uid, name, kind string) {
		if uid == "" {
			return
		}
		allowed[uid] = biz.ExecutionEvent{InvolvedKind: kind, InvolvedName: name, InvolvedUID: uid}
	}
	if scope.TrainJobName != "" && scope.TrainJobUID != "" {
		train, err := a.client.Resource(trainJobs).Namespace(scope.NamespaceName).Get(ctx, scope.TrainJobName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, biz.ErrTrainingUnavailable
		}
		if err == nil && string(train.GetUID()) == scope.TrainJobUID {
			collect(scope.TrainJobUID, scope.TrainJobName, "TrainJob")
			set, setErr := a.client.Resource(jobSetsResource).Namespace(scope.NamespaceName).Get(ctx, scope.TrainJobName, metav1.GetOptions{})
			if setErr != nil && !apierrors.IsNotFound(setErr) {
				return nil, biz.ErrTrainingUnavailable
			}
			if setErr == nil && controllerIs(set, "trainer.kubeflow.org/v1alpha1", "TrainJob", scope.TrainJobName, scope.TrainJobUID) {
				collect(string(set.GetUID()), set.GetName(), "JobSet")
				jobs, listErr := a.client.Resource(jobsResource).Namespace(scope.NamespaceName).List(ctx, metav1.ListOptions{
					LabelSelector: "jobset.sigs.k8s.io/jobset-uid=" + string(set.GetUID()),
				})
				if listErr != nil || jobs.GetContinue() != "" {
					return nil, biz.ErrTrainingUnavailable
				}
				for index := range jobs.Items {
					job := &jobs.Items[index]
					if controllerIs(job, "trainer.kubeflow.org/v1alpha1", "TrainJob", scope.TrainJobName, scope.TrainJobUID) {
						collect(string(job.GetUID()), job.GetName(), "Job")
					}
				}
			}
		}
		// The PodGroup only exists for plans that asked for one; its absence in
		// an older deployment is not an error and is never queried.
		if scope.PodGroupWanted {
			group, groupErr := a.client.Resource(podGroups).Namespace(scope.NamespaceName).Get(ctx, scope.TrainJobName, metav1.GetOptions{})
			if groupErr != nil && !apierrors.IsNotFound(groupErr) {
				return nil, biz.ErrTrainingUnavailable
			}
			if groupErr == nil && controllerIs(group, "trainer.kubeflow.org/v1alpha1", "TrainJob", scope.TrainJobName, scope.TrainJobUID) {
				collect(string(group.GetUID()), group.GetName(), "PodGroup")
			}
		}
	}
	if len(allowed) == 0 {
		return []biz.ExecutionEvent{}, nil
	}
	events, err := a.client.Resource(eventsResource).Namespace(scope.NamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil || events.GetContinue() != "" {
		return nil, biz.ErrTrainingUnavailable
	}
	result := make([]biz.ExecutionEvent, 0, len(events.Items))
	for index := range events.Items {
		event := &events.Items[index]
		if event.GetKind() != "Event" || event.GetNamespace() != scope.NamespaceName {
			return nil, biz.ErrWorkloadViewUnavailable
		}
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

func controllerOf(object *unstructured.Unstructured) (string, string, bool) {
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			return owner.Name, string(owner.UID), true
		}
	}
	return "", "", false
}

// jobIndex keeps multi-node identity explicit; the random Pod suffix is never
// the discriminator.
func jobIndex(pod *unstructured.Unstructured) int32 {
	if value, ok := pod.GetLabels()["batch.kubernetes.io/job-completion-index"]; ok {
		if parsed, err := strconv.ParseInt(value, 10, 32); err == nil && parsed >= 0 {
			return int32(parsed)
		}
	}
	return 0
}

func jobAttempt(pod *unstructured.Unstructured) int32 {
	if value, ok := pod.GetAnnotations()["jobset.sigs.k8s.io/job-index"]; ok {
		if parsed, err := strconv.ParseInt(value, 10, 32); err == nil && parsed >= 0 {
			return int32(parsed)
		}
	}
	return 0
}
