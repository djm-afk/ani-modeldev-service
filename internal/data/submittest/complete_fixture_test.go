//go:build cpu_mainflow

package submittest_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const completeRunID = "55555555-6666-4777-8888-999999999999"
const completeWorkflowUID = "cccccccc-dddd-4eee-8fff-111111111111"
const completePVCUID = "abababab-cdcd-4efe-8aaa-111111111111"
const completeTrainUID = "babababa-dcdc-4fef-8bbb-222222222222"
const completeSetUID = "dddddddd-aaaa-4bbb-8ccc-333333333333"
const completeJobUID = "eeeeeeee-aaaa-4bbb-8ccc-444444444444"
const completePodUID = "ffffffff-aaaa-4bbb-8ccc-555555555555"

type completeFixture struct {
	t                    *testing.T
	mu                   sync.Mutex
	request              biz.PipelineDispatchRequest
	root, source, image  string
	kube, kfp, storage   *httptest.Server
	objects              map[string]map[string]any
	blobs                map[string][]byte
	versions             map[string]string
	skipped              map[string]bool
	creates, runCreates  int
	runStops, trainStops int
	runStoppedAt         string
	runSucceededAt       string
	trainingContainer    string
	trainingStarted      bool
	traceTrainingCreate  bool
	trainingDone         chan struct{}
	trainingErr          error
	trainingUncertain    bool
	trainingLog          []byte
	workspace            biz.WorkspaceBinding
	stepDeletes          int
	archivedStepStates   map[string]string
	archivedStepFinishes map[string]string
	runTasks             []any
	facts                *lifecycle.Repository
	computeDeletes       []string
}

