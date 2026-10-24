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
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
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
	trainingDone         chan struct{}
	trainingErr          error
	trainingLog          []byte
	workspace            biz.WorkspaceBinding
	stepDeletes          int
	archivedStepStates   map[string]string
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
	// Freeze the actual locally qualified image manifest used by Podman. The
	// external TrainJob substitute may not replace it with a synthetic digest.
	snapshot.Program.ImageDigest = f.image
	snapshot.Program.Command = []string{"/opt/venv/bin/python", "-I", "/opt/cpu03/train_mlp.py"}
	snapshot.Input.Object.SizeBytes = int64(len(data))
	snapshot.Input.Object.SHA256 = completeHash(data)
	snapshot.Program.ResolvedArgs = []string{"--data", "/inputs/data.csv", "--output", "/outputs", "--expected-input-sha256", snapshot.Input.Object.SHA256, "--expected-input-bytes", fmt.Sprint(len(data)), "--learning-rate", "0.01"}
	snapshot.Workspace.Mode = "EXECUTION_PVC"
	snapshot.RuntimePlan.Name = "cpu-runtime-v1"
	snapshot.RuntimePlan.TargetJobs = []string{"trainer"}
	f.workspace = biz.WorkspaceBinding{Mode: snapshot.Workspace.Mode, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUID: snapshot.Environment.NamespaceUID, PVCName: "md-" + f.request.Admission.ExecutionID, PVCUID: completePVCUID, InputSubpath: snapshot.Workspace.InputSubpath, TrainingSubpath: snapshot.Workspace.TrainingSubpath, ReportsSubpath: snapshot.Workspace.ReportsSubpath, PublicationSubpath: snapshot.Workspace.PublicationSubpath}
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
		m["ownerReferences"] = []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "name": "main-flow", "uid": completeWorkflowUID, "controller": true}}
		f.objects["/api/v1/namespaces/"+ns+"/pods/main-"+step] = map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": m, "spec": map[string]any{"serviceAccountName": snapshot.Environment.Identities.KFPStepServiceAccount, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": f.workspace.PVCName}}}, "containers": []any{map[string]any{"name": "main", "volumeMounts": []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}}}}}, "status": map[string]any{"phase": "Running"}}
		if step == "close" {
			f.objects["/api/v1/namespaces/"+ns+"/pods/main-"+step]["spec"] = map[string]any{"serviceAccountName": snapshot.Environment.Identities.KFPStepServiceAccount, "containers": []any{map[string]any{"name": "main"}}}
		}
	}
	var runtime map[string]any
	if err := json.Unmarshal([]byte(`{"apiVersion":"trainer.kubeflow.org/v1alpha1","kind":"ClusterTrainingRuntime","metadata":{"name":"cpu-runtime-v1","uid":"77777777-aaaa-4bbb-8ccc-111111111111"},"spec":{"mlPolicy":{"numNodes":1},"template":{"spec":{"failurePolicy":{"maxRestarts":0},"replicatedJobs":[{"name":"trainer","replicas":1,"template":{"metadata":{"labels":{"trainer.kubeflow.org/trainjob-ancestor-step":"trainer"}},"spec":{"parallelism":1,"completions":1,"backoffLimit":0,"template":{"spec":{"restartPolicy":"Never","automountServiceAccountToken":false,"securityContext":{"runAsNonRoot":true,"runAsUser":10001,"fsGroup":10001},"containers":[{"name":"node","image":"registry.example.test/placeholder:never-used","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}}}}]}}}}`), &runtime); err != nil {
		t.Fatal(err)
	}
	runtimeJobs := runtime["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["replicatedJobs"].([]any)
	runtimePod := runtimeJobs[0].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)
	runtimePod["metadata"] = map[string]any{"finalizers": []any{"modeldev.ani.io/training-exit-evidence"}}
	runtimeSpec, _ := json.Marshal(runtime["spec"])
	snapshot.RuntimePlan.ExpectedSpecSHA256 = completeHash(runtimeSpec)
	f.objects["/apis/trainer.kubeflow.org/v1alpha1/clustertrainingruntimes/cpu-runtime-v1"] = runtime
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	if r.Method != "GET" {
		f.t.Error("unexpected Kubernetes mutation")
		w.WriteHeader(405)
		return
	}
	if r.URL.Path == "/api/v1/namespaces/"+ns+"/pods/actual-training-pod/log" {
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
	if strings.HasSuffix(r.URL.Path, "/pods") || strings.HasSuffix(r.URL.Path, "/jobs") {
		items := []any{}
		for p, o := range f.objects {
			if strings.HasPrefix(p, r.URL.Path+"/") {
				f.completeWaitExitLocked(o)
				items = append(items, o)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "List", "metadata": map[string]any{}, "items": items})
		return
	}
	w.WriteHeader(404)
	_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404})
}

