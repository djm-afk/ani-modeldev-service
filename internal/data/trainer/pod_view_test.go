package trainer_test

import (
	"context"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

const (
	workloadNamespace   = "tenant-training"
	workloadNamespaceID = "namespace-uid"
	workloadExecutionID = "608aa6bf-4dac-4989-bd22-9b0ef6f1ce68"
	workloadTrainJob    = "md-608aa6bf-4dac-4989-bd22-9b0ef6f1ce68"
	workloadTrainJobUID = "train-job-uid"
	workloadJobSetUID   = "jobset-uid"
	workloadJobUID      = "job-uid"
)

func workloadScope(podGroup bool) biz.ExecutionWorkloadScope {
	return biz.ExecutionWorkloadScope{
		TenantID: "9086ce3b-bd19-47c3-8467-190607f633db", ExecutionID: workloadExecutionID,
		NamespaceName: workloadNamespace, NamespaceUID: workloadNamespaceID,
		TrainJobName: workloadTrainJob, TrainJobUID: workloadTrainJobUID, PodGroupWanted: podGroup,
		Availability: biz.PodAvailabilityLive,
	}
}

// object builds an unstructured fixture with the exact apiVersion the adapter's
// GVR expects; the fake dynamic client derives its mapping from these.
func object(apiVersion, kind, name, uid string, owner map[string]any, labels map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{"name": name, "uid": uid}
	if kind != "Namespace" {
		metadata["namespace"] = workloadNamespace
	}
	if len(labels) != 0 {
		metadata["labels"] = labels
	}
	if owner != nil {
		metadata["ownerReferences"] = []any{owner}
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": metadata}}
}

func ownerRef(kind, apiVersion, name, uid string) map[string]any {
	return map[string]any{"apiVersion": apiVersion, "kind": kind, "name": name, "uid": uid, "controller": true}
}

func workerChain(podName, podUID, jobName, jobUID string) []runtime.Object {
	return []runtime.Object{
		object("v1", "Namespace", workloadNamespace, workloadNamespaceID, nil, nil),
		object("trainer.kubeflow.org/v1alpha1", "TrainJob", workloadTrainJob, workloadTrainJobUID, nil, nil),
		object("jobset.x-k8s.io/v1alpha2", "JobSet", workloadTrainJob, workloadJobSetUID, ownerRef("TrainJob", "trainer.kubeflow.org/v1alpha1", workloadTrainJob, workloadTrainJobUID), nil),
		object("batch/v1", "Job", jobName, jobUID, ownerRef("JobSet", "jobset.x-k8s.io/v1alpha2", workloadTrainJob, workloadJobSetUID),
			map[string]any{"jobset.sigs.k8s.io/jobset-uid": workloadJobSetUID}),
		object("v1", "Pod", podName, podUID, ownerRef("Job", "batch/v1", jobName, jobUID),
			map[string]any{"batch.kubernetes.io/job-name": jobName, "jobset.sigs.k8s.io/job-index": "2", "jobset.sigs.k8s.io/restart-attempt": "1"}),
	}
}

// Only a Pod whose complete owner chain reaches the verified TrainJob appears;
// a same-namespace Pod with a foreign or partial chain is silently skipped.
func TestListWorkerPodsKeepsOnlyTheVerifiedOwnerChain(t *testing.T) {
	objects := append(workerChain("md-pod-0", "pod-uid-0", "job-0", "job-0-uid"),
		object("v1", "Pod", "foreign-pod", "foreign-uid", ownerRef("Job", "batch/v1", "foreign-job", "foreign-job-uid"),
			map[string]any{"batch.kubernetes.io/job-name": "job-0"}),
		object("v1", "Pod", "no-owner-pod", "orphan-uid", nil,
			map[string]any{"batch.kubernetes.io/job-name": "job-0"}),
	)
	adapter := trainer.New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...))
	pods, err := adapter.ListWorkerPods(context.Background(), workloadScope(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 1 || pods[0].Name != "md-pod-0" || pods[0].UID != "pod-uid-0" {
		t.Fatalf("owner chain filter: %+v", pods)
	}
	if pods[0].Layer != biz.PodLayerTrainer || pods[0].Availability != biz.PodAvailabilityLive || !pods[0].LogAvailable || pods[0].LogID == "" {
		t.Fatalf("worker pod view: %+v", pods[0])
	}
	// JobSet publishes rank and restart attempt as labels; both must survive the
	// projection or multi-node/retry identity is lost.
	if pods[0].JobIndex != 2 || pods[0].Attempt != 1 {
		t.Fatalf("rank/attempt: index=%d attempt=%d", pods[0].JobIndex, pods[0].Attempt)
	}
}

// A missing namespace UID means the boundary itself is unverified, so no list
// is returned at all.
func TestListWorkerPodsRequiresVerifiedNamespaceIdentity(t *testing.T) {
	adapter := trainer.New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), workerChain("md-pod-0", "pod-uid-0", "job-0", "job-0-uid")...))
	scope := workloadScope(false)
	scope.NamespaceUID = "different-uid"
	if _, err := adapter.ListWorkerPods(context.Background(), scope); err == nil {
		t.Fatal("mismatched namespace identity accepted")
	}
}

