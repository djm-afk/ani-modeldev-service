package runtimeproof_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/cleanup"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/traininglogs"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
)

type computeCleanupFixture struct {
	step                       *stepRetentionFixture
	kube                       dynamic.Interface
	mu                         sync.Mutex
	objects                    map[string]*unstructured.Unstructured
	runtime                    *unstructured.Unstructured
	deleted                    []string
	loseDelete, replaceDelete  bool
	logReads                   int
	workflowDeleted            bool
	runtimeDeleted             bool
	stopPatches                int
	pvcOwnerUID                string
	deferDelete                bool
	pendingReads               map[string]int
	refreshTrainingBeforeClose bool
}

// These are Kubernetes/KFP boundary substitutes, never a training load. Every
// admission, training reservation, original exit and cleanup receipt commits
// through the real restricted PostgreSQL adapters before external mutations.
func newComputeCleanupFixture(t *testing.T, fine bool) *computeCleanupFixture {
	return newComputeCleanupClosingFixture(t, fine, "")
}

func newComputeCleanupClosingFixture(t *testing.T, fine bool, closeReason string, configureBoundary ...func(*computeCleanupFixture)) *computeCleanupFixture {
	t.Helper()
	f := &computeCleanupFixture{objects: map[string]*unstructured.Unstructured{}, pendingReads: map[string]int{}}
	deadline := []time.Duration{}
	if closeReason == "DEADLINE" {
		deadline = []time.Duration{2 * time.Second}
	}
	f.step = newConfiguredStepRetentionFixture(t, false, []bool{fine}, func(owner *ownerCloseFixture) {
		f.runtime = computeRuntime(t, owner)
	}, func(step *stepRetentionFixture, repository *lifecycle.Repository, kube dynamic.Interface) {
		f.step, f.kube = step, kube
		step.f.workflow.SetResourceVersion("1")
		step.serveExtra = f.serve
		for _, configure := range configureBoundary {
			configure(f)
		}
		ctx := context.Background()
		runtimePermit, err := repository.ReserveRuntime(ctx, step.f.authority())
		if err != nil || !runtimePermit.SendPermit {
			t.Fatal("original Runtime reservation", err)
		}
		runtimePlan, err := biz.FreezeTrainingPlan(step.f.execution, step.workspace, runtimePermit.State.TrainingWindow.AdmittedAt)
		if err != nil {
			t.Fatal(err)
		}
		binding, err := trainer.New(kube).CreateRuntime(ctx, runtimePlan)
		if err != nil {
			t.Fatal("original Runtime materialization", err)
		}
		if _, err := repository.RecordRuntimeBinding(ctx, step.f.authority(), binding); err != nil {
			t.Fatal(err)
		}
		reserved, err := repository.ReserveTraining(ctx, step.f.authority())
		if err != nil || !reserved.SendPermit {
			t.Fatal("real first training reservation", err)
		}
		core, err := coreclient.NewForConfig(step.clientConfig)
		if err != nil {
			t.Fatal(err)
		}
		training := trainer.New(kube).WithTrainingLogs(traininglogs.New(core))
		handle, err := training.CreateTraining(ctx, *reserved.State.Training)
		if err != nil {
			t.Fatal("real Trainer adapter at HTTPS boundary", err)
		}
		if _, err := repository.RecordTrainingHandle(ctx, step.f.authority(), handle); err != nil {
			t.Fatal(err)
		}
		if closeReason != "" {
			if closeReason == "USER_STOP" {
				a := step.f.execution
				if _, err := execution.New(step.open()).ApplyCloseIntent(ctx, biz.CloseIntent{TenantID: a.TenantID, ExecutionID: a.ExecutionID, OperationID: a.OperationID, SpecHash: a.SpecHash, SourceGeneration: 1, Reason: biz.CloseReasonUserStop, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), RequestedActor: "governance:cleanup-test"}); err != nil {
					t.Fatal("real USER_STOP creation fence", err)
				}
			} else {
				timer := time.NewTimer(time.Until(step.f.execution.Snapshot.DeadlineAt) + 10*time.Millisecond)
				<-timer.C
				if _, _, err := repository.RequestRuntimeClose(ctx, step.f.authority(), "DEADLINE"); err != nil {
					t.Fatal("original database deadline fence", err)
				}
			}
			if err := training.StopTraining(ctx, *reserved.State.Training, handle); err != nil {
				t.Fatal("real original-UID training suspend", err)
			}
		}
		first, err := training.ObserveTraining(ctx, *reserved.State.Training, handle, nil)
		if err != nil || first.WritersAbsent {
			t.Fatal("first exit must persist before finalizer release", err)
		}
		if _, err := repository.RecordTrainingObservation(ctx, step.f.authority(), first); err != nil {
			t.Fatal(err)
		}
		second, err := training.ObserveTraining(ctx, *reserved.State.Training, handle, first.Resources)
		if err != nil || !second.WritersAbsent {
			t.Fatal("durable original training exits did not release", err)
		}
		if _, err := repository.RecordTrainingObservation(ctx, step.f.authority(), second); err != nil {
			t.Fatal(err)
		}
		if f.refreshTrainingBeforeClose {
			fresh, err := training.ObserveTraining(ctx, *reserved.State.Training, handle, second.Resources)
			if err != nil || !fresh.WritersAbsent {
				t.Fatal("fresh original writer evidence before close", err)
			}
			if _, err := repository.RecordTrainingObservation(ctx, step.f.authority(), fresh); err != nil {
				t.Fatal(err)
			}
		}
		if method := reflect.ValueOf(repository).MethodByName("BeginComputeCleanup"); method.IsValid() {
			values := method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(step.f.authority()), reflect.ValueOf(uint64(0))})
			if values[1].IsNil() {
				t.Fatal("terminal training without CLOSED/fence authorized compute cleanup")
			}
		}
	}, deadline...)
	return f
}