func newCompleteFixture(t *testing.T) *completeFixture {
	t.Helper()
	f := &completeFixture{t: t, root: t.TempDir(), objects: map[string]map[string]any{}, blobs: map[string][]byte{}, versions: map[string]string{}, trainingDone: make(chan struct{}), trainingContainer: "cpu-p01-mainflow-" + uuid.NewString()}
	f.image = os.Getenv("CPU_P01_MLP_IMAGE")
	if f.image == "" {
		t.Fatal("CPU_MAINFLOW_PREFLIGHT: real CPU image missing; behavior NOT_RUN")
	}
	f.source, _ = filepath.Abs("../../../training")
	f.request = dispatchRequest(t)
	f.request.Admission.Actor = "governance:user:42"
	dataPath := filepath.Join(f.root, "source-data.csv")
	if output, err := exec.CommandContext(context.Background(), "python3", filepath.Join(f.source, "make_data.py"), "--output", dataPath).CombinedOutput(); err != nil {
		t.Fatalf("CPU_MAINFLOW_PREFLIGHT: fixed CSV generator failed: %v: %s", err, output)
	}
	data, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &f.request.Admission.Snapshot
	snapshot.IdentityRef.Actor = f.request.Admission.Actor
	// Freeze the actual locally qualified image manifest used by Podman. The
	// external TrainJob substitute may not replace it with a synthetic digest.
	snapshot.Program.ImageDigest = f.image
	snapshot.Program.Command = []string{"/opt/venv/bin/python", "-I", "/opt/ani-modeldev/training/train_mlp.py"}
	snapshot.Input.Object.SizeBytes = int64(len(data))
	snapshot.Input.Object.SHA256 = completeHash(data)
	snapshot.Input.RowCount = 1024
	snapshot.Input.PreparedDataset = cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: int64(len(data)), SHA256: snapshot.Input.Object.SHA256, RowCount: snapshot.Input.RowCount}
	dataset, err := cpup01.BuildDatasetManifest(snapshot.Input)
	if err != nil {
		t.Fatal(err)
	}
	datasetSHA, err := dataset.Digest()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Program.ResolvedArgs = []string{"--dataset-manifest", "/inputs/dataset-manifest.json", "--expected-dataset-manifest-sha256", datasetSHA, "--work-root", "/workspace/work/train", "--output", "/outputs", "--learning-rate", "0.01"}
	snapshot.Workspace.Mode = "EXECUTION_PVC"
	f.workspace = biz.WorkspaceBinding{Mode: snapshot.Workspace.Mode, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, PVCName: biz.ExpectedWorkspacePVCName(f.request.Admission.ExecutionID), PVCUID: completePVCUID, InputSubpath: snapshot.Workspace.InputSubpath, TrainingSubpath: snapshot.Workspace.TrainingSubpath, ReportsSubpath: snapshot.Workspace.ReportsSubpath, PublicationSubpath: snapshot.Workspace.PublicationSubpath, WorkSubpath: snapshot.Workspace.WorkSubpath}
	ns := snapshot.Environment.NamespaceName
	meta := func(name, uid string) map[string]any {
		return map[string]any{"name": name, "namespace": ns, "uid": uid, "resourceVersion": "1", "generation": 1}
	}
	f.objects["/api/v1/namespaces/"+ns] = map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns, "uid": snapshot.Environment.NamespaceUID}}
	f.objects["/api/v1/namespaces/"+ns+"/persistentvolumeclaims/"+f.workspace.PVCName] = map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta(f.workspace.PVCName, completePVCUID), "spec": map[string]any{"storageClassName": snapshot.Workspace.StorageClass, "accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": fmt.Sprint(snapshot.Workspace.CapacityBytes)}}}, "status": map[string]string{"phase": "Bound"}}
	f.objects["/api/v1/namespaces/"+ns+"/serviceaccounts/"+snapshot.Environment.Identities.KFPStepServiceAccount] = map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": meta(snapshot.Environment.Identities.KFPStepServiceAccount, "99999999-aaaa-4bbb-8ccc-666666666666")}
	f.objects["/apis/argoproj.io/v1alpha1/namespaces/"+ns+"/workflows/main-flow"] = map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "metadata": meta("main-flow", completeWorkflowUID)}
	for _, step := range []string{"prepare", "train-wait", "collect", "publish", "close"} {
		m := meta("main-"+step, completeStepUID(step))
		m["annotations"] = map[string]any{"workflows.argoproj.io/node-id": "main-" + step, "workflows.argoproj.io/node-name": "main-flow." + step}
		m["ownerReferences"] = []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": "main-flow", "uid": completeWorkflowUID, "controller": true}}
		f.objects["/api/v1/namespaces/"+ns+"/pods/main-"+step] = map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": m, "spec": map[string]any{"serviceAccountName": snapshot.Environment.Identities.KFPStepServiceAccount, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": f.workspace.PVCName}}}, "containers": []any{map[string]any{"name": "main", "volumeMounts": []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}}}}}, "status": map[string]any{"phase": "Running"}}
		if step == "prepare" {
			spec := f.objects["/api/v1/namespaces/"+ns+"/pods/main-"+step]["spec"].(map[string]any)
			main := spec["containers"].([]any)[0].(map[string]any)
			main["volumeMounts"] = append(main["volumeMounts"].([]any), map[string]any{"name": "workspace", "mountPath": "/workspace/work/prepare/tmp", "subPath": "work/prepare/tmp"})
		}
		if step == "close" {
			f.objects["/api/v1/namespaces/"+ns+"/pods/main-"+step]["spec"] = map[string]any{"serviceAccountName": snapshot.Environment.Identities.KFPStepServiceAccount, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": f.workspace.PVCName}}}, "containers": []any{map[string]any{"name": "main", "volumeMounts": []any{map[string]any{"name": "workspace", "mountPath": "/workspace", "readOnly": true}}}}}
		}
	}
	snapshot.RuntimePlan, err = biz.CompileRuntimePlan(*snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f.request.Admission.SpecHash, err = snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	inputKey := "/" + snapshot.Input.Object.Bucket + "/" + snapshot.Input.Object.Key
	f.blobs[inputKey] = data
	f.versions[inputKey] = *snapshot.Input.Object.VersionID
	f.kube = httptest.NewTLSServer(http.HandlerFunc(f.kubeRequest))
	t.Cleanup(f.kube.Close)
	f.kfp = httptest.NewTLSServer(http.HandlerFunc(f.kfpRequest))
	t.Cleanup(f.kfp.Close)
	f.storage = httptest.NewTLSServer(http.HandlerFunc(f.storageRequest))
	t.Cleanup(f.storage.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := f.stopActualTraining(ctx); err != nil {
			t.Errorf("actual training cleanup failed: %v", err)
		}
	})
	return f
}