// A PodGroup is queried only for a frozen plan that schedules one; an older
// deployment neither queries it nor fails.
func TestWorkloadEventsQueryPodGroupOnlyWhenTheFrozenPlanSchedulesOne(t *testing.T) {
	group := object("scheduling.volcano.sh/v1beta1", "PodGroup", workloadTrainJob, "podgroup-uid",
		ownerRef("TrainJob", "trainer.kubeflow.org/v1alpha1", workloadTrainJob, workloadTrainJobUID), nil)
	groupEvent := object("v1", "Event", "group-failed", "event-uid", nil, nil)
	groupEvent.Object["involvedObject"] = map[string]any{"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "PodGroup", "name": workloadTrainJob, "uid": "podgroup-uid"}
	groupEvent.Object["reason"] = "FailedScheduling"
	groupEvent.Object["message"] = "0/3 nodes are available"
	groupEvent.Object["type"] = "Warning"
	groupEvent.Object["source"] = map[string]any{"component": "volcano-scheduler"}
	groupEvent.Object["firstTimestamp"] = "2026-10-23T01:00:00Z"
	groupEvent.Object["lastTimestamp"] = "2026-10-23T01:01:00Z"
	groupEvent.Object["count"] = int64(3)

	objects := append(workerChain("md-pod-0", "pod-uid-0", "job-0", "job-0-uid"), group, groupEvent)
	pods := []biz.ExecutionPod{{Name: "md-pod-0", UID: "pod-uid-0"}}

	wanted := trainer.New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...))
	events, err := wanted.ListTrainingWorkloadEvents(context.Background(), workloadScope(true), pods)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].InvolvedKind != "PodGroup" || events[0].Source != "volcano-scheduler" {
		t.Fatalf("PodGroup event: %+v", events)
	}

	legacy := trainer.New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...))
	events, err = legacy.ListTrainingWorkloadEvents(context.Background(), workloadScope(false), pods)
	if err != nil {
		t.Fatalf("legacy plan without PodGroup must not fail: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("legacy plan reported a PodGroup event: %+v", events)
	}
}

// Events of objects outside the verified identity set never reach the page.
func TestWorkloadEventsIgnoreForeignInvolvedObjects(t *testing.T) {
	foreign := object("v1", "Event", "foreign", "event-uid", nil, nil)
	foreign.Object["involvedObject"] = map[string]any{"apiVersion": "v1", "kind": "Pod", "name": "other-pod", "uid": "other-pod-uid"}
	foreign.Object["reason"] = "FailedScheduling"
	foreign.Object["type"] = "Warning"
	foreign.Object["message"] = "foreign failure"
	foreign.Object["firstTimestamp"] = "2026-10-23T01:00:00Z"
	foreign.Object["lastTimestamp"] = "2026-10-23T01:00:00Z"

	objects := append(workerChain("md-pod-0", "pod-uid-0", "job-0", "job-0-uid"), foreign)
	adapter := trainer.New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...))
	events, err := adapter.ListTrainingWorkloadEvents(context.Background(), workloadScope(false), []biz.ExecutionPod{{Name: "md-pod-0", UID: "pod-uid-0"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.InvolvedUID == "other-pod-uid" {
			t.Fatalf("foreign event leaked: %+v", event)
		}
	}
}
