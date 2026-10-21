package trainer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/admissionfixture"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"time"
)

func TestTrainingCompleteRejectionIsDistinctFromUnknownCreate(t *testing.T) {
	// Public Status captured from the task-only real server dry-run. It is an
	// external HTTP response fixture, never a manufactured typed domain error.
	invalid, err := os.ReadFile("testdata/training-create-invalid-422.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                string
		code                int
		body                string
		truncated, rejected bool
	}{
		{"complete forbidden", 403, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`, false, true},
		{"complete bad request", 400, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"BadRequest","code":400}`, false, true},
		{"actual invalid admission response", 422, string(invalid), false, true},
		{"truncated invalid", 422, string(invalid), true, false},
		{"invalid wrong reason", 422, strings.Replace(string(invalid), `"reason": "Invalid"`, `"reason": "Forbidden"`, 1), false, false},
		{"invalid missing kind", 422, strings.Replace(string(invalid), `"kind": "Status",`, ``, 1), false, false},
		{"invalid wrong version", 422, strings.Replace(string(invalid), `"apiVersion": "v1"`, `"apiVersion": "v2"`, 1), false, false},
		{"unrecognized forbidden body", 403, `proxy failed`, false, false},
		{"truncated forbidden", 403, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`, true, false},
		{"mismatched status reason", 400, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":400}`, false, false},
		{"conflict", 409, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"AlreadyExists","code":409}`, false, false},
		{"server unavailable", 503, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"ServiceUnavailable","code":503}`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newTrainingFixture(t)
			f.createStatusCode, f.createStatusBody, f.createStatusTruncated = test.code, test.body, test.truncated
			_, err := f.adapter(t).CreateTraining(context.Background(), f.plan)
			if test.rejected {
				var rejected *biz.TrainingCreationRejection
				if !errors.As(err, &rejected) || !errors.Is(err, biz.ErrTrainingRejected) || rejected.RequestSHA256 != f.plan.RequestSHA256 || rejected.NamespaceUID != f.plan.Workspace.NamespaceUID || rejected.StatusCode != int32(test.code) || !rejected.ObservedAt.IsZero() {
					t.Fatalf("complete trusted %d lost the original request-bound refusal: %v", test.code, err)
				}
			} else if !errors.Is(err, biz.ErrTrainingUncertain) {
				t.Fatalf("untrusted or unknown create lost uncertainty: %v", err)
			}
			if f.posts != 1 || f.train != nil {
				t.Fatal("boundary rejection recreated or fabricated a TrainJob")
			}
		})
	}
}

func TestTrainingCreateFreezesCPUWorkspaceAndFindsUncertainResultWithoutRecreate(t *testing.T) {
	f := newTrainingFixture(t)
	f.loseCreateResponse = true
	a := f.adapter(t)
	ctx := context.Background()
	if _, err := a.CreateTraining(ctx, f.plan); !errors.Is(err, biz.ErrTrainingUncertain) {
		t.Fatalf("lost Create response must stay uncertain: %v", err)
	}
	handle, err := a.FindTraining(ctx, f.plan)
	if err != nil {
		t.Fatalf("recover the same deterministic TrainJob: %v", err)
	}
	if handle.TrainJobUID != trainingUID || handle.PVCUID != workspaceUID || handle.NamespaceUID != trainingNamespaceUID {
		t.Fatalf("incorrect exact resource handles: %+v", handle)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Fatalf("uncertain result must never resend Create, posts=%d", f.posts)
	}
	job := f.train
	for _, field := range []struct {
		path []string
		want string
	}{
		{[]string{"metadata", "name"}, f.plan.Name},
		{[]string{"spec", "runtimeRef", "name"}, f.plan.Snapshot.RuntimePlan.Name},
		{[]string{"spec", "trainer", "image"}, f.plan.Snapshot.Program.ImageDigest},
	} {
		got, _, _ := unstructured.NestedString(job, field.path...)
		if got != field.want {
			t.Errorf("%v=%q, want %q", field.path, got, field.want)
		}
	}
	trainerSpec, _, _ := unstructured.NestedMap(job, "spec", "trainer")
	if trainerSpec["numNodes"] != float64(1) || trainerSpec["numProcPerNode"] != float64(1) {
		t.Fatalf("not single-process CPU: %v", trainerSpec)
	}
	resources, _, _ := unstructured.NestedMap(job, "spec", "trainer", "resourcesPerNode")
	encoded, _ := json.Marshal(resources)
	if strings.Contains(string(encoded), "gpu") || !strings.Contains(string(encoded), "cpu") || !strings.Contains(string(encoded), "memory") {
		t.Fatalf("missing bounded CPU resources: %s", encoded)
	}
	if overrides, _, _ := unstructured.NestedSlice(job, "spec", "podTemplateOverrides"); len(overrides) != 0 {
		t.Fatal("complete Runtime duplicated with mutable overrides")
	}
	override := runtimePodTemplate(f)
	sa, _, _ := unstructured.NestedString(override, "spec", "serviceAccountName")
	if sa != "cpu-training" {
		t.Fatalf("training used wrong service account %q", sa)
	}
	containers, _, _ := unstructured.NestedSlice(override, "spec", "containers")
	if len(containers) != 1 || containers[0].(map[string]any)["name"] != "node" {
		t.Fatalf("unexpected training containers: %v", containers)
	}
	mounts, _, _ := unstructured.NestedSlice(containers[0].(map[string]any), "volumeMounts")
	if len(mounts) != 3 || mounts[0].(map[string]any)["name"] == mounts[1].(map[string]any)["name"] {
		t.Fatalf("Trainer strict CRD requires distinct names for input/output mounts: %v", mounts)
	}
	inputReadOnly, outputPrivate := false, false
	for _, item := range mounts {
		mount := item.(map[string]any)
		if mount["mountPath"] == "/inputs" && mount["subPath"] == "inputs" && mount["readOnly"] == true {
			inputReadOnly = true
		}
		if mount["mountPath"] == "/outputs" && mount["subPath"] == "training" && mount["readOnly"] != true {
			outputPrivate = true
		}
	}
	if !inputReadOnly || !outputPrivate || len(mounts) != 3 {
		t.Fatalf("workspace isolation lost: %v", mounts)
	}
}