func completeHash(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
func completeStepUID(step string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cpu-p01-mainflow-"+step)).String()
}

func (f *completeFixture) kubeRequest(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.traceTrainingCreate {
		f.t.Logf("MAIN_FLOW_TRAIN_CREATE: %s %s", r.Method, r.URL.Path)
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer synthetic-kube-owner" {
		w.WriteHeader(401)
		return
	}
	ns := f.workspace.NamespaceName
	if r.URL.Path == "/apis/authentication.k8s.io/v1/tokenreviews" {
		var input struct {
			Spec struct {
				Token     string   `json:"token"`
				Audiences []string `json:"audiences"`
			} `json:"spec"`
		}
		if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&input) != nil {
			w.WriteHeader(400)
			return
		}
		step := strings.TrimPrefix(input.Spec.Token, "synthetic-bound-")
		valid := false
		for _, s := range []string{"prepare", "train-wait", "collect", "publish", "close"} {
			valid = valid || step == s
		}
		if len(input.Spec.Audiences) != 1 || input.Spec.Audiences[0] != "ani-modeldev-managed-step" {
			valid = false
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "TokenReview", "status": map[string]any{"authenticated": valid, "audiences": []string{"ani-modeldev-managed-step"}, "user": map[string]any{"username": "system:serviceaccount:" + ns + ":" + f.request.Admission.Snapshot.Environment.Identities.KFPStepServiceAccount, "uid": "99999999-aaaa-4bbb-8ccc-666666666666", "extra": map[string]any{"authentication.kubernetes.io/pod-name": []string{"main-" + step}, "authentication.kubernetes.io/pod-uid": []string{completeStepUID(step)}}}}})
		return
	}
	runtimePath := "/apis/trainer.kubeflow.org/v1alpha1/namespaces/" + ns + "/trainingruntimes"
	if r.Method == "POST" && r.URL.Path == runtimePath {
		var object map[string]any
		if json.NewDecoder(r.Body).Decode(&object) != nil {
			w.WriteHeader(400)
			return
		}
		meta := object["metadata"].(map[string]any)
		key := runtimePath + "/" + meta["name"].(string)
		if f.objects[key] != nil {
			w.WriteHeader(409)
			return
		}
		meta["uid"], meta["resourceVersion"], meta["generation"] = "77777777-aaaa-4bbb-8ccc-111111111111", "1", 1
		f.objects[key] = object
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(object)
		return
	}
	trainPath := "/apis/trainer.kubeflow.org/v1alpha1/namespaces/" + ns + "/trainjobs"
	if r.Method == "POST" && r.URL.Path == trainPath {
		f.creates++
		var job map[string]any
		if json.NewDecoder(r.Body).Decode(&job) != nil {
			w.WriteHeader(400)
			return
		}
		m := job["metadata"].(map[string]any)
		name := m["name"].(string)
		if f.objects[trainPath+"/"+name] != nil {
			w.WriteHeader(409)
			return
		}
		m["uid"] = completeTrainUID
		m["resourceVersion"] = "1"
		m["generation"] = 1
		f.objects[trainPath+"/"+name] = job
		f.makeTrainingChildren(name)
		f.trainingStarted = true
		go f.runTraining(name)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(job)
		return
	}
	if r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, trainPath+"/") {
		job := f.objects[r.URL.Path]
		if job == nil {
			w.WriteHeader(404)
			return
		}
		job["spec"].(map[string]any)["suspend"] = true
		f.trainStops++
		// Controller acceptance alone is not termination. Release the fixture
		// mutex so the actual process waiter can record its observed exit.
		f.mu.Unlock()
		err := f.stopActualTraining(r.Context())
		f.mu.Lock()
		if err != nil {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(job)
		return
	}
	if os.Getenv("ANI_MODELDEV_WEBHOOK_MAINFLOW") != "" && f.handleRetainedPodMutation(w, r) {
		return
	}
	collections := map[string]bool{
		"/api/v1/namespaces/" + ns + "/pods":                                         true,
		"/apis/batch/v1/namespaces/" + ns + "/jobs":                                  true,
		"/apis/jobset.x-k8s.io/v1alpha2/namespaces/" + ns + "/jobsets":               true,
		"/apis/scheduling.volcano.sh/v1beta1/namespaces/" + ns + "/podgroups":        true,
		"/apis/trainer.kubeflow.org/v1alpha1/namespaces/" + ns + "/trainjobs":        true,
		"/apis/trainer.kubeflow.org/v1alpha1/namespaces/" + ns + "/trainingruntimes": true,
		"/apis/argoproj.io/v1alpha1/namespaces/" + ns + "/workflows":                 true,
	}
	if r.Method == http.MethodDelete && collections[r.URL.Path[:strings.LastIndex(r.URL.Path, "/")]] {
		f.deleteOriginalComputeLocked(w, r)
		return
	}
	if r.Method != "GET" {
		f.t.Error("unexpected Kubernetes mutation")
		w.WriteHeader(405)
		return
	}
	if r.URL.Path == "/api/v1/namespaces/"+ns+"/pods/actual-training-pod/log" {
		if f.objects[strings.TrimSuffix(r.URL.Path, "/log")] == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404})
			return
		}
		if r.URL.Query().Get("container") != "node" || r.URL.Query().Get("timestamps") != "true" || (r.URL.Query().Get("follow") != "" && r.URL.Query().Get("follow") != "false") || (r.URL.Query().Get("previous") != "" && r.URL.Query().Get("previous") != "false") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tail, tailErr := strconv.Atoi(r.URL.Query().Get("tailLines"))
		limit, limitErr := strconv.Atoi(r.URL.Query().Get("limitBytes"))
		if tailErr != nil || limitErr != nil || tail < 1 || tail > 1001 || limit < 1 || limit > 65537 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Only the actual container stdout is exposed. Kubernetes supplies its
		// timestamp envelope; the test substitute preserves the original text.
		lines := strings.Split(strings.TrimSuffix(string(f.trainingLog), "\n"), "\n")
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
		var body strings.Builder
		for _, line := range lines {
			if line == "" {
				continue
			}
			var record struct {
				Timestamp string `json:"timestamp"`
			}
			_ = json.Unmarshal([]byte(line), &record)
			stamp := record.Timestamp
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				stamp = time.Now().UTC().Format(time.RFC3339Nano)
			}
			body.WriteString(stamp + " " + line + "\n")
		}
		raw := []byte(body.String())
		if len(raw) > limit {
			raw = raw[:limit]
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(raw)
		return
	}
	if object := f.objects[r.URL.Path]; object != nil {
		f.completeWaitExitLocked(object)
		_ = json.NewEncoder(w).Encode(object)
		return
	}
	if collections[r.URL.Path] {
		items := []any{}
		for p, o := range f.objects {
			if strings.HasPrefix(p, r.URL.Path+"/") {
				f.completeWaitExitLocked(o)
				items = append(items, o)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "List", "metadata": map[string]any{"resourceVersion": "1"}, "items": items})
		return
	}
	w.WriteHeader(404)
	_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404})
}