func computeRuntime(t *testing.T, f *ownerCloseFixture) *unstructured.Unstructured {
	t.Helper()
	p := f.execution.Snapshot.RuntimePlan
	var spec map[string]any
	if err := json.Unmarshal(p.ExpectedSpec, &spec); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainingRuntime", "metadata": map[string]any{"name": p.Name, "namespace": p.Namespace, "uid": "runtime-original", "resourceVersion": "1", "annotations": map[string]any{"modeldev.ani.io/tenant-id": f.execution.TenantID, "modeldev.ani.io/execution-id": f.execution.ExecutionID, "modeldev.ani.io/create-operation-id": f.execution.OperationID, "modeldev.ani.io/execution-spec-sha256": f.execution.SpecHash, "modeldev.ani.io/runtime-spec-sha256": p.ExpectedSpecSHA256, "modeldev.ani.io/workspace-uid": "retention-workspace-uid"}}, "spec": spec}}
}

func (f *computeCleanupFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	ns := f.step.workspace.NamespaceName
	podPrefix := "/api/v1/namespaces/" + ns + "/pods/"
	trainPrefix := "/apis/trainer.kubeflow.org/v1alpha1/namespaces/" + ns + "/trainjobs"
	setPrefix := "/apis/jobset.x-k8s.io/v1alpha2/namespaces/" + ns + "/jobsets"
	jobPrefix := "/apis/batch/v1/namespaces/" + ns + "/jobs"
	groupPrefix := "/apis/scheduling.volcano.sh/v1beta1/namespaces/" + ns + "/podgroups"
	workflowPath := "/apis/argoproj.io/v1alpha1/namespaces/" + ns + "/workflows/" + f.step.f.workflow.GetName()
	runtimePath := "/apis/trainer.kubeflow.org/v1alpha1/clustertrainingruntimes/" + f.runtime.GetName()
	if f.runtime.GetKind() == "TrainingRuntime" {
		runtimePath = "/apis/trainer.kubeflow.org/v1alpha1/namespaces/" + ns + "/trainingruntimes/" + f.runtime.GetName()
	}
	pvcPath := "/api/v1/namespaces/" + ns + "/persistentvolumeclaims/" + f.step.workspace.PVCName
	trainingPod := strings.HasPrefix(r.URL.Path, podPrefix+"actual-training")
	matched := r.URL.Path == runtimePath || r.URL.Path == pvcPath || strings.HasPrefix(r.URL.Path, trainPrefix) || strings.HasPrefix(r.URL.Path, setPrefix) || strings.HasPrefix(r.URL.Path, jobPrefix) || strings.HasPrefix(r.URL.Path, groupPrefix) || trainingPod || r.URL.Path == workflowPath && r.Method == http.MethodDelete || r.URL.Path == workflowPath && f.workflowDeleted
	if !matched {
		return false
	}
	write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	status := func(code int, reason string) {
		w.WriteHeader(code)
		write(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReason(reason), Code: int32(code)})
	}
	if r.Method == http.MethodDelete || r.Method == http.MethodPatch {
		pool := f.step.open()
		state, err := lifecycle.New(pool).GetRuntime(r.Context(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
		pool.Close()
		if err != nil {
			t.Error("mutation could not read separately committed facts", err)
			status(500, "InternalError")
			return true
		}
		if r.Method == http.MethodDelete {
			raw, _ := json.Marshal(state)
			var document map[string]json.RawMessage
			_ = json.Unmarshal(raw, &document)
			var receipt struct {
				LogsReady bool                  `json:"logs_ready"`
				Requested []biz.RuntimeResource `json:"requested"`
			}
			if json.Unmarshal(document["ComputeCleanup"], &receipt) != nil || !receipt.LogsReady || len(receipt.Requested) == 0 || state.ClosedAt == nil {
				t.Error("compute DELETE preceded committed original exits/logs/intent")
				status(500, "InternalError")
				return true
			}
		} else if strings.HasPrefix(r.URL.Path, trainPrefix+"/") {
			if state.CloseGeneration == 0 || state.ClosedAt != nil {
				t.Error("training suspend preceded original durable creation fence")
				status(500, "InternalError")
				return true
			}
		} else if state.Observation == nil || len(state.Observation.Resources) == 0 {
			t.Error("training finalizer release preceded original DB exit observation")
			status(500, "InternalError")
			return true
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case runtimePath:
			if f.runtimeDeleted {
				status(404, "NotFound")
				return true
			}
			write(f.runtime)
			return true
		case pvcPath:
			pvc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": f.step.workspace.PVCName, "namespace": ns, "uid": f.step.workspace.PVCUID}, "spec": map[string]any{"storageClassName": f.step.f.execution.Snapshot.Workspace.StorageClass}, "status": map[string]any{"phase": "Bound"}}}
			if f.pvcOwnerUID != "" {
				pvc.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", Name: "original", UID: types.UID(f.pvcOwnerUID)}})
			}
			write(pvc)
			return true
		case jobPrefix, setPrefix, trainPrefix, groupPrefix:
			items := []any{}
			for path, object := range f.objects {
				if strings.HasPrefix(path, r.URL.Path+"/") {
					items = append(items, object.Object)
				}
			}
			write(map[string]any{"apiVersion": "v1", "kind": "List", "metadata": map[string]any{}, "items": items})
			return true
		}
		if strings.HasSuffix(r.URL.Path, "/log") && trainingPod {
			f.logReads++
			_, _ = w.Write([]byte(time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano) + " actual retained training output\n"))
			return true
		}
	}
	if r.Method == http.MethodPost && r.URL.Path == trainPrefix {
		var object unstructured.Unstructured
		if json.NewDecoder(r.Body).Decode(&object) != nil {
			status(400, "BadRequest")
			return true
		}
		object.SetUID("train-original")
		object.SetResourceVersion("1")
		object.SetGeneration(1)
		_ = unstructured.SetNestedSlice(object.Object, []any{map[string]any{"type": "Complete", "status": "True"}}, "status", "conditions")
		f.objects[trainPrefix+"/"+object.GetName()] = &object
		makeController := func(api, kind, name, uid, ownerAPI, ownerKind, ownerName, ownerUID string, condition string) *unstructured.Unstructured {
			return &unstructured.Unstructured{Object: map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns, "uid": uid, "resourceVersion": "1", "generation": int64(1), "ownerReferences": []any{map[string]any{"apiVersion": ownerAPI, "kind": ownerKind, "name": ownerName, "uid": ownerUID, "controller": true}}}, "spec": map[string]any{"suspend": false}, "status": map[string]any{"conditions": []any{map[string]any{"type": condition, "status": "True"}}}}}
		}
		set := makeController("jobset.x-k8s.io/v1alpha2", "JobSet", object.GetName(), "set-original", object.GetAPIVersion(), object.GetKind(), object.GetName(), string(object.GetUID()), "Completed")
		job := makeController("batch/v1", "Job", object.GetName()+"-node-0", "job-original", set.GetAPIVersion(), set.GetKind(), set.GetName(), string(set.GetUID()), "Complete")
		f.objects[setPrefix+"/"+set.GetName()] = set
		f.objects[jobPrefix+"/"+job.GetName()] = job
		group := makeController("scheduling.volcano.sh/v1beta1", "PodGroup", object.GetName(), "group-original", object.GetAPIVersion(), object.GetKind(), object.GetName(), string(object.GetUID()), "Scheduled")
		vector, vectorErr := biz.TrainingResourceVector(f.step.f.execution.Snapshot)
		if vectorErr != nil {
			t.Error(vectorErr)
			status(500, "InternalError")
			return true
		}
		minimum := map[string]any{}
		for key, value := range vector {
			minimum[key] = value
		}
		group.Object["spec"] = map[string]any{"minMember": int64(1), "minResources": minimum, "queue": "default"}
		f.objects[groupPrefix+"/"+group.GetName()] = group
		pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "actual-training", "namespace": ns, "uid": "training-pod-original", "resourceVersion": "1", "finalizers": []any{"modeldev.ani.io/training-exit-evidence"}, "ownerReferences": []any{map[string]any{"apiVersion": job.GetAPIVersion(), "kind": "Job", "name": job.GetName(), "uid": string(job.GetUID()), "controller": true}}}, "spec": map[string]any{"serviceAccountName": f.step.f.execution.Snapshot.Environment.Identities.TrainerServiceAccount, "containers": []any{map[string]any{"name": "node"}}, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": f.step.workspace.PVCName}}}}, "status": map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "node", "restartCount": int64(0), "state": map[string]any{"terminated": map[string]any{"exitCode": int64(0), "finishedAt": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)}}}}}}}
		var runtimeSpec map[string]any
		if json.Unmarshal(f.step.f.execution.Snapshot.RuntimePlan.ExpectedSpec, &runtimeSpec) != nil {
			t.Fatal("invalid fixture runtime")
		}
		runtimeJobs, _, _ := unstructured.NestedSlice(runtimeSpec, "template", "spec", "replicatedJobs")
		podSpec, _, _ := unstructured.NestedMap(runtimeJobs[0].(map[string]any), "template", "spec", "template", "spec")
		pod.Object["spec"] = podSpec
		f.objects[podPrefix+pod.GetName()] = pod
		f.step.pods[pod.GetName()] = pod
		write(&object)
		return true
	}
	var object *unstructured.Unstructured
	if r.URL.Path == runtimePath && !f.runtimeDeleted {
		object = f.runtime
	} else if r.URL.Path == workflowPath && !f.workflowDeleted {
		object = f.step.f.workflow
	} else {
		object = f.objects[r.URL.Path]
	}
	if object == nil {
		status(404, "NotFound")
		return true
	}
	if r.Method == http.MethodGet && f.pendingReads[r.URL.Path] > 0 {
		f.pendingReads[r.URL.Path]--
		if f.pendingReads[r.URL.Path] == 0 {
			delete(f.objects, r.URL.Path)
			if r.URL.Path == runtimePath {
				f.runtimeDeleted = true
			}
			status(404, "NotFound")
			return true
		}
	}
	switch r.Method {
	case http.MethodGet:
		write(object)
	case http.MethodPatch:
		var patch []map[string]any
		if json.NewDecoder(r.Body).Decode(&patch) != nil || (len(patch) != 5 && len(patch) != 3) {
			status(400, "BadRequest")
			return true
		}
		if patch[0]["value"] != string(object.GetUID()) || patch[1]["value"] != object.GetResourceVersion() {
			status(409, "Conflict")
			return true
		}
		if len(patch) == 3 {
			if object.GetKind() != "TrainJob" || patch[2]["path"] != "/spec/suspend" || patch[2]["value"] != true {
				status(400, "BadRequest")
				return true
			}
			f.stopPatches++
			for _, current := range f.objects {
				if current.GetKind() == "Pod" {
					_ = unstructured.SetNestedField(current.Object, "Failed", "status", "phase")
					_ = unstructured.SetNestedSlice(current.Object, []any{map[string]any{"name": "node", "restartCount": int64(0), "state": map[string]any{"terminated": map[string]any{"exitCode": int64(143), "finishedAt": time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)}}}}, "status", "containerStatuses")
				} else {
					current.SetGeneration(2)
					_ = unstructured.SetNestedField(current.Object, true, "spec", "suspend")
					_ = unstructured.SetNestedSlice(current.Object, []any{map[string]any{"type": "Suspended", "status": "True", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339Nano)}}, "status", "conditions")
				}
				current.SetResourceVersion("suspended-2")
			}
			write(object)
			return true
		}
		object.SetFinalizers(nil)
		object.SetResourceVersion("2")
		write(object)
	case http.MethodDelete:
		var options metav1.DeleteOptions
		if f.replaceDelete {
			object.SetUID(types.UID("same-name-replacement"))
			f.replaceDelete = false
		}
		wantedPolicy := metav1.DeletePropagationOrphan
		if suspended, _, _ := unstructured.NestedBool(object.Object, "spec", "suspend"); object.GetKind() == "TrainJob" && suspended {
			wantedPolicy = metav1.DeletePropagationBackground
		}
		if json.NewDecoder(r.Body).Decode(&options) != nil || options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil || string(*options.Preconditions.UID) != string(object.GetUID()) || *options.Preconditions.ResourceVersion != object.GetResourceVersion() || options.PropagationPolicy == nil || *options.PropagationPolicy != wantedPolicy || len(object.GetFinalizers()) != 0 {
			status(409, "Conflict")
			return true
		}
		f.deleted = append(f.deleted, object.GetKind())
		if f.deferDelete && object.GetKind() == "TrainJob" {
			at := metav1.Now()
			object.SetDeletionTimestamp(&at)
			object.SetResourceVersion("deleting-3")
			f.pendingReads[r.URL.Path] = 3
		} else {
			delete(f.objects, r.URL.Path)
		}
		if object.GetKind() == "Pod" {
			delete(f.step.pods, object.GetName())
		}
		if object.GetKind() == "Workflow" {
			f.workflowDeleted = true
		}
		if object.GetKind() == "TrainingRuntime" {
			f.runtimeDeleted = true
		}
		// Emulate Orphan GC only, preserving original dependent identities.
		for _, child := range f.objects {
			if wantedPolicy != metav1.DeletePropagationOrphan {
				continue
			}
			refs := child.GetOwnerReferences()
			if len(refs) == 1 && refs[0].UID == object.GetUID() {
				child.SetOwnerReferences(nil)
				child.SetResourceVersion("3")
			}
		}
		if f.loseDelete {
			f.loseDelete = false
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			} else {
				t.Error(err)
			}
			return true
		}
		write(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Success", Code: 200})
	default:
		t.Errorf("unexpected compute boundary %s %s", r.Method, r.URL.Path)
		status(400, "BadRequest")
	}
	return true
}