func (f *completeFixture) makeTrainingChildren(name string) {
	ns := f.workspace.NamespaceName
	object := func(api, kind, name, uid, ownerAPI, ownerKind, ownerName, ownerUID string) map[string]any {
		return map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns, "uid": uid, "generation": 1, "resourceVersion": "1", "ownerReferences": []any{map[string]any{"apiVersion": ownerAPI, "kind": ownerKind, "name": ownerName, "uid": ownerUID, "controller": true}}}, "status": map[string]any{"conditions": []any{}}}
	}
	set := object("jobset.x-k8s.io/v1alpha2", "JobSet", name, completeSetUID, "trainer.kubeflow.org/v1alpha1", "TrainJob", name, completeTrainUID)
	set["spec"] = map[string]any{"suspend": false}
	f.objects["/apis/jobset.x-k8s.io/v1alpha2/namespaces/"+ns+"/jobsets/"+name] = set
	job := object("batch/v1", "Job", name+"-trainer-0", completeJobUID, "jobset.x-k8s.io/v1alpha2", "JobSet", name, completeSetUID)
	job["spec"] = map[string]any{"parallelism": 1, "completions": 1, "backoffLimit": 0}
	job["status"].(map[string]any)["active"] = 1
	f.objects["/apis/batch/v1/namespaces/"+ns+"/jobs/"+name+"-trainer-0"] = job
	pod := object("v1", "Pod", "actual-training-pod", completePodUID, "batch/v1", "Job", name+"-trainer-0", completeJobUID)
	pod["spec"] = map[string]any{"serviceAccountName": f.request.Admission.Snapshot.Environment.Identities.TrainerServiceAccount, "restartPolicy": "Never", "automountServiceAccountToken": false, "containers": []any{map[string]any{"name": "node"}}}
	pod["status"] = map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "node", "restartCount": 0, "state": map[string]any{"running": map[string]any{"startedAt": time.Now().UTC().Format(time.RFC3339)}}}}}
	f.objects["/api/v1/namespaces/"+ns+"/pods/actual-training-pod"] = pod
}