// Emulate Kubernetes' guarded Orphan removal, preserving every dependent's
// original UID/status. The real repository must commit exits, logs and exact
// delete intent before this external API can observe a mutation.
func (f *completeFixture) deleteOriginalComputeLocked(w http.ResponseWriter, r *http.Request) {
	object := f.objects[r.URL.Path]
	if object == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404})
		return
	}
	meta := object["metadata"].(map[string]any)
	var options metav1.DeleteOptions
	if json.NewDecoder(r.Body).Decode(&options) != nil || options.Preconditions == nil || options.Preconditions.UID == nil || string(*options.Preconditions.UID) != meta["uid"] || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != meta["resourceVersion"] || options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationOrphan {
		w.WriteHeader(http.StatusConflict)
		return
	}
	finalizers, _ := meta["finalizers"].([]any)
	if raw := meta["finalizers"]; raw != nil && finalizers == nil {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if len(finalizers) != 0 || f.facts == nil {
		w.WriteHeader(http.StatusConflict)
		return
	}
	state, err := f.facts.GetRuntime(r.Context(), f.request.Admission.TenantID, f.request.Admission.ExecutionID)
	if err != nil || state.ClosedAt == nil || state.StepExitCleanup == nil || state.StepExitCleanup.CompletedAt == nil || state.ComputeCleanup == nil || !state.ComputeCleanup.LogsReady {
		f.t.Error("external compute DELETE preceded committed CLOSED/exits/logs/intent", err)
		w.WriteHeader(http.StatusConflict)
		return
	}
	matched := 0
	for _, target := range state.ComputeCleanup.Requested {
		if target.APIVersion == object["apiVersion"] && target.Kind == object["kind"] && target.Namespace == meta["namespace"] && target.Name == meta["name"] && target.UID == meta["uid"] {
			matched++
		}
	}
	if matched != 1 {
		f.t.Error("external compute DELETE was outside the durable original target set")
		w.WriteHeader(http.StatusConflict)
		return
	}
	for _, child := range f.objects {
		childMeta := child["metadata"].(map[string]any)
		refs, _ := childMeta["ownerReferences"].([]any)
		remaining := []any{}
		for _, raw := range refs {
			if raw.(map[string]any)["uid"] != meta["uid"] {
				remaining = append(remaining, raw)
			}
		}
		if len(remaining) != len(refs) {
			childMeta["ownerReferences"] = remaining
			version, err := strconv.Atoi(childMeta["resourceVersion"].(string))
			if err != nil {
				f.t.Error("external original child lost its API resourceVersion", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			childMeta["resourceVersion"] = strconv.Itoa(version + 1)
		}
	}
	f.computeDeletes = append(f.computeDeletes, meta["uid"].(string))
	delete(f.objects, r.URL.Path)
	_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Success", Code: 200})
}

func (f *completeFixture) makeTrainingChildren(name string) {
	ns := f.workspace.NamespaceName
	object := func(api, kind, name, uid, ownerAPI, ownerKind, ownerName, ownerUID string) map[string]any {
		return map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns, "uid": uid, "generation": 1, "resourceVersion": "1", "ownerReferences": []any{map[string]any{"apiVersion": ownerAPI, "kind": ownerKind, "name": ownerName, "uid": ownerUID, "controller": true}}}, "status": map[string]any{"conditions": []any{}}}
	}
	set := object("jobset.x-k8s.io/v1alpha2", "JobSet", name, completeSetUID, "trainer.kubeflow.org/v1alpha1", "TrainJob", name, completeTrainUID)
	var runtimeSpec map[string]any
	if err := json.Unmarshal(f.request.Admission.Snapshot.RuntimePlan.ExpectedSpec, &runtimeSpec); err != nil {
		f.t.Error(err)
		return
	}
	setSpec := runtimeSpec["template"].(map[string]any)["spec"].(map[string]any)
	setSpec["suspend"] = false
	set["spec"] = setSpec
	f.objects["/apis/jobset.x-k8s.io/v1alpha2/namespaces/"+ns+"/jobsets/"+name] = set
	group := object("scheduling.volcano.sh/v1beta1", "PodGroup", name, "12345678-aaaa-4bbb-8ccc-111111111111", "trainer.kubeflow.org/v1alpha1", "TrainJob", name, completeTrainUID)
	vector, err := biz.TrainingResourceVector(f.request.Admission.Snapshot)
	if err != nil {
		f.t.Error(err)
		return
	}
	group["spec"] = map[string]any{"minMember": 1, "minResources": vector, "queue": "default"}
	f.objects["/apis/scheduling.volcano.sh/v1beta1/namespaces/"+ns+"/podgroups/"+name] = group
	nativeJob := setSpec["replicatedJobs"].([]any)[0].(map[string]any)
	jobSpec := nativeJob["template"].(map[string]any)["spec"].(map[string]any)
	job := object("batch/v1", "Job", name+"-node-0", completeJobUID, "jobset.x-k8s.io/v1alpha2", "JobSet", name, completeSetUID)
	job["spec"] = jobSpec
	job["status"].(map[string]any)["active"] = 1
	f.objects["/apis/batch/v1/namespaces/"+ns+"/jobs/"+name+"-node-0"] = job
	pod := object("v1", "Pod", "actual-training-pod", completePodUID, "batch/v1", "Job", name+"-node-0", completeJobUID)
	podTemplate := jobSpec["template"].(map[string]any)
	pod["spec"] = podTemplate["spec"]
	for key, value := range podTemplate["metadata"].(map[string]any) {
		pod["metadata"].(map[string]any)[key] = value
	}
	pod["status"] = map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "node", "restartCount": 0, "state": map[string]any{"running": map[string]any{"startedAt": time.Now().UTC().Format(time.RFC3339)}}}}}
	f.objects["/api/v1/namespaces/"+ns+"/pods/actual-training-pod"] = pod
}

