package runtimeproof

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// This controller/KFP snapshot was captured from the first real 2.16 Run.
// Only the external record is a fixture; role and ownership decisions are real.
func recordedTasks(t *testing.T) (*unstructured.Unstructured, []kfp.ManagedTask, []unstructured.Unstructured) {
	return recordedTasksFrom(t, "testdata/kfp-2.16-failed-before-prepare.json")
}

func recordedTasksFrom(t *testing.T, path string) (*unstructured.Unstructured, []kfp.ManagedTask, []unstructured.Unstructured) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Workflow map[string]any
		Pods     []map[string]any
		Run      map[string]any
	}
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	details := record.Run["run_details"].(map[string]any)["task_details"].([]any)
	tasks := make([]kfp.ManagedTask, 0, len(details))
	for _, item := range details {
		m := item.(map[string]any)
		task := kfp.ManagedTask{RunID: m["run_id"].(string), ID: m["task_id"].(string), Name: m["display_name"].(string)}
		task.State, _ = m["state"].(string)
		refs, _ := m["child_tasks"].([]any)
		for _, item := range refs {
			m := item.(map[string]any)
			name, _ := m["pod_name"].(string)
			tasksID, _ := m["task_id"].(string)
			task.ChildTasks = append(task.ChildTasks, kfp.ManagedTaskReference{PodName: name, TaskID: tasksID})
		}
		tasks = append(tasks, task)
	}
	pods := make([]unstructured.Unstructured, len(record.Pods))
	for i, pod := range record.Pods {
		pods[i] = unstructured.Unstructured{Object: pod}
	}
	return &unstructured.Unstructured{Object: record.Workflow}, tasks, pods
}

func TestRecordedKFPNodeReferencesResolveActualMainPodAndSkippedStages(t *testing.T) {
	workflow, raw, pods := recordedTasks(t)
	tasks, err := resolvedManagedTasks(workflow, raw, pods)
	if err != nil {
		t.Fatal(err)
	}
	task, err := oneTask(tasks, "workspace-name")
	if err != nil || task.PodName != "general-cpu-rnggt-retry-system-container-impl-1467617326" || task.State != "SUCCEEDED" {
		t.Fatalf("actual executor Pod unresolved: %+v %v", task, err)
	}
	for _, name := range []string{"prepare", "train-wait", "collect", "publish", "close"} {
		task, err := oneTask(tasks, name)
		if err != nil || task.PodName != "" || task.State != "SKIPPED" {
			t.Fatalf("controller-omitted %s not proven: %+v %v", name, task, err)
		}
	}
}

func TestRecordedKFPMappingRejectsOtherWorkflowAndContradictoryState(t *testing.T) {
	for _, attack := range []string{"owner", "node", "state", "duplicate"} {
		t.Run(attack, func(t *testing.T) {
			workflow, raw, pods := recordedTasks(t)
			for i := range pods {
				if pods[i].GetName() != "general-cpu-rnggt-retry-system-container-impl-1467617326" {
					continue
				}
				switch attack {
				case "owner":
					refs := pods[i].GetOwnerReferences()
					refs[0].UID = "unrelated-workflow"
					pods[i].SetOwnerReferences(refs)
				case "node":
					annotations := pods[i].GetAnnotations()
					annotations["workflows.argoproj.io/node-id"] = "general-cpu-rnggt-3234889654"
					pods[i].SetAnnotations(annotations)
				case "duplicate":
					duplicate := pods[i].DeepCopy()
					duplicate.SetName("forged-pod")
					duplicate.SetUID("forged-uid")
					pods = append(pods, *duplicate)
				}
				break
			}
			if attack == "state" {
				for i := range raw {
					if raw[i].Name == "prepare" {
						raw[i].State = "RUNNING"
					}
				}
			}
			if _, err := resolvedManagedTasks(workflow, raw, pods); err == nil {
				t.Fatal("unproven task/Pod association accepted")
			}
		})
	}
}

func TestCurrentTaskResolutionRejectsDirectPodNameWithoutNativeGraph(t *testing.T) {
	tasks := []kfp.ManagedTask{{RunID: "original-run", ID: "old-task", Name: "prepare", PodName: "unproven-pod", State: "SUCCEEDED"}}
	if _, err := resolvedManagedTasks(nil, tasks, nil); err == nil {
		t.Fatal("accepted direct Pod name without native Workflow node and actual Pod UID")
	}
}