func (f *computeCleanupFixture) worker(t *testing.T) *biz.CloseWorker {
	t.Helper()
	pool := f.step.open()
	dispatch := submission.New(pool)
	facts := lifecycle.New(pool)
	identity, err := stepidentity.New(f.kube, "managed-test-audience")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := biz.NewManagedSteps(dispatch, execution.New(pool), identity, f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := biz.NewManagedRuntime(steps, facts, trainer.New(f.kube), f.step.proof, f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	closer, err := biz.NewExecutionCloser(runtime, f.step.f.runs, f.step.proof, f.step.proof)
	if err != nil {
		t.Fatal(err)
	}
	// The regression runs unchanged against the frozen pre-feature source and
	// produces a behavioral RED when the automatic capability is absent.
	core, err := coreclient.NewForConfig(f.step.clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	if method := reflect.ValueOf(f.step.proof).MethodByName("WithTrainingLogs"); method.IsValid() {
		method.Call([]reflect.Value{reflect.ValueOf(traininglogs.New(core))})
	}
	if method := reflect.ValueOf(closer).MethodByName("WithComputeCleanup"); method.IsValid() {
		values := method.Call([]reflect.Value{reflect.ValueOf(f.step.proof)})
		if !values[1].IsNil() {
			t.Fatal(values[1].Interface())
		}
		closer = values[0].Interface().(*biz.ExecutionCloser)
	}
	worker, err := biz.NewCloseWorker(facts, closer, biz.PipelineDispatchBinding{TenantID: f.step.f.execution.TenantID, Environment: f.step.f.execution.Snapshot.Environment, Owner: f.step.f.dispatch.Plan.Owner}, 10, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func TestAutomaticComputeCleanupOriginalGraphAndRestart(t *testing.T) {
	for _, fine := range []bool{false, true} {
		t.Run(map[bool]string{false: "CPU", true: "FINETUNING"}[fine], func(t *testing.T) {
			f := newComputeCleanupFixture(t, fine)
			result, err := f.worker(t).ReconcileOnce(context.Background())
			if err != nil || result.Unresolved != 0 {
				t.Fatalf("automatic cleanup unresolved: %+v %v", result, err)
			}
			f.mu.Lock()
			deleted := append([]string{}, f.deleted...)
			remaining := len(f.objects)
			logs := f.logReads
			f.mu.Unlock()
			if remaining != 0 || !f.runtimeDeleted || !reflect.DeepEqual(deleted, []string{"TrainJob", "JobSet", "Job", "Pod", "PodGroup", "Workflow", "TrainingRuntime"}) || logs != 1 {
				t.Fatalf("AUTO_COMPUTE_NOT_IMPLEMENTED: original terminal graph remains=%d deletes=%v logs=%d", remaining, deleted, logs)
			}
			state, err := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(state)
			var document map[string]json.RawMessage
			_ = json.Unmarshal(raw, &document)
			var receipt struct {
				Phase           string                `json:"phase"`
				ConfirmedAbsent []biz.RuntimeResource `json:"confirmed_absent"`
				LogsReady       bool                  `json:"logs_ready"`
			}
			if json.Unmarshal(document["ComputeCleanup"], &receipt) != nil || receipt.Phase != "APPLIED" || len(receipt.ConfirmedAbsent) != 7 || !receipt.LogsReady {
				t.Fatal("real reconnect lost automatic cleanup receipt")
			}
			if state.Workspace == nil || state.CloseEvidence == nil || state.Observation == nil || len(state.Observation.Resources) != 5 || !state.Observation.Resources[0].APIObjectPresent {
				t.Fatal("cleanup erased original writer history or protected workspace")
			}
			query := service.NewQuery(execution.New(f.step.open()), nil)
			queryContext := service.WithVerifiedGovernanceQuery(context.Background(), service.GovernanceDelivery{TenantID: f.step.f.execution.TenantID, RequestID: "post-cleanup-log-read"}, modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName)
			logResult, err := query.GetExecutionLogs(queryContext, &modeldevv1.GetExecutionLogsRequest{ExecutionId: f.step.f.execution.ExecutionID})
			if err != nil || len(logResult.GetLines()) != 1 || logResult.Source.ResourceUid != "training-pod-original" || logResult.Lines[0].Text != "actual retained training output" {
				t.Fatal("original durable logs unavailable after all API objects were removed", err)
			}
			record, err := execution.New(f.step.open()).GetOperationRecord(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := biz.CleanupPlanFor(record)
			if err != nil || len(plan.Targets) != 0 {
				t.Fatal("explicit cleanup lost the original durable automatic receipt", err)
			}
			boundary := cleanup.New(f.kube, f.step.proof)
			if err := boundary.VerifyCleanupWritersAbsent(context.Background(), record); err != nil {
				t.Fatal("removed Workflow still required for original writer proof", err)
			}
			if _, err := boundary.VerifyCleanupResolved(context.Background(), record); err != nil {
				t.Fatal("durably removed compute broke later explicit cleanup verification", err)
			}
			again, err := f.worker(t).ReconcileOnce(context.Background())
			if err != nil || again.Examined != 0 {
				t.Fatal("restart reissued completed cleanup", again, err)
			}
		})
	}
}

func TestAutomaticComputeCleanupLostDeleteAndUIDConflicts(t *testing.T) {
	f := newComputeCleanupFixture(t, false)
	f.loseDelete = true
	first, err := f.worker(t).ReconcileOnce(context.Background())
	if err != nil || first.Unresolved != 1 {
		t.Fatal("lost DELETE response became success", first, err)
	}
	second, err := f.worker(t).ReconcileOnce(context.Background())
	if err != nil || second.Unresolved != 0 {
		t.Fatal("restart did not resume original durable intent", second, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.objects) != 0 || !f.runtimeDeleted || !reflect.DeepEqual(f.deleted, []string{"TrainJob", "JobSet", "Job", "Pod", "PodGroup", "Workflow", "TrainingRuntime"}) {
		t.Fatal("restart reissued or lost an original target", f.deleted)
	}
}

func TestAutomaticComputeCleanupAfterOriginalStopAndDeadline(t *testing.T) {
	for _, reason := range []string{"USER_STOP", "DEADLINE"} {
		t.Run(reason, func(t *testing.T) {
			f := newComputeCleanupClosingFixture(t, reason == "DEADLINE", reason)
			f.deferDelete = true
			f.loseDelete = true
			result, err := f.worker(t).ReconcileOnce(context.Background())
			if err != nil || result.Unresolved != 1 {
				t.Fatal("lost stopped DELETE response became success", result, err)
			}
			result, err = f.worker(t).ReconcileOnce(context.Background())
			if err != nil || result.Unresolved != 0 {
				t.Fatal("stopped original compute cleanup did not finish", result, err)
			}
			state, err := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID)
			if err != nil || state.CloseReason != reason || state.ComputeCleanup == nil || state.ComputeCleanup.Phase != "APPLIED" || state.Workspace == nil || f.stopPatches != 1 || len(f.objects) != 0 || !f.workflowDeleted {
				t.Fatal("original Stop/deadline fence or durable compute receipt lost", err)
			}
			for _, resource := range state.Observation.Resources {
				if resource.Kind == "Pod" && (resource.ExitCode == nil || *resource.ExitCode != 143 || !resource.APIObjectPresent) {
					t.Fatal("automatic disposal erased the original partial training exit", resource)
				}
			}
			if again, err := f.worker(t).ReconcileOnce(context.Background()); err != nil || again.Examined != 0 {
				t.Fatal("restart renewed original Stop/deadline cleanup", again, err)
			}
		})
	}
}

func TestAutomaticComputeCleanupRetainsUnobservedSuspendAndStoppedUnknownWriter(t *testing.T) {
	for _, attack := range []string{"unobserved-suspend", "unreported-suspend", "stale-suspend-report", "changed-suspend-generation", "protected-pvc-owner", "stopped-unknown-writer", "stopped-replacement"} {
		t.Run(attack, func(t *testing.T) {
			f := newComputeCleanupClosingFixture(t, false, "USER_STOP")
			switch attack {
			case "unobserved-suspend":
				for _, object := range f.objects {
					if object.GetKind() == "JobSet" {
						_ = unstructured.SetNestedField(object.Object, false, "spec", "suspend")
					}
				}
			case "unreported-suspend", "stale-suspend-report", "changed-suspend-generation":
				for _, object := range f.objects {
					if object.GetKind() != "JobSet" {
						continue
					}
					switch attack {
					case "unreported-suspend":
						_ = unstructured.SetNestedSlice(object.Object, []any{}, "status", "conditions")
					case "stale-suspend-report":
						_ = unstructured.SetNestedSlice(object.Object, []any{map[string]any{"type": "Suspended", "status": "True", "lastTransitionTime": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)}}, "status", "conditions")
					case "changed-suspend-generation":
						object.SetGeneration(3)
					}
				}
			case "protected-pvc-owner":
				// Even a non-controller ownerRef would let Background GC remove
				// the work volume. Keep the volume and block compute deletion.
				f.pvcOwnerUID = "train-original"
			case "stopped-replacement":
				f.replaceDelete = true
			case "stopped-unknown-writer":
				pod := f.objects["/api/v1/namespaces/"+f.step.workspace.NamespaceName+"/pods/actual-training"].DeepCopy()
				pod.SetName("unknown-stopped-writer")
				pod.SetUID("unknown-stopped-uid")
				pod.SetOwnerReferences(nil)
				f.step.pods[pod.GetName()] = pod
			}
			result, err := f.worker(t).ReconcileOnce(context.Background())
			if err != nil || result.Unresolved != 1 || len(f.deleted) != 0 {
				t.Fatal("unobserved/replaced/additional stopped writer allowed deletion", result, err, f.deleted)
			}
		})
	}
}

func TestAutomaticComputeCleanupKeepsReplacementAndUnknownWriter(t *testing.T) {
	for _, attack := range []string{"replace-at-delete", "foreign-finalizer", "unknown-writer"} {
		t.Run(attack, func(t *testing.T) {
			f := newComputeCleanupFixture(t, false)
			f.mu.Lock()
			switch attack {
			case "replace-at-delete":
				f.replaceDelete = true
			case "foreign-finalizer":
				f.objects["/api/v1/namespaces/"+f.step.workspace.NamespaceName+"/pods/actual-training"].SetFinalizers([]string{"foreign.example/retain"})
			case "unknown-writer":
				f.step.pods["unrelated-writer"] = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "unrelated-writer", "namespace": f.step.workspace.NamespaceName, "uid": "unknown-writer-uid"}, "spec": map[string]any{"containers": []any{map[string]any{"name": "main"}}, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": f.step.workspace.PVCName}}}}, "status": map[string]any{"phase": "Running"}}}
			}
			f.mu.Unlock()
			result, err := f.worker(t).ReconcileOnce(context.Background())
			if err != nil || result.Unresolved != 1 {
				t.Fatal("unsafe cleanup passed", attack, result, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.deleted) != 0 {
				t.Fatal("unsafe cleanup deleted compute", attack, f.deleted)
			}
		})
	}
}

func TestAutomaticComputeCleanupRejectsCorruptDurableTargetBeforeFurtherDelete(t *testing.T) {
	f := newComputeCleanupFixture(t, false)
	f.loseDelete = true
	first, err := f.worker(t).ReconcileOnce(context.Background())
	if err != nil || first.Unresolved != 1 {
		t.Fatal("lost original attempt preflight", first, err)
	}
	pool := f.step.open()
	changed, err := pool.Exec(context.Background(), "UPDATE modeldev_execution_runtimes SET facts=jsonb_set(facts,'{ComputeCleanup,targets,0,UID}',to_jsonb($3::text),false) WHERE tenant_id=$1::uuid AND execution_id=$2::uuid", f.step.f.execution.TenantID, f.step.f.execution.ExecutionID, "forged-original-target")
	if err != nil || changed.RowsAffected() != 1 {
		t.Fatal("negative fixture could not corrupt its isolated receipt", err)
	}
	if _, err := lifecycle.New(f.step.open()).GetRuntime(context.Background(), f.step.f.execution.TenantID, f.step.f.execution.ExecutionID); !errors.Is(err, biz.ErrPersistence) {
		t.Fatal("forged persisted cleanup identity accepted", err)
	}
	result, err := f.worker(t).ReconcileOnce(context.Background())
	if err != nil || result.Unresolved != 1 {
		t.Fatal("corrupt original target did not block owner", result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reflect.DeepEqual(f.deleted, []string{"TrainJob"}) {
		t.Fatal("corrupted receipt authorized another mutation", f.deleted)
	}
}

func TestAutomaticComputeCleanupBoundsBlockedAttemptsAndRetainsForeignFinalizer(t *testing.T) {
	f := newComputeCleanupFixture(t, false)
	podPath := "/api/v1/namespaces/" + f.step.workspace.NamespaceName + "/pods/actual-training"
	f.objects[podPath].SetFinalizers([]string{"foreign.example/retain"})
	for i := 0; i < 3; i++ {
		result, err := f.worker(t).ReconcileOnce(context.Background())
		if err != nil || result.Unresolved != 1 {
			t.Fatal("blocked original attempt lost its review result", i, result, err)
		}
	}
	result, err := f.worker(t).ReconcileOnce(context.Background())
	if err != nil || result.Examined != 0 || len(f.deleted) != 0 || !reflect.DeepEqual(f.objects[podPath].GetFinalizers(), []string{"foreign.example/retain"}) {
		t.Fatal("blocked cleanup retried indefinitely or removed a foreign marker", result, err)
	}
}