func (f *completeFixture) runTraining(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := f.request.Admission.Snapshot.Program.Command
	resources := f.request.Admission.Snapshot.Resources
	cpu := fmt.Sprintf("%d.%03d", resources.LimitMillicpu/1000, resources.LimitMillicpu%1000)
	memory := strconv.FormatInt(resources.LimitMemoryBytes, 10)
	args := []string{"run", "--name", f.trainingContainer, "--label", "modeldev.ani.io/test-run=" + f.trainingContainer, "--pull=never", "--network", "none", "--http-proxy=false", "--cpus", cpu, "--memory", memory, "--memory-swap", memory, "--pids-limit", "256", "--cap-drop", "all", "--security-opt", "no-new-privileges", "--read-only", "--userns", "keep-id:uid=10001,gid=10001", "--volume", f.source + ":/opt/ani-modeldev/training:ro,Z", "--volume", filepath.Join(f.root, f.workspace.InputSubpath) + ":/inputs:ro,Z", "--volume", filepath.Join(f.root, f.workspace.TrainingSubpath) + ":/outputs:rw,Z", "--volume", filepath.Join(f.root, f.workspace.WorkSubpath, "train") + ":/workspace/work/train:rw,Z", "--env", "TMPDIR=/workspace/work/train/tmp", "--env", "HF_HOME=/workspace/work/train/hf", "--env", "HF_DATASETS_CACHE=/workspace/work/train/hf/datasets", "--env", "MPLCONFIGDIR=/workspace/work/train/matplotlib", "--entrypoint", command[0], f.image}
	args = append(args, command[1:]...)
	args = append(args, f.request.Admission.Snapshot.Program.ResolvedArgs...)
	output, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	read, done := context.WithTimeout(context.Background(), 20*time.Second)
	exitCode, exitErr := f.actualTrainingExit(read)
	done()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trainingErr = err
	f.trainingLog = output
	f.trainingUncertain = exitErr != nil
	f.t.Logf("MAIN_FLOW_ACTUAL_PROCESS: container=%s context_error=%v process_error=%v exit_proof_error=%v", f.trainingContainer, ctx.Err(), err, exitErr)
	if f.trainingUncertain {
		if f.trainingErr == nil {
			f.trainingErr = exitErr
		}
		// A CLI exit never supplies a fabricated worker termination. Cleanup
		// independently removes this exact task-owned container if necessary.
		close(f.trainingDone)
		return
	}
	ns := f.workspace.NamespaceName
	phase, condition, setCondition := "Succeeded", "Complete", "Completed"
	code := exitCode
	if code != 0 {
		phase = "Failed"
		condition = "Failed"
		setCondition = "Failed"
	}
	status := func(typ string) map[string]any {
		return map[string]any{"active": 0, "conditions": []any{map[string]any{"type": typ, "status": "True", "observedGeneration": 1, "reason": "ActualProcessExited", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}}}
	}
	f.objects["/apis/trainer.kubeflow.org/v1alpha1/namespaces/"+ns+"/trainjobs/"+name]["status"] = status(condition)
	f.objects["/apis/jobset.x-k8s.io/v1alpha2/namespaces/"+ns+"/jobsets/"+name]["status"] = status(setCondition)
	f.objects["/apis/batch/v1/namespaces/"+ns+"/jobs/"+name+"-node-0"]["status"] = status(condition)
	f.objects["/api/v1/namespaces/"+ns+"/pods/actual-training-pod"]["status"] = map[string]any{"phase": phase, "containerStatuses": []any{map[string]any{"name": "node", "restartCount": 0, "state": map[string]any{"terminated": map[string]any{"exitCode": code, "reason": phase, "finishedAt": time.Now().UTC().Format(time.RFC3339)}}}}}
	close(f.trainingDone)
}