func (f *completeFixture) runTraining(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := f.request.Admission.Snapshot.Program.Command
	args := []string{"run", "--rm", "--name", f.trainingContainer, "--pull=never", "--network", "none", "--http-proxy=false", "--cpus", "2", "--memory", "2g", "--memory-swap", "2g", "--pids-limit", "256", "--cap-drop", "all", "--security-opt", "no-new-privileges", "--read-only", "--userns", "keep-id:uid=10001,gid=10001", "--volume", filepath.Join(f.root, f.workspace.InputSubpath) + ":/inputs:ro,Z", "--volume", filepath.Join(f.root, f.workspace.TrainingSubpath) + ":/outputs:rw,Z", "--entrypoint", command[0], f.image}
	args = append(args, command[1:]...)
	args = append(args, f.request.Admission.Snapshot.Program.ResolvedArgs...)
	output, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trainingErr = err
	f.trainingLog = output
	ns := f.workspace.NamespaceName
	phase, condition, setCondition := "Succeeded", "Complete", "Completed"
	code := 0
	if err != nil {
		phase = "Failed"
		condition = "Failed"
		setCondition = "Failed"
		code = 1
	}
	status := func(typ string) map[string]any {
		return map[string]any{"active": 0, "conditions": []any{map[string]any{"type": typ, "status": "True", "observedGeneration": 1, "reason": "ActualProcessExited", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}}}
	}
	f.objects["/apis/trainer.kubeflow.org/v1alpha1/namespaces/"+ns+"/trainjobs/"+name]["status"] = status(condition)
	f.objects["/apis/jobset.x-k8s.io/v1alpha2/namespaces/"+ns+"/jobsets/"+name]["status"] = status(setCondition)
	f.objects["/apis/batch/v1/namespaces/"+ns+"/jobs/"+name+"-trainer-0"]["status"] = status(condition)
	f.objects["/api/v1/namespaces/"+ns+"/pods/actual-training-pod"]["status"] = map[string]any{"phase": phase, "containerStatuses": []any{map[string]any{"name": "node", "restartCount": 0, "state": map[string]any{"terminated": map[string]any{"exitCode": code, "reason": phase, "finishedAt": time.Now().UTC().Format(time.RFC3339)}}}}}
	close(f.trainingDone)
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
		return nil
	default:
	}
	if output, err := exec.CommandContext(ctx, "podman", "stop", "--time", "1", container).CombinedOutput(); err != nil {
		select {
		case <-f.trainingDone:
			return nil
		default:
			return fmt.Errorf("stop task-owned training container: %w: %s", err, output)
		}
	}
	select {
	case <-f.trainingDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
	} else if r.Method == "POST" && r.URL.Path == "/apis/v2beta1/runs/"+completeRunID+":terminate" {
		f.runStops++
		f.runStoppedAt = time.Now().UTC().Format(time.RFC3339Nano)
		// Only KFP's external step processes are simulated here. Prepare has
		// already returned from real component work; no training exit is set.
		nodes := map[string]any{}
		for _, step := range []string{"prepare", "train-wait", "collect", "publish", "close"} {
			pod := f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-"+step]
			if pod == nil {
				continue
			}
			phase := pod["status"].(map[string]any)["phase"].(string)
			if phase != "Succeeded" && phase != "Failed" {
				phase = "Failed"
				pod["status"] = map[string]any{"phase": phase, "containerStatuses": []any{map[string]any{"name": "main", "restartCount": 0, "state": map[string]any{"terminated": map[string]any{"exitCode": 143, "reason": "KFPStopped", "finishedAt": f.runStoppedAt}}}}}
			}
			nodes["main-"+step] = map[string]any{"id": "main-" + step, "name": "main-flow." + step, "type": "Pod", "phase": phase, "finishedAt": f.runStoppedAt}
		}
		workflow := f.objects["/apis/argoproj.io/v1alpha1/namespaces/"+f.workspace.NamespaceName+"/workflows/main-flow"]
		workflow["metadata"].(map[string]any)["labels"] = map[string]any{"workflows.argoproj.io/completed": "true"}
		workflow["status"] = map[string]any{"phase": "Failed", "finishedAt": f.runStoppedAt, "conditions": []any{map[string]any{"type": "Completed", "status": "True"}}, "nodes": nodes}
		_ = json.NewEncoder(w).Encode(map[string]any{})
		return
	} else if r.Method != "GET" || r.URL.Path != "/apis/v2beta1/runs/"+completeRunID {
		w.WriteHeader(400)
		return
	}
	s := f.request.Admission.Snapshot
	tasks := []any{}
	for _, step := range []string{"prepare", "train-wait", "collect", "publish", "close"} {
		state := "RUNNING"
		if f.skipped[step] {
			tasks = append(tasks, map[string]string{"run_id": completeRunID, "task_id": step + "-task", "display_name": step, "pod_name": "", "state": "SKIPPED"})
			continue
		}
		pod := f.objects["/api/v1/namespaces/"+s.Environment.NamespaceName+"/pods/main-"+step]
		if pod == nil {
			state = f.archivedStepStates[step]
			if state == "" {
				f.t.Error("KFP boundary lost task history independently of Pod cleanup")
				w.WriteHeader(500)
				return
			}
		} else if pod["status"].(map[string]any)["phase"] == "Succeeded" {
			state = "SUCCEEDED"
		} else if pod["status"].(map[string]any)["phase"] == "Failed" {
			state = "FAILED"
		}
		tasks = append(tasks, map[string]string{"run_id": completeRunID, "task_id": step + "-task", "display_name": step, "pod_name": "main-" + step, "state": state})
	}
	run := map[string]any{"run_id": completeRunID, "experiment_id": s.Environment.ExperimentID, "display_name": "md-" + f.request.Admission.ExecutionID, "pipeline_version_reference": map[string]string{"pipeline_id": s.Implementation.PipelineID, "pipeline_version_id": s.Implementation.PipelineVersionID}, "runtime_config": map[string]any{"parameters": map[string]string{"execution_id": f.request.Admission.ExecutionID, "spec_hash": f.request.Admission.SpecHash}, "pipeline_root": f.request.Owner.PipelineRoot}, "service_account": s.Environment.Identities.KFPStepServiceAccount, "state": "RUNNING", "run_details": map[string]any{"task_details": tasks}}
	if f.runStoppedAt != "" {
		run["state"], run["finished_at"] = "CANCELED", f.runStoppedAt
	} else if f.runSucceededAt != "" {
		run["state"], run["finished_at"] = "SUCCEEDED", f.runSucceededAt
	}
	_ = json.NewEncoder(w).Encode(run)
}

