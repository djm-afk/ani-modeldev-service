package runtimeproof

import "testing"

func TestRecordedNativeMetadataExecutorsResolveActualTaskPods(t *testing.T) {
	workflow, raw, pods := recordedTasksFrom(t, "testdata/kfp-2.16-metadata-executors.json")
	tasks, err := resolvedManagedTasksForKind(workflow, raw, pods, "FINETUNING")
	if err != nil {
		t.Fatal(err)
	}
	for name, pod := range map[string]string{
		"workspace-name":  "ft-7jsv6-metadata-0-2-retry-system-container-impl-2759475230",
		"prepare":         "ft-7jsv6-metadata-0-2-retry-system-container-impl-2514105504",
		"close-finalizer": "ft-7jsv6-metadata-0-2-retry-system-container-impl-2929180653",
	} {
		task, err := oneTask(tasks, name)
		if err != nil || task.PodName != pod {
			t.Fatalf("original %s executor unresolved: %+v %v", name, task, err)
		}
	}
	train, err := oneTask(tasks, "train-wait")
	if err != nil || train.State != "SKIPPED" || train.PodName != "" {
		t.Fatalf("omitted training became a workload: %+v %v", train, err)
	}
}

func TestRecordedNativeMetadataExecutorsRejectUnprovenPodAssociations(t *testing.T) {
	for _, attack := range []string{"template", "owner", "node", "duplicate"} {
		t.Run(attack, func(t *testing.T) {
			workflow, raw, pods := recordedTasksFrom(t, "testdata/kfp-2.16-metadata-executors.json")
			if attack == "template" {
				nodes := workflow.Object["status"].(map[string]any)["nodes"].(map[string]any)
				nodes["ft-7jsv6-2514105504"].(map[string]any)["templateName"] = "metadata-untrusted-retry-system-container-impl"
			}
			for i := range pods {
				if pods[i].GetName() != "ft-7jsv6-metadata-0-2-retry-system-container-impl-2514105504" {
					continue
				}
				switch attack {
				case "owner":
					owners := pods[i].GetOwnerReferences()
					owners[0].UID = "another-workflow"
					pods[i].SetOwnerReferences(owners)
				case "node":
					annotations := pods[i].GetAnnotations()
					annotations["workflows.argoproj.io/node-id"] = "ft-7jsv6-2759475230"
					pods[i].SetAnnotations(annotations)
				case "duplicate":
					duplicate := pods[i].DeepCopy()
					duplicate.SetName("replacement-pod")
					duplicate.SetUID("replacement-uid")
					pods = append(pods, *duplicate)
				}
				break
			}
			if _, err := resolvedManagedTasksForKind(workflow, raw, pods, "FINETUNING"); err == nil {
				t.Fatal("unproven executor accepted")
			}
		})
	}
}
