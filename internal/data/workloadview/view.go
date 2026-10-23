// Package workloadview projects whitelisted Kubernetes objects into the
// execution-detail view models. Both the Trainer and the KFP adapter use the
// same projection so a field cannot be exposed by one layer and hidden by the
// other. Only explicitly named fields are copied; a Pod spec, environment,
// mounts or any Secret-bearing detail never reaches a view.
package workloadview

import (
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Pod projects one Pod. Availability is decided by the caller because only the
// caller knows whether the owning controller still exists.
func Pod(pod *unstructured.Unstructured, layer biz.PodLayer, availability biz.PodAvailability) biz.ExecutionPod {
	view := biz.ExecutionPod{Name: pod.GetName(), UID: string(pod.GetUID()), Layer: layer,
		Availability: availability, APIPresent: true}
	view.Phase, _, _ = unstructured.NestedString(pod.Object, "status", "phase")
	view.IP, _, _ = unstructured.NestedString(pod.Object, "status", "podIP")
	view.NodeName, _, _ = unstructured.NestedString(pod.Object, "spec", "nodeName")
	if created := pod.GetCreationTimestamp(); !created.IsZero() {
		at := created.UTC()
		view.CreatedAt = &at
	}
	if deleted := pod.GetDeletionTimestamp(); deleted != nil && !deleted.IsZero() {
		at := deleted.UTC()
		view.DeletedAt = &at
	}
	if started, _, err := unstructured.NestedString(pod.Object, "status", "startTime"); err == nil && started != "" {
		if at, ok := ParseTime(started); ok {
			utc := at.UTC()
			view.StartTime = &utc
		}
	}
	view.Ready = ConditionTrue(pod, "Ready")
	view.Containers, view.Restarts = Containers(pod, "containers", "containerStatuses", biz.ContainerTypeContainer)
	view.InitContainers, _ = Containers(pod, "initContainers", "initContainerStatuses", biz.ContainerTypeInit)
	view.EphemeralContainers, _ = Containers(pod, "ephemeralContainers", "ephemeralContainerStatuses", biz.ContainerTypeEphemeral)
	for _, container := range view.Containers {
		if container.Name == "node" {
			view.ExitCode = container.ExitCode
		}
	}
	return view
}

func ConditionTrue(pod *unstructured.Unstructured, conditionType string) bool {
	conditions, _, err := unstructured.NestedSlice(pod.Object, "status", "conditions")
	if err != nil {
		return false
	}
	for _, value := range conditions {
		condition, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if condition["type"] == conditionType {
			return condition["status"] == "True"
		}
	}
	return false
}

// Containers keeps the declared order and reports exited codes only where the
// container actually terminated. A declared-but-not-yet-created container is
// unknown rather than assumed waiting.
func Containers(pod *unstructured.Unstructured, specField, statusField string, kind biz.ContainerType) ([]biz.PodContainer, int32) {
	declared, _, err := unstructured.NestedSlice(pod.Object, "spec", specField)
	if err != nil {
		return nil, 0
	}
	statuses, _, err := unstructured.NestedSlice(pod.Object, "status", statusField)
	if err != nil {
		return nil, 0
	}
	byName := make(map[string]map[string]any, len(statuses))
	for _, value := range statuses {
		status, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name, _ := status["name"].(string)
		byName[name] = status
	}
	result := make([]biz.PodContainer, 0, len(declared))
	var restarts int32
	for _, value := range declared {
		container, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name, _ := container["name"].(string)
		if name == "" {
			continue
		}
		view := biz.PodContainer{Name: name, Type: kind, State: biz.ContainerStateUnknown}
		status := byName[name]
		if status == nil {
			result = append(result, view)
			continue
		}
		if count, ok := status["restartCount"].(int64); ok && count >= 0 && count <= 1<<31-1 {
			view.RestartCount = int32(count)
			restarts += view.RestartCount
		}
		fillState(&view, status["state"])
		if last, present, _ := unstructured.NestedMap(status, "lastState"); present && len(last) != 0 {
			previous := biz.PodContainer{Name: name, Type: kind, State: biz.ContainerStateUnknown}
			fillState(&previous, last)
			view.LastState = &previous
		}
		result = append(result, view)
	}
	return result, restarts
}

func fillState(view *biz.PodContainer, value any) {
	state, ok := value.(map[string]any)
	if !ok || len(state) != 1 {
		return
	}
	for key, payload := range state {
		fields, ok := payload.(map[string]any)
		if !ok {
			return
		}
		switch key {
		case "running":
			view.State = biz.ContainerStateRunning
			if at, ok := ParseTime(fields["startedAt"]); ok {
				view.StartedAt = &at
			}
		case "waiting":
			view.State = biz.ContainerStateWaiting
			view.Reason, _ = fields["reason"].(string)
			view.Message, _ = fields["message"].(string)
		case "terminated":
			view.State = biz.ContainerStateTerminated
			view.Reason, _ = fields["reason"].(string)
			view.Message, _ = fields["message"].(string)
			if code, ok := fields["exitCode"].(int64); ok && code >= -1<<31 && code <= 1<<31-1 {
				exit := int32(code)
				view.ExitCode = &exit
			}
			if at, ok := ParseTime(fields["startedAt"]); ok {
				view.StartedAt = &at
			}
			if at, ok := ParseTime(fields["finishedAt"]); ok {
				view.FinishedAt = &at
			}
		}
	}
}

// Event projects one Kubernetes Event. involvedObject identity is supplied by
// the caller after its own UID authorization, never taken from the message.
func Event(event *unstructured.Unstructured) (biz.ExecutionEvent, bool) {
	view := biz.ExecutionEvent{}
	view.Type, _, _ = unstructured.NestedString(event.Object, "type")
	view.Reason, _, _ = unstructured.NestedString(event.Object, "reason")
	view.Message, _, _ = unstructured.NestedString(event.Object, "message")
	view.FieldPath, _, _ = unstructured.NestedString(event.Object, "involvedObject", "fieldPath")
	view.ReportingInstance, _, _ = unstructured.NestedString(event.Object, "reportingInstance")
	view.Source, _, _ = unstructured.NestedString(event.Object, "source", "component")
	if view.Source == "" {
		view.Source, _, _ = unstructured.NestedString(event.Object, "reportingComponent")
	}
	view.FirstTimestamp, _ = eventTime(event, "firstTimestamp", "eventTime")
	view.LastTimestamp, _ = eventTime(event, "lastTimestamp", "eventTime")
	if view.LastTimestamp.IsZero() {
		view.LastTimestamp = view.FirstTimestamp
	}
	if view.FirstTimestamp.IsZero() {
		view.FirstTimestamp = view.LastTimestamp
	}
	if count, found, err := unstructured.NestedInt64(event.Object, "count"); err == nil && found && count >= 0 && count <= 1<<31-1 {
		view.Count = int32(count)
	} else if view.Count == 0 {
		view.Count = 1
	}
	return view, !view.LastTimestamp.IsZero()
}

// EventInvolvedUID is the only authorization input of an Event.
func EventInvolvedUID(event *unstructured.Unstructured) string {
	value, _, _ := unstructured.NestedString(event.Object, "involvedObject", "uid")
	return value
}

func eventTime(event *unstructured.Unstructured, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if text, found, err := unstructured.NestedString(event.Object, key); err == nil && found && text != "" {
			if at, ok := ParseTime(text); ok {
				return at, true
			}
		}
	}
	return time.Time{}, false
}

func ParseTime(value any) (time.Time, bool) {
	text, ok := value.(string)
	if !ok || text == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return time.Time{}, false
	}
	return at.UTC(), true
}