func (f *completeFixture) actualTrainingExit(ctx context.Context) (int, error) {
	raw, err := exec.CommandContext(ctx, "podman", "inspect", "--type", "container", f.trainingContainer).Output()
	if err != nil {
		return 0, fmt.Errorf("actual container exit not observed: %w", err)
	}
	var rows []struct {
		ID     string `json:"Id"`
		Name   string
		Config struct{ Labels map[string]string }
		State  struct {
			Status   string
			Running  *bool
			Pid      *int
			ExitCode *int
		}
	}
	if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 {
		return 0, fmt.Errorf("actual container exit response invalid")
	}
	row := rows[0]
	if len(row.ID) != 64 || strings.TrimPrefix(row.Name, "/") != f.trainingContainer || row.Config.Labels["modeldev.ani.io/test-run"] != f.trainingContainer || row.State.Status != "exited" || row.State.Running == nil || *row.State.Running || row.State.Pid == nil || *row.State.Pid != 0 || row.State.ExitCode == nil || *row.State.ExitCode < 0 || *row.State.ExitCode > 255 {
		return 0, fmt.Errorf("task-owned actual container exit remains unproven")
	}
	return *row.State.ExitCode, nil
}

func (f *completeFixture) stopActualTraining(ctx context.Context) error {
	f.mu.Lock()
	started, container := f.trainingStarted, f.trainingContainer
	f.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-f.trainingDone:
		return f.removeTrainingContainer(ctx, container)
	default:
	}
	if output, err := exec.CommandContext(ctx, "podman", "stop", "--time", "1", container).CombinedOutput(); err != nil {
		select {
		case <-f.trainingDone:
			return f.removeTrainingContainer(ctx, container)
		default:
			return fmt.Errorf("stop task-owned training container: %w: %s", err, output)
		}
	}
	select {
	case <-f.trainingDone:
		return f.removeTrainingContainer(ctx, container)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *completeFixture) removeTrainingContainer(ctx context.Context, container string) error {
	check := exec.CommandContext(ctx, "podman", "container", "exists", container)
	if err := check.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("cannot establish task-owned container absence: %w", err)
	}
	label, err := exec.CommandContext(ctx, "podman", "inspect", "--format", `{{ index .Config.Labels "modeldev.ani.io/test-run" }}`, container).Output()
	if err != nil || strings.TrimSpace(string(label)) != container {
		return fmt.Errorf("cannot prove task-owned container identity before cleanup")
	}
	if output, err := exec.CommandContext(ctx, "podman", "rm", "--force", container).CombinedOutput(); err != nil {
		return fmt.Errorf("remove task-owned uncertain container: %w: %s", err, output)
	}
	if err := exec.CommandContext(ctx, "podman", "container", "exists", container).Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("cannot confirm task-owned container cleanup: %w", err)
	}
	return fmt.Errorf("task-owned container remains after cleanup")
}

func (f *completeFixture) kfpRequest(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer synthetic-kfp-owner" {
		w.WriteHeader(401)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/apis/v2beta1/runs" {
		f.runCreates++
		if f.currentRunTasksLocked() == nil {
			w.WriteHeader(500)
			return
		}
	} else if r.Method == "POST" && r.URL.Path == "/apis/v2beta1/runs/"+completeRunID+":terminate" {
		f.runStops++
		f.runStoppedAt = time.Now().UTC().Format(time.RFC3339Nano)
		// Only KFP's external step processes are simulated here. Prepare has
		// already returned from real component work; no training exit is set.
		for _, step := range []string{"prepare", "train-wait", "collect", "publish", "close", "driver"} {
			pod := f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-"+step]
			if pod == nil {
				continue
			}
			phase := pod["status"].(map[string]any)["phase"].(string)
			if phase != "Succeeded" && phase != "Failed" {
				phase = "Failed"
				pod["status"] = map[string]any{"phase": phase, "containerStatuses": []any{map[string]any{"name": "main", "restartCount": 0, "state": map[string]any{"terminated": map[string]any{"exitCode": 143, "reason": "KFPStopped", "finishedAt": f.runStoppedAt}}}}}
			}
			f.completeWaitExitLocked(pod)
		}
		if f.currentRunTasksLocked() == nil {
			w.WriteHeader(500)
			return
		}
		workflow := f.objects["/apis/argoproj.io/v1alpha1/namespaces/"+f.workspace.NamespaceName+"/workflows/main-flow"]
		labels, _ := workflow["metadata"].(map[string]any)["labels"].(map[string]any)
		if labels == nil {
			labels = map[string]any{}
		}
		labels["workflows.argoproj.io/completed"] = "true"
		workflow["metadata"].(map[string]any)["labels"] = labels
		status := workflow["status"].(map[string]any)
		status["phase"], status["finishedAt"] = "Failed", f.runStoppedAt
		status["conditions"] = []any{map[string]any{"type": "Completed", "status": "True"}}
		_ = json.NewEncoder(w).Encode(map[string]any{})
		return
	} else if r.Method != "GET" || r.URL.Path != "/apis/v2beta1/runs/"+completeRunID {
		w.WriteHeader(400)
		return
	}
	s := f.request.Admission.Snapshot
	tasks := f.runTasks
	if tasks == nil {
		w.WriteHeader(500)
		return
	}
	run := map[string]any{"run_id": completeRunID, "experiment_id": s.Environment.ExperimentID, "display_name": "md-" + f.request.Admission.ExecutionID, "pipeline_version_reference": map[string]string{"pipeline_id": s.Implementation.PipelineID, "pipeline_version_id": s.Implementation.PipelineVersionID}, "runtime_config": map[string]any{"parameters": map[string]string{"execution_id": f.request.Admission.ExecutionID, "spec_hash": f.request.Admission.SpecHash}, "pipeline_root": f.request.Owner.PipelineRoot}, "service_account": s.Environment.Identities.KFPStepServiceAccount, "state": "RUNNING", "run_details": map[string]any{"task_details": tasks}}
	if f.runStoppedAt != "" {
		run["state"], run["finished_at"] = "CANCELED", f.runStoppedAt
	} else if f.runSucceededAt != "" {
		run["state"], run["finished_at"] = "SUCCEEDED", f.runSucceededAt
	}
	_ = json.NewEncoder(w).Encode(run)
}

