package runtimeproof

import (
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Only VerifyOwnerWritersAbsent may consume this proof, after independently
// verifying the original confirmed Run and the completed full Workflow graph.
// Kubernetes v1.35.8 TerminatePod deliberately retains PodInitializing for the
// unstarted init suffix and regular containers. It does not supply a main exit.
func ownerInitializationAbort(pod *unstructured.Unstructured, now time.Time) *biz.PodInitializationAbort {
	return observeInitializationAbort(pod, now, true)
}

// After its independently verified abort was persisted, removing our own
// finalizer must not erase the proof. This branch is used only with that receipt.
func observeInitializationAbort(pod *unstructured.Unstructured, now time.Time, requireRetention bool) *biz.PodInitializationAbort {
	if pod == nil || pod.GetDeletionTimestamp() == nil || pod.GetResourceVersion() == "" || pod.GetGeneration() <= 0 {
		return nil
	}
	retained := false
	for _, finalizer := range pod.GetFinalizers() {
		retained = retained || finalizer == "modeldev.ani.io/step-exit-evidence"
	}
	phase, _ := textAt(pod, "status", "phase")
	policy, _ := textAt(pod, "spec", "restartPolicy")
	node, _ := textAt(pod, "spec", "nodeName")
	if requireRetention && !retained || phase != "Failed" || policy != "Never" || node == "" {
		return nil
	}
	for _, fields := range [][]string{{"spec", "ephemeralContainers"}, {"status", "ephemeralContainerStatuses"}} {
		items, _, err := unstructured.NestedSlice(pod.Object, fields...)
		if err != nil || len(items) != 0 {
			return nil
		}
	}
	proof := &biz.PodInitializationAbort{PodResourceVersion: pod.GetResourceVersion(), PodGeneration: pod.GetGeneration(), NodeName: node, Phase: phase, RestartPolicy: policy, DeletedAt: pod.GetDeletionTimestamp().Time.UTC()}
	conditions, found, err := unstructured.NestedSlice(pod.Object, "status", "conditions")
	if err != nil || !found {
		return nil
	}
	seenConditions := make(map[string]bool)
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		kind, ok := condition["type"].(string)
		if !ok || seenConditions[kind] {
			return nil
		}
		seenConditions[kind] = true
		if kind == "DisruptionTarget" && condition["status"] == "True" {
			return nil
		}
		if kind != "Initialized" && kind != "PodReadyToStartContainers" {
			continue
		}
		generation, found, err := unstructured.NestedInt64(condition, "observedGeneration")
		if err != nil || !found || generation != pod.GetGeneration() || condition["status"] != "False" {
			return nil
		}
		text, _, _ := unstructured.NestedString(condition, "lastTransitionTime")
		at, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || at.IsZero() || at.After(now) {
			return nil
		}
		if kind == "Initialized" {
			proof.NotInitializedAt = at.UTC()
		} else {
			proof.SandboxStoppedAt = at.UTC()
		}
	}
	seenNames := make(map[string]bool)
	for _, group := range []struct{ spec, status string }{{"initContainers", "initContainerStatuses"}, {"containers", "containerStatuses"}} {
		declared, found, err := unstructured.NestedSlice(pod.Object, "spec", group.spec)
		if err != nil || !found || len(declared) == 0 {
			return nil
		}
		statuses, found, err := unstructured.NestedSlice(pod.Object, "status", group.status)
		if err != nil || !found || len(statuses) != len(declared) {
			return nil
		}
		byName := make(map[string]map[string]any)
		for _, raw := range statuses {
			status, ok := raw.(map[string]any)
			if !ok {
				return nil
			}
			name, ok := status["name"].(string)
			if !ok || name == "" || byName[name] != nil {
				return nil
			}
			byName[name] = status
		}
		waitingSuffix := false
		for _, raw := range declared {
			container, ok := raw.(map[string]any)
			if !ok {
				return nil
			}
			name, ok := container["name"].(string)
			status := byName[name]
			if !ok || name == "" || seenNames[name] || status == nil {
				return nil
			}
			seenNames[name] = true
			// Restartable init containers and per-container restart rules need
			// their own actual exit proof; they never use this branch.
			for _, field := range []string{"restartPolicy", "restartPolicyRules"} {
				if _, present := container[field]; present {
					return nil
				}
			}
			if group.spec == "initContainers" {
				proof.DeclaredInitContainers = append(proof.DeclaredInitContainers, name)
			} else {
				proof.DeclaredContainers = append(proof.DeclaredContainers, name)
			}
			restarts, found, err := unstructured.NestedInt64(status, "restartCount")
			if err != nil || !found || restarts != 0 {
				return nil
			}
			started, found, err := unstructured.NestedBool(status, "started")
			if err != nil || !found || started {
				return nil
			}
			last, found, err := unstructured.NestedMap(status, "lastState")
			if err != nil || !found || len(last) != 0 {
				return nil
			}
			state, found, err := unstructured.NestedMap(status, "state")
			if err != nil || !found || len(state) != 1 {
				return nil
			}
			if terminated, ok := state["terminated"].(map[string]any); ok {
				if group.spec != "initContainers" || waitingSuffix {
					return nil
				}
				code, found, err := unstructured.NestedInt64(terminated, "exitCode")
				if err != nil || !found || code != 0 {
					return nil
				}
				start, _, _ := unstructured.NestedString(terminated, "startedAt")
				finish, _, _ := unstructured.NestedString(terminated, "finishedAt")
				startedAt, startErr := time.Parse(time.RFC3339Nano, start)
				finishedAt, finishErr := time.Parse(time.RFC3339Nano, finish)
				cid, _, _ := unstructured.NestedString(terminated, "containerID")
				currentCID, _, _ := unstructured.NestedString(status, "containerID")
				image, _, _ := unstructured.NestedString(status, "imageID")
				if startErr != nil || finishErr != nil || cid == "" || cid != currentCID || image == "" {
					return nil
				}
				proof.InitContainerExits = append(proof.InitContainerExits, biz.InitializationContainerExit{Name: name, ContainerID: cid, ImageID: image, ExitCode: int32(code), StartedAt: startedAt.UTC(), FinishedAt: finishedAt.UTC()})
				continue
			}
			waiting, ok := state["waiting"].(map[string]any)
			if !ok || waiting["reason"] != "PodInitializing" {
				return nil
			}
			ready, found, err := unstructured.NestedBool(status, "ready")
			if err != nil || !found || ready {
				return nil
			}
			for _, field := range []string{"containerID", "imageID"} {
				if value, present := status[field]; present && value != nil && value != "" {
					return nil
				}
			}
			if group.spec == "initContainers" {
				waitingSuffix = true
				proof.UnstartedInitContainers = append(proof.UnstartedInitContainers, name)
			} else {
				proof.UnstartedContainers = append(proof.UnstartedContainers, name)
			}
		}
	}
	if !proof.ValidAt(now) {
		return nil
	}
	return proof
}
