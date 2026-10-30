package biz

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

const RuntimeGeneratorVersion = "ani.modeldev.runtime.v1"
const RuntimeDefaultingRules = "trainer-2.1.0-jobset-explicit-v1"

// ExpectedWorkspacePVCName is also used by the KFP workspace-name component.
// The PVC's future UID is bound only after actual creation and preparation.
func ExpectedWorkspacePVCName(executionID string) string {
	return "ani-kfp-workspace-" + strings.ToLower(executionID)
}

// CompileRuntimePlan is pure. It freezes the complete training template before
// admission; the owner later adds execution annotations without changing spec.
// Its finite safety policy inherits the existing UID 10001 training image and
// original exit-evidence finalizer. No user supplied Kubernetes object enters.
func CompileRuntimePlan(snapshot cpup01.Snapshot) (cpup01.RuntimePlan, error) {
	r := snapshot.Resources
	if snapshot.Timing.Validate() != nil || !validAdmissionID(snapshot.IdentityRef.ExecutionID) ||
		!validAdmissionID(snapshot.Environment.NamespaceUID) || snapshot.Environment.NamespaceName == "" ||
		snapshot.Environment.Identities.TrainerServiceAccount == "" || r.Nodes != 1 || r.ProcessesPerNode != 1 ||
		r.RequestMillicpu <= 0 || r.RequestMillicpu != r.LimitMillicpu || r.RequestMemoryBytes <= 0 || r.RequestMemoryBytes != r.LimitMemoryBytes ||
		!strings.Contains(snapshot.Program.ImageDigest, "@sha256:") || len(snapshot.Program.Command) == 0 {
		return cpup01.RuntimePlan{}, ErrInvalidAdmission
	}
	resources, err := TrainingResourceVector(snapshot)
	if err != nil {
		return cpup01.RuntimePlan{}, err
	}
	pvc := ExpectedWorkspacePVCName(snapshot.IdentityRef.ExecutionID)
	volume := func(name string) any {
		return map[string]any{"name": name, "persistentVolumeClaim": map[string]any{"claimName": pvc}}
	}
	mount := func(name, destination, subpath string, readOnly bool) any {
		return map[string]any{"name": name, "mountPath": destination, "subPath": subpath, "readOnly": readOnly}
	}
	volumes := []any{volume("input"), volume("output"), volume("work")}
	mounts := []any{
		mount("input", "/inputs", snapshot.Workspace.InputSubpath, true),
		mount("output", "/outputs", snapshot.Workspace.TrainingSubpath, false),
		mount("work", "/workspace/work/train", snapshot.Workspace.WorkSubpath+"/train", false),
	}
	if snapshot.Kind == "FINETUNING" {
		volumes = []any{volume("ft-input"), volume("ft-adapter"), volume("ft-report"), volume("work")}
		mounts = []any{
			mount("ft-input", "/workspace/inputs", snapshot.Workspace.InputSubpath, true),
			mount("ft-adapter", "/workspace/training/adapter", snapshot.Workspace.TrainingSubpath+"/adapter", false),
			mount("ft-report", "/workspace/reports/train", snapshot.Workspace.ReportsSubpath+"/train", false),
			mount("work", "/workspace/work/train", snapshot.Workspace.WorkSubpath+"/train", false),
		}
	} else if snapshot.Kind != "GENERAL_TRAINING" {
		return cpup01.RuntimePlan{}, ErrInvalidAdmission
	}
	pod := map[string]any{
		"serviceAccountName":           snapshot.Environment.Identities.TrainerServiceAccount,
		"automountServiceAccountToken": false, "restartPolicy": "Never", "schedulerName": "volcano",
		"terminationGracePeriodSeconds": int64(5),
		"securityContext":               map[string]any{"runAsNonRoot": true, "runAsUser": int64(10001), "runAsGroup": int64(10001), "fsGroup": int64(10001), "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"volumes":                       volumes,
		"containers": []any{map[string]any{
			"name": "node", "image": snapshot.Program.ImageDigest, "imagePullPolicy": "IfNotPresent",
			"command": snapshot.Program.Command, "args": snapshot.Program.ResolvedArgs,
			"resources":       map[string]any{"requests": resources, "limits": resources},
			"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
			"volumeMounts":    mounts,
			"env": []any{
				map[string]any{"name": "TMPDIR", "value": "/workspace/work/train/tmp"},
				map[string]any{"name": "HF_HOME", "value": "/workspace/work/train/hf"},
				map[string]any{"name": "HF_DATASETS_CACHE", "value": "/workspace/work/train/hf/datasets"},
				map[string]any{"name": "MPLCONFIGDIR", "value": "/workspace/work/train/matplotlib"},
			},
		}},
	}
	queue := "default"
	podMetadata := map[string]any{"finalizers": []string{"modeldev.ani.io/training-exit-evidence"}}
	if snapshot.TrainingImage != nil {
		// The general-training Pod must reference the shared tenant pull Secret
		// by name. The name is derived offline from the same rule Resource uses;
		// the Secret material itself never enters this plan. The finetuning
		// chain keeps its own unchanged template.
		pod["imagePullSecrets"] = []any{map[string]any{"name": snapshot.TrainingImage.PullSecretRef}}
	}
	if snapshot.GPU != nil {
		gpu := snapshot.GPU.Execution
		queue = gpu.QueueName
		pod["schedulerName"], pod["runtimeClassName"], pod["nodeSelector"] = gpu.SchedulerName, gpu.RuntimeClassName, gpu.NodeSelector
		annotations := map[string]string{"scheduling.volcano.sh/queue-name": queue}
		for key, value := range gpu.PodAnnotations {
			annotations[key] = value
		}
		podMetadata["annotations"] = annotations
	}
	spec := map[string]any{
		"mlPolicy": map[string]any{"numNodes": int64(1)}, "podGroupPolicy": map[string]any{"volcano": map[string]any{}},
		"template": map[string]any{
			"metadata": map[string]any{"annotations": map[string]any{"scheduling.volcano.sh/queue-name": queue}},
			"spec": map[string]any{
				"failurePolicy": map[string]any{"maxRestarts": int64(0), "restartStrategy": "Recreate"},
				"replicatedJobs": []any{map[string]any{
					"name": "node", "groupName": "default", "replicas": int64(1),
					"template": map[string]any{
						"metadata": map[string]any{"labels": map[string]any{"trainer.kubeflow.org/trainjob-ancestor-step": "trainer"}},
						"spec": map[string]any{
							"activeDeadlineSeconds": snapshot.Timing.StepTimeoutSeconds,
							"parallelism":           int64(1), "completions": int64(1), "backoffLimit": int64(0), "completionMode": "Indexed",
							"template": map[string]any{"metadata": podMetadata, "spec": pod},
						},
					},
				}},
			},
		},
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(spec) != nil {
		return cpup01.RuntimePlan{}, ErrInvalidAdmission
	}
	raw := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	digest := sha256.Sum256(raw)
	plan := cpup01.RuntimePlan{GeneratorVersion: RuntimeGeneratorVersion, APIGroup: "trainer.kubeflow.org", Kind: "TrainingRuntime", Namespace: snapshot.Environment.NamespaceName,
		Name: cpup01.RuntimeName(snapshot.Environment.NamespaceUID, snapshot.IdentityRef.ExecutionID), ExpectedSpec: raw, ExpectedSpecSHA256: hex.EncodeToString(digest[:]), DefaultingRules: RuntimeDefaultingRules, TargetJobs: []string{"node"}}
	if plan.Validate() != nil {
		return cpup01.RuntimePlan{}, ErrInvalidAdmission
	}
	return plan, nil
}

// RuntimePlanSchedulesPodGroup validates the sole current Volcano policy.
// Missing or different policy is an invalid plan, never a retired-plan default.
func RuntimePlanSchedulesPodGroup(plan cpup01.RuntimePlan) (bool, error) {
	var spec map[string]any
	if len(plan.ExpectedSpec) == 0 || json.Unmarshal(plan.ExpectedSpec, &spec) != nil {
		return false, ErrInvalidAdmission
	}
	policy, found, err := unstructuredNestedMap(spec, "podGroupPolicy")
	if err != nil {
		return false, ErrInvalidAdmission
	}
	if !found {
		return false, ErrInvalidAdmission
	}
	if len(policy) != 1 {
		return false, ErrInvalidAdmission
	}
	volcano, found, err := unstructuredNestedMap(policy, "volcano")
	if err != nil || !found {
		return false, ErrInvalidAdmission
	}
	if len(volcano) != 0 {
		return false, ErrInvalidAdmission
	}
	return true, nil
}

func unstructuredNestedMap(object map[string]any, key string) (map[string]any, bool, error) {
	value, found := object[key]
	if !found {
		return nil, false, nil
	}
	nested, ok := value.(map[string]any)
	if !ok {
		return nil, false, ErrInvalidAdmission
	}
	return nested, true, nil
}

// TrainingResourceVector is the single TRAIN requests/limits source used by
// both the complete Runtime template and TrainJob resourcesPerNode.
func TrainingResourceVector(snapshot cpup01.Snapshot) (map[string]string, error) {
	r := snapshot.Resources
	if snapshot.ComputeOrigin.Validate() != nil || r.RequestMillicpu != snapshot.ComputeOrigin.CPUMillis || r.RequestMemoryBytes != snapshot.ComputeOrigin.MemoryMiB*1024*1024 || r.RequestMillicpu <= 0 || r.RequestMillicpu != r.LimitMillicpu || r.RequestMemoryBytes <= 0 || r.RequestMemoryBytes != r.LimitMemoryBytes {
		return nil, ErrInvalidAdmission
	}
	vector := map[string]string{"cpu": milliCPU(r.RequestMillicpu), "memory": binaryBytes(r.RequestMemoryBytes)}
	if snapshot.ComputeOrigin.DeviceMode == "cpu" {
		if snapshot.GPU != nil {
			return nil, ErrInvalidAdmission
		}
	} else if snapshot.ComputeOrigin.DeviceMode == "gpu" {
		if snapshot.GPU == nil || snapshot.GPU.ValidateFor(snapshot.ComputeOrigin) != nil {
			return nil, ErrInvalidAdmission
		}
		for key, value := range snapshot.GPU.Execution.Resources {
			vector[key] = value
		}
	} else {
		return nil, ErrInvalidAdmission
	}
	return vector, nil
}

func milliCPU(value int64) string {
	if value%1000 == 0 {
		return strconv.FormatInt(value/1000, 10)
	}
	return strconv.FormatInt(value, 10) + "m"
}

func binaryBytes(value int64) string {
	if value%(1<<30) == 0 {
		return strconv.FormatInt(value/(1<<30), 10) + "Gi"
	}
	if value%(1<<20) == 0 {
		return strconv.FormatInt(value/(1<<20), 10) + "Mi"
	}
	return strconv.FormatInt(value, 10)
}