// External controllers publish their native task/Pod graph when the boundary
// changes state, before ModelDev independently reads Workflow and KFP Run.
func (f *completeFixture) currentRunTasksLocked() []any {
	s := f.request.Admission.Snapshot
	tasks := []any{}
	nodes := map[string]any{}
	for _, step := range []string{"prepare", "train-wait", "collect", "publish", "close"} {
		state := "RUNNING"
		controller, nodeID := step+"-controller", "main-"+step
		if f.skipped[step] {
			tasks = append(tasks, map[string]any{"run_id": completeRunID, "task_id": step + "-task", "display_name": step, "state": "SKIPPED"})
			nodes[controller] = map[string]any{"id": controller, "name": "main-flow." + step + "-controller", "displayName": step, "type": "DAG", "phase": "Skipped"}
			continue
		}
		pod := f.objects["/api/v1/namespaces/"+s.Environment.NamespaceName+"/pods/main-"+step]
		if pod == nil {
			state = f.archivedStepStates[step]
			if state == "" {
				f.t.Error("KFP boundary lost task history independently of Pod cleanup")
				return nil
			}
		} else if pod["status"].(map[string]any)["phase"] == "Succeeded" {
			state = "SUCCEEDED"
		} else if pod["status"].(map[string]any)["phase"] == "Failed" {
			state = "FAILED"
		}
		phase := map[string]string{"RUNNING": "Running", "SUCCEEDED": "Succeeded", "FAILED": "Failed", "CANCELED": "Failed"}[state]
		nodes[controller] = map[string]any{"id": controller, "name": "main-flow." + step + "-controller", "displayName": step, "type": "DAG", "phase": phase, "children": []any{nodeID}}
		node := map[string]any{"id": nodeID, "name": "main-flow." + step, "displayName": "executor", "type": "Pod", "phase": phase, "boundaryID": controller, "templateName": "metadata-0-2-retry-system-container-impl"}
		if state != "RUNNING" {
			finished := f.archivedStepFinishes[step]
			if pod != nil {
				finished = completePodFinishedAt(pod)
			}
			if finished == "" {
				f.t.Error("terminal KFP node lost its original Pod exit timestamp", step)
				return nil
			}
			node["finishedAt"] = finished
		}
		nodes[nodeID] = node
		tasks = append(tasks, map[string]any{"run_id": completeRunID, "task_id": step + "-task", "display_name": step, "state": state, "child_tasks": []any{map[string]any{"pod_name": nodeID}}})
	}
	if os.Getenv("ANI_MODELDEV_WEBHOOK_MAINFLOW") != "" {
		phase, finished := "Running", f.archivedStepFinishes["driver"]
		if pod := f.objects["/api/v1/namespaces/"+s.Environment.NamespaceName+"/pods/main-driver"]; pod != nil {
			phase = pod["status"].(map[string]any)["phase"].(string)
			finished = completePodFinishedAt(pod)
		} else if state := f.archivedStepStates["driver"]; state != "" {
			phase = map[string]string{"SUCCEEDED": "Succeeded", "FAILED": "Failed"}[state]
		}
		node := map[string]any{"id": "main-driver", "name": "main-flow.driver", "displayName": "driver", "type": "Pod", "phase": phase}
		if finished != "" {
			node["finishedAt"] = finished
		}
		nodes["main-driver"] = node
	}
	workflow := f.objects["/apis/argoproj.io/v1alpha1/namespaces/"+s.Environment.NamespaceName+"/workflows/main-flow"]
	if workflow["status"] == nil {
		workflow["status"] = map[string]any{"phase": "Running"}
	}
	workflow["status"].(map[string]any)["nodes"] = nodes
	f.runTasks = tasks
	return tasks
}

