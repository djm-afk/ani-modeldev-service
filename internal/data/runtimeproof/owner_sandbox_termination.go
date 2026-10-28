package runtimeproof

import (
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// This is consumed only after the original confirmed Run/full Workflow graph
// has terminated, before any workspace exists. Unlike initialization abort,
// kubelet has completed init and then lost regular container exit status.
func observeSandboxTermination(pod *unstructured.Unstructured, now time.Time, requireRetention bool) *biz.PodSandboxTermination {
	if pod == nil || pod.GetResourceVersion() == "" || pod.GetGeneration() <= 0 {
		return nil
	}
	retained := false
	for _, finalizer := range pod.GetFinalizers() {
		retained = retained || finalizer == stepExitEvidenceFinalizer
	}
	if requireRetention && !retained {
		return nil
	}
	proof := &biz.PodSandboxTermination{PodResourceVersion: pod.GetResourceVersion(), PodGeneration: pod.GetGeneration(), StepRole: pod.GetLabels()["ani.modeldev/step-role"]}
	proof.NodeName, _ = textAt(pod, "spec", "nodeName")
	proof.RestartPolicy, _ = textAt(pod, "spec", "restartPolicy")
	proof.Phase, _ = textAt(pod, "status", "phase")
	proof.Reason, _ = textAt(pod, "status", "reason")
	var found bool
	var err error
	proof.StatusObservedGeneration, found, err = unstructured.NestedInt64(pod.Object, "status", "observedGeneration")
	if err != nil || !found {
		return nil
	}
	for _, path := range [][]string{{"spec", "ephemeralContainers"}, {"status", "ephemeralContainerStatuses"}} {
		items, _, err := unstructured.NestedSlice(pod.Object, path...)
		if err != nil || len(items) != 0 {
			return nil
		}
	}
	conditions, found, err := unstructured.NestedSlice(pod.Object, "status", "conditions")
	if err != nil || !found {
		return nil
	}
	seenConditions := map[string]bool{}
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		kind, ok := condition["type"].(string)
		if !ok || seenConditions[kind] || kind == "DisruptionTarget" && condition["status"] == "True" {
			return nil
		}
		seenConditions[kind] = true
		if kind != "Initialized" && kind != "PodReadyToStartContainers" && kind != "Ready" && kind != "ContainersReady" {
			continue
		}
		state, stateOK := condition["status"].(string)
		generation, present, e := unstructured.NestedInt64(condition, "observedGeneration")
		text, _, _ := unstructured.NestedString(condition, "lastTransitionTime")
		at, timeErr := time.Parse(time.RFC3339Nano, text)
		if !stateOK || !present || e != nil || timeErr != nil {
			return nil
		}
		proof.Conditions = append(proof.Conditions, biz.SandboxTerminationCondition{Type: kind, Status: state, ObservedGeneration: generation, TransitionAt: at.UTC()})
	}
	seenNames := map[string]bool{}
	for _, group := range []struct{ spec, status string }{{"initContainers", "initContainerStatuses"}, {"containers", "containerStatuses"}} {
		declared, found, err := unstructured.NestedSlice(pod.Object, "spec", group.spec)
		statuses, statusFound, statusErr := unstructured.NestedSlice(pod.Object, "status", group.status)
		if err != nil || !found || len(declared) == 0 || statusErr != nil || !statusFound || len(statuses) != len(declared) {
			return nil
		}
		byName := map[string]map[string]any{}
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
			for _, field := range []string{"restartPolicy", "restartPolicyRules"} {
				if _, present := container[field]; present {
					return nil
				}
			}
			state, found, err := unstructured.NestedMap(status, "state")
			terminated, ok := state["terminated"].(map[string]any)
			if err != nil || !found || len(state) != 1 || !ok {
				return nil
			}
			restarts, found, err := unstructured.NestedInt64(status, "restartCount")
			started, startFound, startErr := unstructured.NestedBool(status, "started")
			ready, readyFound, readyErr := unstructured.NestedBool(status, "ready")
			last, lastFound, lastErr := unstructured.NestedMap(status, "lastState")
			cid, _, cidErr := unstructured.NestedString(status, "containerID")
			image, imageFound, imageErr := unstructured.NestedString(status, "imageID")
			if err != nil || !found || startErr != nil || !startFound || started || readyErr != nil || !readyFound || lastErr != nil || !lastFound || cidErr != nil || imageErr != nil || !imageFound {
				return nil
			}
			if group.spec == "initContainers" {
				code, codeFound, codeErr := unstructured.NestedInt64(terminated, "exitCode")
				start, _, _ := unstructured.NestedString(terminated, "startedAt")
				finish, _, _ := unstructured.NestedString(terminated, "finishedAt")
				startAt, startErr := time.Parse(time.RFC3339Nano, start)
				finishAt, finishErr := time.Parse(time.RFC3339Nano, finish)
				actualCID, _, _ := unstructured.NestedString(terminated, "containerID")
				if restarts != 0 || !ready || len(last) != 0 || codeErr != nil || !codeFound || code != 0 || startErr != nil || finishErr != nil || cid == "" || actualCID != cid || image == "" {
					return nil
				}
				proof.DeclaredInitContainers = append(proof.DeclaredInitContainers, name)
				proof.InitContainerExits = append(proof.InitContainerExits, biz.InitializationContainerExit{Name: name, ContainerID: cid, ImageID: image, ExitCode: 0, StartedAt: startAt.UTC(), FinishedAt: finishAt.UTC()})
				continue
			}
			marker := sandboxMissingMarker(terminated, biz.SandboxMissingMessage)
			if marker == nil {
				return nil
			}
			missing := biz.SandboxMissingContainer{Name: name, ContainerID: cid, ImageID: image, RestartCount: restarts, Started: started, Ready: ready, Current: *marker}
			if len(last) != 0 {
				previous, ok := last["terminated"].(map[string]any)
				if len(last) != 1 || !ok {
					return nil
				}
				missing.Previous = sandboxMissingMarker(previous, biz.SandboxPreviousMissingMessage)
				if missing.Previous == nil {
					return nil
				}
			}
			proof.DeclaredContainers = append(proof.DeclaredContainers, name)
			proof.MissingContainers = append(proof.MissingContainers, missing)
		}
	}
	if !proof.ValidAt(now) {
		return nil
	}
	return proof
}

func sandboxMissingMarker(state map[string]any, message string) *biz.SandboxMissingMarker {
	code, found, err := unstructured.NestedInt64(state, "exitCode")
	if len(state) != 5 || err != nil || !found || code != 137 || state["reason"] != "ContainerStatusUnknown" || state["message"] != message {
		return nil
	}
	for _, field := range []string{"startedAt", "finishedAt"} {
		if value, present := state[field]; !present || value != nil {
			return nil
		}
	}
	return &biz.SandboxMissingMarker{Reason: "ContainerStatusUnknown", Message: message, ReportedExitCode: 137}
}