func TestTrainingUsesFrozenNodeJobTargetThroughCreateFindStopAndObserve(t *testing.T) {
	f := newTrainingFixture(t)
	jobs, _, _ := unstructured.NestedSlice(f.runtime, "spec", "template", "spec", "replicatedJobs")
	jobs[0].(map[string]any)["name"] = "node"
	if err := unstructured.SetNestedSlice(f.runtime, jobs, "spec", "template", "spec", "replicatedJobs"); err != nil {
		t.Fatal(err)
	}
	spec, _, _ := unstructured.NestedMap(f.runtime, "spec")
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	f.plan.Snapshot.RuntimePlan.ExpectedSpecSHA256 = hex.EncodeToString(digest[:])
	f.plan.Snapshot.RuntimePlan.TargetJobs = []string{"node"}
	jobName := f.plan.Name + "-node-0"
	if err := unstructured.SetNestedField(f.job, jobName, "metadata", "name"); err != nil {
		t.Fatal(err)
	}
	owners, _, _ := unstructured.NestedSlice(f.pod, "metadata", "ownerReferences")
	owners[0].(map[string]any)["name"] = jobName
	if err := unstructured.SetNestedSlice(f.pod, owners, "metadata", "ownerReferences"); err != nil {
		t.Fatal(err)
	}
	a := f.adapter(t)
	ctx := context.Background()
	handle, err := a.CreateTraining(ctx, f.plan)
	if err != nil {
		t.Fatalf("create with the frozen node training Job: %v", err)
	}
	if overrides, _, _ := unstructured.NestedSlice(f.train, "spec", "podTemplateOverrides"); len(overrides) != 0 {
		t.Fatal("unexpected mutable override")
	}
	found, err := a.FindTraining(ctx, f.plan)
	if err != nil || found != handle {
		t.Fatalf("find the same node-targeted TrainJob: %+v / %v", found, err)
	}
	if err := a.StopTraining(ctx, f.plan, handle); err != nil {
		t.Fatalf("suspend the same node-targeted TrainJob: %v", err)
	}
	f.setTerminal(t, 0)
	observation, err := a.ObserveTraining(ctx, f.plan, handle, nil)
	if err != nil || observation.Outcome != "SUCCEEDED" || !observation.WritersAbsent {
		t.Fatalf("observe the real node Job Pod exit: %+v / %v", observation, err)
	}
	for _, resource := range observation.Resources {
		if resource.Kind == "Job" && resource.Name == jobName && resource.UID == childJobUID {
			return
		}
	}
	t.Fatal("observation lost the exact node Job identity")
}