// This is the explicit external KFP/Argo boundary. It acknowledges only Pods
// whose real component work has already returned and been marked Succeeded.
func (f *completeFixture) finishWorkflowNaturally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	nodes := map[string]any{}
	for _, step := range []string{"prepare", "train-wait", "collect", "publish", "close"} {
		pod := f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-"+step]
		if pod["status"].(map[string]any)["phase"] != "Succeeded" {
			f.t.Fatal("external Workflow completion preceded actual component exit", step)
		}
		nodes["main-"+step] = map[string]any{"id": "main-" + step, "name": "main-flow." + step, "type": "Pod", "phase": "Succeeded", "finishedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	}
	f.runSucceededAt = time.Now().UTC().Format(time.RFC3339Nano)
	if os.Getenv("ANI_MODELDEV_WEBHOOK_MAINFLOW") != "" {
		pod := f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-driver"]
		pod["status"] = map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "main", "restartCount": 0, "state": map[string]any{"terminated": map[string]any{"exitCode": 0, "finishedAt": f.runSucceededAt}}}}}
		f.completeWaitExitLocked(pod)
		nodes["main-driver"] = map[string]any{"id": "main-driver", "name": "main-flow.driver", "type": "Pod", "phase": "Succeeded", "finishedAt": f.runSucceededAt}
	}
	workflow := f.objects["/apis/argoproj.io/v1alpha1/namespaces/"+f.workspace.NamespaceName+"/workflows/main-flow"]
	workflow["metadata"].(map[string]any)["labels"] = map[string]any{"workflows.argoproj.io/completed": "true"}
	workflow["metadata"].(map[string]any)["resourceVersion"] = "2"
	workflow["status"] = map[string]any{"phase": "Succeeded", "finishedAt": f.runSucceededAt, "conditions": []any{map[string]any{"type": "Completed", "status": "True"}}, "nodes": nodes}
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
	f.objects["/api/v1/namespaces/"+f.workspace.NamespaceName+"/pods/main-publish"]["status"] = map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "main", "restartCount": 0, "state": map[string]any{"terminated": map[string]any{"exitCode": 0, "finishedAt": time.Now().UTC().Format(time.RFC3339)}}}}}
}