// This is the explicit external KFP/Argo boundary. It acknowledges only Pods
// whose real component work has already returned and been marked Succeeded.
func (f *completeFixture) finishWorkflowNaturally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, step := range []string{"prepare", "train-wait", "collect", "publish", "close"} {
		pod := f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-"+step]
		if pod["status"].(map[string]any)["phase"] != "Succeeded" {
			f.t.Fatal("external Workflow completion preceded actual component exit", step)
		}
		f.completeWaitExitLocked(pod)
	}
	f.runSucceededAt = time.Now().UTC().Format(time.RFC3339Nano)
	if os.Getenv("ANI_MODELDEV_WEBHOOK_MAINFLOW") != "" {
		pod := f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-driver"]
		pod["status"] = map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "main", "restartCount": 0, "state": map[string]any{"terminated": map[string]any{"exitCode": 0, "finishedAt": f.runSucceededAt}}}}}
		f.completeWaitExitLocked(pod)
	}
	if f.currentRunTasksLocked() == nil {
		f.t.Fatal("external Workflow completion lost its native task graph")
	}
	workflow := f.objects["/apis/argoproj.io/v1alpha1/namespaces/"+f.workspace.NamespaceName+"/workflows/main-flow"]
	labels, _ := workflow["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels["workflows.argoproj.io/completed"] = "true"
	workflow["metadata"].(map[string]any)["labels"] = labels
	workflow["metadata"].(map[string]any)["resourceVersion"] = "2"
	status := workflow["status"].(map[string]any)
	status["phase"], status["finishedAt"] = "Succeeded", f.runSucceededAt
	status["conditions"] = []any{map[string]any{"type": "Completed", "status": "True"}}
}

func completePodFinishedAt(pod map[string]any) string {
	status, _ := pod["status"].(map[string]any)
	containers, _ := status["containerStatuses"].([]any)
	for _, raw := range containers {
		container, _ := raw.(map[string]any)
		if container["name"] != "main" {
			continue
		}
		state, _ := container["state"].(map[string]any)
		terminated, _ := state["terminated"].(map[string]any)
		finished, _ := terminated["finishedAt"].(string)
		return finished
	}
	return ""
}

func (f *completeFixture) storageRequest(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") && !validFixtureDownload(r) {
		w.WriteHeader(403)
		return
	}
	key := r.URL.Path
	if r.Method == "PUT" {
		if r.Header.Get("If-None-Match") != "*" {
			f.t.Error("upload must not overwrite")
			w.WriteHeader(400)
			return
		}
		if f.blobs[key] != nil {
			w.WriteHeader(412)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		f.blobs[key] = body
		f.versions[key] = uuid.NewString()
		w.Header().Set("x-amz-version-id", f.versions[key])
		w.WriteHeader(200)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	body, found := f.blobs[key]
	if !found {
		w.WriteHeader(404)
		return
	}
	if version := r.URL.Query().Get("versionId"); version != "" && version != f.versions[key] {
		w.WriteHeader(404)
		return
	}
	w.Header().Set("x-amz-version-id", f.versions[key])
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Method == "GET" {
		_, _ = w.Write(body)
	}
}

// The external S3 substitute accepts credential-free GET only when the actual
// SDK signature covers this URL/version and remains within its sixty seconds.
func validFixtureDownload(r *http.Request) bool {
	q := r.URL.Query()
	signedAt, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
	if err != nil || r.Method != http.MethodGet || q.Get("X-Amz-Expires") != "60" || q.Get("versionId") == "" || time.Now().Before(signedAt) || !time.Now().Before(signedAt.Add(time.Minute)) {
		return false
	}
	want := q.Get("X-Amz-Signature")
	for key := range q {
		if strings.HasPrefix(key, "X-Amz-") && key != "X-Amz-Expires" {
			q.Del(key)
		}
	}
	copy := r.Clone(context.Background())
	u := *r.URL
	u.Scheme = "https"
	u.Host = r.Host
	u.RawQuery = q.Encode()
	copy.URL = &u
	copy.Header = make(http.Header)
	signed, _, err := v4.NewSigner().PresignHTTP(r.Context(), aws.Credentials{AccessKeyID: "synthetic-key", SecretAccessKey: "synthetic-secret"}, copy, "UNSIGNED-PAYLOAD", "s3", "us-east-1", signedAt, func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	if err != nil {
		return false
	}
	got, err := url.Parse(signed)
	return err == nil && want != "" && hmac.Equal([]byte(got.Query().Get("X-Amz-Signature")), []byte(want))
}

func (f *completeFixture) finishPublisher() {
	f.mu.Lock()
	defer f.mu.Unlock()
	pod := f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-publish"]
	pod["status"] = map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "main", "restartCount": 0, "state": map[string]any{"terminated": map[string]any{"exitCode": 0, "finishedAt": time.Now().UTC().Format(time.RFC3339Nano)}}}}}
	f.completeWaitExitLocked(pod)
	if f.currentRunTasksLocked() == nil {
		f.t.Fatal("external uploader completion lost its native task graph")
	}
}