func TestTrainingRejectsUnboundOrUnsafeRuntimeTargetsBeforeCreate(t *testing.T) {
	for _, mode := range []string{"target mismatch", "missing target", "multiple targets", "invalid target", "wrong training role", "digest mismatch", "multiple Jobs"} {
		t.Run(mode, func(t *testing.T) {
			f := newTrainingFixture(t)
			switch mode {
			case "target mismatch":
				f.plan.Snapshot.RuntimePlan.TargetJobs = []string{"trainer"}
			case "missing target":
				f.plan.Snapshot.RuntimePlan.TargetJobs = nil
			case "multiple targets":
				f.plan.Snapshot.RuntimePlan.TargetJobs = []string{"trainer", "node"}
			case "invalid target":
				f.plan.Snapshot.RuntimePlan.TargetJobs = []string{"../trainer"}
			case "wrong training role", "multiple Jobs":
				jobs, _, _ := unstructured.NestedSlice(f.runtime, "spec", "template", "spec", "replicatedJobs")
				if mode == "wrong training role" {
					if err := unstructured.SetNestedField(jobs[0].(map[string]any), "dataset-initializer", "template", "metadata", "labels", "trainer.kubeflow.org/trainjob-ancestor-step"); err != nil {
						t.Fatal(err)
					}
				} else {
					jobs = append(jobs, jobs[0])
				}
				if err := unstructured.SetNestedSlice(f.runtime, jobs, "spec", "template", "spec", "replicatedJobs"); err != nil {
					t.Fatal(err)
				}
				spec, _, _ := unstructured.NestedMap(f.runtime, "spec")
				encoded, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(encoded)
				f.plan.Snapshot.RuntimePlan.ExpectedSpecSHA256 = hex.EncodeToString(digest[:])
			case "digest mismatch":
				f.plan.Snapshot.RuntimePlan.ExpectedSpecSHA256 = strings.Repeat("0", 64)
			}
			if _, err := f.adapter(t).CreateTraining(context.Background(), f.plan); !errors.Is(err, biz.ErrRuntimeConflict) {
				t.Fatalf("unbound or unsafe target must fail closed: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.posts != 0 || f.train != nil {
				t.Fatalf("invalid runtime created a TrainJob: posts=%d", f.posts)
			}
		})
	}
}

func TestTrainingFindRejectsSpecSubstitutionAndRecreatedWorkspace(t *testing.T) {
	for _, mode := range []string{"image", "workspace"} {
		t.Run(mode, func(t *testing.T) {
			f := newTrainingFixture(t)
			a := f.adapter(t)
			if _, err := a.CreateTraining(context.Background(), f.plan); err != nil {
				t.Fatalf("create: %v", err)
			}
			f.mu.Lock()
			if mode == "image" {
				_ = unstructured.SetNestedField(f.train, "registry.test/other:latest", "spec", "trainer", "image")
			} else {
				_ = unstructured.SetNestedField(f.pvc, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "metadata", "uid")
			}
			f.mu.Unlock()
			if _, err := a.FindTraining(context.Background(), f.plan); err == nil {
				t.Fatal("matching annotations must not hide substituted spec/PVC")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.posts != 1 {
				t.Fatalf("mismatch caused another create: %d", f.posts)
			}
		})
	}
}

func TestTrainingObserveRequiresRealPodExitAndChecksHistoricalWriters(t *testing.T) {
	f := newTrainingFixture(t)
	a := f.adapter(t)
	handle, err := a.CreateTraining(context.Background(), f.plan)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.setTerminal(t, 0)
	observation, err := a.ObserveTraining(context.Background(), f.plan, handle, nil)
	if err != nil || observation.Outcome != "SUCCEEDED" || !observation.WritersAbsent {
		t.Fatalf("controller plus real zero exit: %+v / %v", observation, err)
	}
	if len(observation.Resources) < 4 {
		t.Fatalf("lost TrainJob/JobSet/Job/Pod history: %+v", observation.Resources)
	}
	f.mu.Lock()
	f.oldPod = trainingObject(t, `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"cpu-execution","name":"prior-training-pod","uid":"11111111-1111-4111-8111-111111111111","ownerReferences":[{"apiVersion":"batch/v1","kind":"Job","name":"old-training-job","uid":"22222222-2222-4222-8222-222222222222","controller":true}]},"spec":{"serviceAccountName":"cpu-training","containers":[{"name":"node"}]},"status":{"phase":"Running","containerStatuses":[{"name":"node","state":{"running":{"startedAt":"2026-10-03T00:00:00Z"}}}]}}`)
	f.mu.Unlock()
	history := append(observation.Resources, biz.RuntimeResource{APIVersion: "v1", Kind: "Pod", Namespace: "cpu-execution", Name: "prior-training-pod", UID: "11111111-1111-4111-8111-111111111111", OwnerUID: "22222222-2222-4222-8222-222222222222"})
	observation, err = a.ObserveTraining(context.Background(), f.plan, handle, history)
	if err != nil || observation.WritersAbsent {
		t.Fatalf("old active Pod must keep writers present: %+v / %v", observation, err)
	}
	f.mu.Lock()
	f.oldPod = nil
	f.mu.Unlock()
	observation, err = a.ObserveTraining(context.Background(), f.plan, handle, history)
	if err == nil && observation.WritersAbsent {
		t.Fatal("missing historical Pod is not writer-absence proof")
	}
}

func TestTrainingFailureAndSuspendDoNotMistakeControllerAckForWriterAbsence(t *testing.T) {
	f := newTrainingFixture(t)
	a := f.adapter(t)
	handle, err := a.CreateTraining(context.Background(), f.plan)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.StopTraining(context.Background(), f.plan, handle); err != nil {
		t.Fatalf("suspend exact TrainJob: %v", err)
	}
	observation, err := a.ObserveTraining(context.Background(), f.plan, handle, nil)
	if err != nil || observation.WritersAbsent {
		t.Fatalf("suspend accepted while Pod still running: %+v / %v", observation, err)
	}
	f.setTerminal(t, 7)
	observation, err = a.ObserveTraining(context.Background(), f.plan, handle, observation.Resources)
	if err != nil || observation.Outcome != "FAILED" || !observation.WritersAbsent {
		t.Fatalf("real failed exit must propagate: %+v / %v", observation, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.patches != 1 || f.deletes != 0 {
		t.Fatalf("stop must suspend, retain workspace: patches=%d deletes=%d", f.patches, f.deletes)
	}
}

func TestBoundTrainingStopsAndObservesWithoutCurrentRuntimeOrPVC(t *testing.T) {
	for _, mode := range []string{"runtime missing", "runtime drift", "pvc missing", "pvc replacement", "trainjob spec drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newTrainingFixture(t)
			a := f.adapter(t)
			handle, err := a.CreateTraining(context.Background(), f.plan)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			switch mode {
			case "runtime missing":
				f.runtime = nil
			case "runtime drift":
				f.runtime["spec"] = map[string]any{}
			case "pvc missing":
				f.pvc = nil
			case "pvc replacement":
				_ = unstructured.SetNestedField(f.pvc, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "metadata", "uid")
			case "trainjob spec drift":
				_ = unstructured.SetNestedField(f.train, "registry.test/drift:latest", "spec", "trainer", "image")
			}
			f.mu.Unlock()
			if err := a.StopTraining(context.Background(), f.plan, handle); err != nil {
				t.Fatalf("original computation must remain stoppable: %v", err)
			}
			observation, err := a.ObserveTraining(context.Background(), f.plan, handle, nil)
			if err != nil || observation.WritersAbsent {
				t.Fatalf("suspend alone must not prove writer exit: %+v, %v", observation, err)
			}
			f.setTerminal(t, 7)
			observation, err = a.ObserveTraining(context.Background(), f.plan, handle, observation.Resources)
			if err != nil || !observation.WritersAbsent || observation.Outcome != "FAILED" {
				t.Fatalf("original failed writers must be observable: %+v, %v", observation, err)
			}
			if f.posts != 1 || f.patches != 1 || f.deletes != 0 {
				t.Fatalf("stop recreated or deleted resources: posts=%d patches=%d deletes=%d", f.posts, f.patches, f.deletes)
			}
		})
	}
}

func TestBoundTrainingStopRejectsReplacementUIDOrScope(t *testing.T) {
	for _, mode := range []string{"trainjob uid", "namespace uid", "tenant", "execution", "spec hash", "request hash", "workspace"} {
		t.Run(mode, func(t *testing.T) {
			f := newTrainingFixture(t)
			a := f.adapter(t)
			handle, err := a.CreateTraining(context.Background(), f.plan)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			switch mode {
			case "trainjob uid":
				_ = unstructured.SetNestedField(f.train, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "metadata", "uid")
			case "namespace uid":
				_ = unstructured.SetNestedField(f.namespace, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "metadata", "uid")
			default:
				key := map[string]string{"tenant": "tenant-id", "execution": "execution-id", "spec hash": "execution-spec-sha256", "request hash": "training-request-sha256", "workspace": "workspace-uid"}[mode]
				_ = unstructured.SetNestedField(f.train, "substituted", "metadata", "annotations", "modeldev.ani.io/"+key)
			}
			f.mu.Unlock()
			if err := a.StopTraining(context.Background(), f.plan, handle); !errors.Is(err, biz.ErrRuntimeConflict) {
				t.Fatalf("replacement or foreign scope must fail: %v", err)
			}
			if f.patches != 0 {
				t.Fatal("patched a replacement or foreign object")
			}
		})
	}
}

func TestTrainingObservationPreservesConfirmedPodExitAfterGarbageCollection(t *testing.T) {
	f := newTrainingFixture(t)
	a := f.adapter(t)
	handle, err := a.CreateTraining(context.Background(), f.plan)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.setTerminal(t, 0)
	confirmed, err := a.ObserveTraining(context.Background(), f.plan, handle, nil)
	if err != nil || !confirmed.WritersAbsent || confirmed.Outcome != "SUCCEEDED" {
		t.Fatalf("precondition: current controller and actual Pod exit must be proven: %+v / %v", confirmed, err)
	}
	f.mu.Lock()
	f.pod = nil
	f.mu.Unlock()
	t.Run("confirmed exit survives API garbage collection", func(t *testing.T) {
		observation, err := a.ObserveTraining(context.Background(), f.plan, handle, confirmed.Resources)
		if err != nil || !observation.WritersAbsent || observation.Outcome != "SUCCEEDED" {
			t.Fatalf("GC must retain the observed exact-UID exit while current controllers remain terminal: %+v / %v", observation, err)
		}
		for _, fact := range observation.Resources {
			if fact.UID != childPodUID {
				continue
			}
			if fact.APIObjectPresent || !fact.Terminal || fact.ExitCode == nil || *fact.ExitCode != 0 || fact.OwnerUID != childJobUID {
				t.Fatalf("GC erased the durable exit or invented API presence: %+v", fact)
			}
			return
		}
		t.Fatal("GC dropped the known Pod identity")
	})
	t.Run("missing Pod without prior exit remains unproven", func(t *testing.T) {
		history := append(append([]biz.RuntimeResource{}, confirmed.Resources...), biz.RuntimeResource{
			APIVersion: "v1", Kind: "Pod", Namespace: "cpu-execution", Name: "prior-training-pod",
			UID: "11111111-1111-4111-8111-111111111111", OwnerUID: "22222222-2222-4222-8222-222222222222",
		})
		observation, err := a.ObserveTraining(context.Background(), f.plan, handle, history)
		if err != nil || observation.WritersAbsent {
			t.Fatalf("404 without a prior terminal exit cannot prove writers absent: %+v / %v", observation, err)
		}
		for _, fact := range observation.Resources {
			if fact.Name == "prior-training-pod" && (fact.APIObjectPresent || fact.Terminal || fact.ExitCode != nil) {
				t.Fatalf("missing unproven Pod acquired a terminal fact: %+v", fact)
			}
		}
	})
}

const (
	trainingNamespaceUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	trainingUID          = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	workspaceUID         = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	jobSetUID            = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	childJobUID          = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	childPodUID          = "ffffffff-ffff-4fff-8fff-ffffffffffff"
)

type trainingFixture struct {
	mu                                                                 sync.Mutex
	plan                                                               biz.TrainingPlan
	namespace, pvc, runtime, train, jobset, job, pod, oldPod, podGroup map[string]any
	posts, patches, deletes                                            int
	podPatches                                                         int
	podPatchStatusCode                                                 int
	beforePodPatch                                                     func()
	loseCreateResponse                                                 bool
	createStatusCode                                                   int
	createStatusBody                                                   string
	createStatusTruncated                                              bool
	podListResourceVersion                                             string
}

func newTrainingFixture(t *testing.T) *trainingFixture {
	t.Helper()
	f := &trainingFixture{}
	f.namespace = trainingObject(t, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"cpu-execution","uid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}`)
	f.pvc = trainingObject(t, `{"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"namespace":"cpu-execution","name":"cpu-workspace","uid":"cccccccc-cccc-4ccc-8ccc-cccccccccccc"},"spec":{"storageClassName":"task-workspace","accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"2147483648"}}},"status":{"phase":"Bound"}}`)
	f.runtime = trainingObject(t, `{"apiVersion":"trainer.kubeflow.org/v1alpha1","kind":"ClusterTrainingRuntime","metadata":{"name":"cpu-runtime","uid":"99999999-9999-4999-8999-999999999999"},"spec":{"mlPolicy":{"numNodes":1},"template":{"spec":{"failurePolicy":{"maxRestarts":0},"replicatedJobs":[{"name":"trainer","replicas":1,"template":{"metadata":{"labels":{"trainer.kubeflow.org/trainjob-ancestor-step":"trainer"}},"spec":{"parallelism":1,"completions":1,"backoffLimit":0,"template":{"spec":{"restartPolicy":"Never","automountServiceAccountToken":false,"securityContext":{"runAsNonRoot":true,"runAsUser":1000,"fsGroup":1000},"containers":[{"name":"node","image":"registry.test/cpu@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}}}}]}}}}`)
	f.jobset = trainingObject(t, `{"apiVersion":"jobset.x-k8s.io/v1alpha2","kind":"JobSet","metadata":{"namespace":"cpu-execution","name":"md-22222222-2222-4222-8222-222222222222","uid":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","generation":1,"ownerReferences":[{"apiVersion":"trainer.kubeflow.org/v1alpha1","kind":"TrainJob","name":"md-22222222-2222-4222-8222-222222222222","uid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","controller":true}]},"spec":{"suspend":false},"status":{"conditions":[]}}`)
	f.job = trainingObject(t, `{"apiVersion":"batch/v1","kind":"Job","metadata":{"namespace":"cpu-execution","name":"md-22222222-2222-4222-8222-222222222222-trainer-0","uid":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","ownerReferences":[{"apiVersion":"jobset.x-k8s.io/v1alpha2","kind":"JobSet","name":"md-22222222-2222-4222-8222-222222222222","uid":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","controller":true}]},"spec":{"backoffLimit":0,"parallelism":1,"completions":1},"status":{"active":1,"conditions":[]}}`)
	f.pod = trainingObject(t, `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"cpu-execution","name":"training-pod","uid":"ffffffff-ffff-4fff-8fff-ffffffffffff","ownerReferences":[{"apiVersion":"batch/v1","kind":"Job","name":"md-22222222-2222-4222-8222-222222222222-trainer-0","uid":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","controller":true}]},"spec":{"serviceAccountName":"cpu-training","restartPolicy":"Never","automountServiceAccountToken":false,"containers":[{"name":"node"}]},"status":{"phase":"Running","containerStatuses":[{"name":"node","state":{"running":{"startedAt":"2026-10-03T00:00:00Z"}}}]}}`)

	seed := conformance.SnapshotV1()
	seed.Environment.NamespaceName, seed.Environment.NamespaceUID = "cpu-execution", trainingNamespaceUID
	seed.Environment.Identities.TrainerServiceAccount = "cpu-training"
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	seed.DeadlineAt = accepted.Add(time.Hour)
	admission := admissionfixture.Bind(t, biz.Admission{TenantID: "11111111-1111-4111-8111-111111111111", OperationID: "33333333-3333-4333-8333-333333333333", ExecutionID: "22222222-2222-4222-8222-222222222222", Actor: "governance:user:trainer-fixture", Intent: conformance.IntentV1(), Snapshot: seed, AcceptedAt: accepted})
	workspace := biz.WorkspaceBinding{Mode: seed.Workspace.Mode, NamespaceName: "cpu-execution", NamespaceUID: trainingNamespaceUID, PVCName: biz.ExpectedWorkspacePVCName(admission.ExecutionID), PVCUID: workspaceUID, InputSubpath: seed.Workspace.InputSubpath, TrainingSubpath: seed.Workspace.TrainingSubpath, ReportsSubpath: seed.Workspace.ReportsSubpath, PublicationSubpath: seed.Workspace.PublicationSubpath, PreparedManifestSHA256: strings.Repeat("1", 64), PreparedManifestBytes: 256}
	_ = unstructured.SetNestedField(f.pvc, workspace.PVCName, "metadata", "name")
	plan, err := biz.FreezeTrainingPlan(biz.Execution{Admission: admission}, workspace, accepted)
	if err != nil {
		t.Fatal(err)
	}
	f.plan, err = biz.BindTrainingRuntime(plan, biz.RuntimeTemplateBinding{NamespaceUID: trainingNamespaceUID, Name: admission.Snapshot.RuntimePlan.Name, UID: "99999999-9999-4999-8999-999999999999", ObservedSpecSHA256: admission.Snapshot.RuntimePlan.ExpectedSpecSHA256})
	if err != nil {
		t.Fatal(err)
	}
	installFixtureRuntime(t, f)

	return f
}

func trainingObject(t *testing.T, source string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(source), &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func (f *trainingFixture) setTerminal(t *testing.T, code int32) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	phase, trainCondition, setCondition := "Succeeded", "Complete", "Completed"
	if code != 0 {
		phase, trainCondition, setCondition = "Failed", "Failed", "Failed"
	}
	f.train["status"] = map[string]any{"conditions": []any{map[string]any{"type": trainCondition, "status": "True", "observedGeneration": float64(1), "reason": "Terminal", "lastTransitionTime": "2026-10-03T00:00:00Z"}}}
	f.jobset["status"] = map[string]any{"conditions": []any{map[string]any{"type": setCondition, "status": "True", "observedGeneration": float64(1)}}}
	f.job["status"] = map[string]any{"active": float64(0), "conditions": []any{map[string]any{"type": trainCondition, "status": "True"}}}
	f.pod["status"] = map[string]any{"phase": phase, "containerStatuses": []any{map[string]any{"name": "node", "restartCount": float64(0), "state": map[string]any{"terminated": map[string]any{"exitCode": float64(code), "reason": phase, "finishedAt": "2026-10-03T00:00:00Z"}}}}}
}

func (f *trainingFixture) adapter(t *testing.T) *trainer.Adapter {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			f.deletes++
			t.Error("training adapter must not delete retained resources")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		path := r.URL.Path
		var response any
		switch {
		case path == "/api/v1/namespaces/cpu-execution":
			response = f.namespace
		case path == "/api/v1/namespaces/cpu-execution/persistentvolumeclaims/"+f.plan.Workspace.PVCName:
			response = f.pvc
		case path == "/apis/trainer.kubeflow.org/v1alpha1/namespaces/cpu-execution/trainingruntimes/"+f.plan.Snapshot.RuntimePlan.Name:
			response = f.runtime
		case path == "/apis/trainer.kubeflow.org/v1alpha1/namespaces/cpu-execution/trainjobs" && r.Method == http.MethodPost:
			f.posts++
			if f.createStatusCode != 0 {
				if f.createStatusTruncated {
					w.Header().Set("Content-Length", strconv.Itoa(len(f.createStatusBody)+1))
				}
				w.WriteHeader(f.createStatusCode)
				_, _ = w.Write([]byte(f.createStatusBody))
				return
			}
			if f.train != nil {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&f.train); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = unstructured.SetNestedField(f.train, trainingUID, "metadata", "uid")
			_ = unstructured.SetNestedField(f.train, "1", "metadata", "resourceVersion")
			_ = unstructured.SetNestedField(f.train, float64(1), "metadata", "generation")
			if f.loseCreateResponse {
				w.WriteHeader(http.StatusGatewayTimeout)
				return
			}
			w.WriteHeader(http.StatusCreated)
			response = f.train
		case path == "/apis/trainer.kubeflow.org/v1alpha1/namespaces/cpu-execution/trainjobs/"+f.plan.Name:
			if r.Method == http.MethodPatch {
				f.patches++
				var patch []map[string]any
				if r.Header.Get("Content-Type") != "application/json-patch+json" || json.NewDecoder(r.Body).Decode(&patch) != nil {
					t.Error("Stop must use conditional JSON patch")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				uidTest, versionTest, suspend := false, false, false
				for _, op := range patch {
					if op["op"] == "test" && op["path"] == "/metadata/uid" && op["value"] == trainingUID {
						uidTest = true
					}
					if op["op"] == "test" && op["path"] == "/metadata/resourceVersion" && op["value"] == "1" {
						versionTest = true
					}
					if (op["op"] == "add" || op["op"] == "replace") && op["path"] == "/spec/suspend" && op["value"] == true {
						suspend = true
					}
				}
				if !uidTest || !versionTest || !suspend {
					t.Error("Stop omitted UID/version/suspend binding")
					w.WriteHeader(http.StatusConflict)
					return
				}
				_ = unstructured.SetNestedField(f.train, true, "spec", "suspend")
			}
			response = f.train
		case path == "/apis/jobset.x-k8s.io/v1alpha2/namespaces/cpu-execution/jobsets/"+f.plan.Name:
			response = f.jobset
		case path == "/apis/scheduling.volcano.sh/v1beta1/namespaces/cpu-execution/podgroups/"+f.plan.Name:
			response = f.podGroup
		case path == "/apis/batch/v1/namespaces/cpu-execution/jobs":
			response = map[string]any{"apiVersion": "batch/v1", "kind": "JobList", "metadata": map[string]any{}, "items": []any{f.job}}
		case path == "/api/v1/namespaces/cpu-execution/pods":
			items := []any{}
			if f.pod != nil {
				items = append(items, f.pod)
			}
			response = map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{"resourceVersion": f.podListResourceVersion}, "items": items}
		case path == "/apis/batch/v1/namespaces/cpu-execution/jobs/"+f.job["metadata"].(map[string]any)["name"].(string):
			response = f.job
		case path == "/api/v1/namespaces/cpu-execution/pods/training-pod":
			if r.Method == http.MethodPatch {
				f.podPatches++
				if f.beforePodPatch != nil {
					f.beforePodPatch()
				}
				if f.podPatchStatusCode != 0 {
					w.WriteHeader(f.podPatchStatusCode)
					_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "Forbidden", "code": f.podPatchStatusCode})
					return
				}
				var patch []map[string]any
				if r.Header.Get("Content-Type") != "application/json-patch+json" || json.NewDecoder(r.Body).Decode(&patch) != nil || f.pod == nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				metadata := f.pod["metadata"].(map[string]any)
				tested := make(map[string]bool)
				var finalizers any
				for _, op := range patch {
					field, ok := op["path"].(string)
					if !ok || !strings.HasPrefix(field, "/metadata/") {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					field = strings.TrimPrefix(field, "/metadata/")
					if op["op"] == "test" && reflect.DeepEqual(op["value"], metadata[field]) {
						tested[field] = true
					} else if op["op"] == "replace" && field == "finalizers" {
						finalizers = op["value"]
					} else {
						w.WriteHeader(http.StatusConflict)
						return
					}
				}
				if !tested["uid"] || !tested["resourceVersion"] || !tested["ownerReferences"] || !tested["finalizers"] || finalizers == nil {
					t.Error("Pod finalizer removal omitted an identity or ownership guard")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				metadata["finalizers"], metadata["resourceVersion"] = finalizers, "2"
				if len(finalizers.([]any)) == 0 {
					delete(metadata, "finalizers")
				}
			}
			response = f.pod
		case path == "/api/v1/namespaces/cpu-execution/pods/prior-training-pod":
			response = f.oldPod
		default:
			t.Errorf("unexpected Kubernetes operation %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if response == nil || (reflectNilMap(response)) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404})
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	client, err := dynamic.NewForConfig(&rest.Config{Host: server.URL, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}})
	if err != nil {
		t.Fatal(err)
	}
	return trainer.New(client)
}

func reflectNilMap(value any) bool { object, ok := value.(map[string]any); return ok && object == nil }

func installFixtureRuntime(t *testing.T, f *trainingFixture) {
	t.Helper()
	var spec map[string]any
	if err := json.Unmarshal(f.plan.Snapshot.RuntimePlan.ExpectedSpec, &spec); err != nil {
		t.Fatal(err)
	}
	f.runtime = map[string]any{"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainingRuntime", "metadata": map[string]any{"namespace": f.plan.Workspace.NamespaceName, "name": f.plan.Snapshot.RuntimePlan.Name, "uid": f.plan.RuntimeBinding.UID, "annotations": map[string]any{"modeldev.ani.io/tenant-id": f.plan.TenantID, "modeldev.ani.io/execution-id": f.plan.ExecutionID, "modeldev.ani.io/create-operation-id": f.plan.OperationID, "modeldev.ani.io/execution-spec-sha256": f.plan.SpecHash, "modeldev.ani.io/runtime-spec-sha256": f.plan.Snapshot.RuntimePlan.ExpectedSpecSHA256, "modeldev.ani.io/workspace-uid": f.plan.Workspace.PVCUID}}, "spec": spec}
	jobs, _, _ := unstructured.NestedSlice(spec, "template", "spec", "replicatedJobs")
	podSpec, _, _ := unstructured.NestedMap(jobs[0].(map[string]any), "template", "spec", "template", "spec")
	f.pod["spec"] = podSpec
	vector, err := biz.TrainingResourceVector(f.plan.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	minimum := map[string]any{}
	for key, value := range vector {
		minimum[key] = value
	}
	queue := "default"
	if f.plan.Snapshot.GPU != nil {
		queue = f.plan.Snapshot.GPU.Execution.QueueName
	}
	f.podGroup = map[string]any{"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "PodGroup", "metadata": map[string]any{"name": f.plan.Name, "namespace": f.plan.Workspace.NamespaceName, "uid": "podgroup-original", "ownerReferences": []any{map[string]any{"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainJob", "name": f.plan.Name, "uid": trainingUID, "controller": true}}}, "spec": map[string]any{"minMember": int64(1), "minResources": minimum, "queue": queue}}
}
