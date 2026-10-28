package kfp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"k8s.io/apimachinery/pkg/util/validation"
)

var ErrManagedRunUnverified = errors.New("KFP_MANAGED_RUN_UNVERIFIED")

// ManagedTaskReference refers to a dependent task or node. PodName is the
// backend's raw Argo node ID, not a verified Kubernetes Pod name. Exactly one
// of TaskID and PodName is set.
type ManagedTaskReference struct {
	TaskID  string
	PodName string
}

// ManagedTask is an observation from KFP's frozen Run, not a callback claim.
// PodName is populated only by native Workflow/Pod resolution; the raw KFP
// top-level legacy field is never accepted as an identity.
type ManagedTask struct {
	RunID, ID, Name, PodName, State string
	ChildTasks                      []ManagedTaskReference
}

// ManagedRun is one authenticated observation of the frozen KFP Run. Terminal
// state is not evidence that its Kubernetes writers have stopped.
type ManagedRun struct {
	State      string
	FinishedAt time.Time
	Tasks      []ManagedTask
}

// GetManagedTasks always reads the actual authenticated GetRun endpoint and
// verifies the complete frozen execution/spec/version/root/SA association.
func (client *Client) GetManagedTasks(ctx context.Context, plan biz.PipelineDispatchPlan, runID string) ([]ManagedTask, error) {
	run, err := client.GetManagedRun(ctx, plan, runID)
	if err != nil {
		return nil, err
	}
	return run.Tasks, nil
}

// GetManagedRun shares the exact frozen binding and bounded response checks
// with normal managed-step verification; owner close does not use a weaker API.
func (client *Client) GetManagedRun(ctx context.Context, plan biz.PipelineDispatchPlan, runID string) (ManagedRun, error) {
	id, err := uuid.Parse(runID)
	if client == nil || client.http == nil || client.tokens == nil || ctx == nil || ctx.Err() != nil ||
		err != nil || id == uuid.Nil || id.String() != runID || plan.Environment.KFPConnectionRef != client.connectionRef {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	token, err := client.tokens.BearerToken(ctx, plan.TenantID, plan.Environment)
	if err != nil || len(token) > 16384 || !bearerTokenPattern.MatchString(token) {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint+"/"+id.String(), nil)
	if err != nil {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.http.Do(request)
	if err != nil {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || mediaType != "application/json" {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCreateRunResponseBytes+1))
	if err != nil || len(body) > maxCreateRunResponseBytes {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	expected, err := json.Marshal(createRunBody{
		ExperimentID: strings.ToLower(plan.Environment.ExperimentID), DisplayName: plan.DisplayName,
		PipelineVersionReference: pipelineVersionReference{PipelineID: strings.ToLower(plan.PipelineID), PipelineVersionID: strings.ToLower(plan.PipelineVersionID)},
		RuntimeConfig:            runtimeConfig{Parameters: map[string]string{"execution_id": plan.ExecutionID, "spec_hash": plan.SpecHash}, PipelineRoot: plan.Owner.PipelineRoot},
		ServiceAccount:           plan.Environment.Identities.KFPStepServiceAccount,
	})
	if err != nil || confirmedRunID(body, expected) != runID {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	// KFP 2.16.0 run.proto child_tasks are dependent task/node references.
	// They are not the logical task's own main Pod or workload identity proof.
	var run map[string]any
	if json.Unmarshal(body, &run) != nil {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	result := ManagedRun{}
	if value, present := run["state"]; present {
		state, ok := value.(string)
		if !ok {
			return ManagedRun{}, ErrManagedRunUnverified
		}
		result.State = state
	}
	if value, present := run["finished_at"]; present {
		finished, ok := value.(string)
		at, err := time.Parse(time.RFC3339Nano, finished)
		if !ok || err != nil || at.IsZero() {
			return ManagedRun{}, ErrManagedRunUnverified
		}
		result.FinishedAt = at.UTC()
	}
	details, ok := run["run_details"].(map[string]any)
	if !ok {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	tasks, ok := details["task_details"].([]any)
	if !ok {
		return ManagedRun{}, ErrManagedRunUnverified
	}
	result.Tasks = make([]ManagedTask, 0, len(tasks))
	seenTasks := make(map[string]bool, len(tasks))
	for _, value := range tasks {
		task, ok := value.(map[string]any)
		if !ok {
			return ManagedRun{}, ErrManagedRunUnverified
		}
		if _, retired := task["pod_name"]; retired {
			return ManagedRun{}, ErrManagedRunUnverified
		}
		observed := ManagedTask{}
		for key, target := range map[string]*string{"run_id": &observed.RunID, "task_id": &observed.ID, "display_name": &observed.Name, "state": &observed.State} {
			if value, present := task[key]; present {
				text, ok := value.(string)
				if !ok {
					return ManagedRun{}, ErrManagedRunUnverified
				}
				*target = text
			}
		}
		if observed.RunID != runID || !managedTaskID(observed.ID) || observed.Name == "" || len(observed.Name) > 256 || strings.TrimSpace(observed.Name) != observed.Name || strings.IndexFunc(observed.Name, func(r rune) bool { return r < ' ' || r == 127 }) >= 0 || seenTasks[observed.ID] {
			return ManagedRun{}, ErrManagedRunUnverified
		}
		seenTasks[observed.ID] = true
		if value, present := task["child_tasks"]; present {
			children, ok := value.([]any)
			if !ok {
				return ManagedRun{}, ErrManagedRunUnverified
			}
			seen := make(map[ManagedTaskReference]bool, len(children))
			for _, value := range children {
				child, ok := value.(map[string]any)
				if !ok || len(child) != 1 {
					return ManagedRun{}, ErrManagedRunUnverified
				}
				reference := ManagedTaskReference{}
				if value, present := child["pod_name"]; present {
					name, ok := value.(string)
					if !ok || name == "" || len(validation.IsDNS1123Subdomain(name)) != 0 {
						return ManagedRun{}, ErrManagedRunUnverified
					}
					reference.PodName = name
				} else if value, present := child["task_id"]; present {
					id, ok := value.(string)
					if !ok || !managedTaskID(id) {
						return ManagedRun{}, ErrManagedRunUnverified
					}
					reference.TaskID = id
				} else {
					return ManagedRun{}, ErrManagedRunUnverified
				}
				if seen[reference] {
					return ManagedRun{}, ErrManagedRunUnverified
				}
				seen[reference] = true
				observed.ChildTasks = append(observed.ChildTasks, reference)
			}
		}
		result.Tasks = append(result.Tasks, observed)
	}
	return result, nil
}

// Task IDs are opaque upstream strings, not assumed to share Run UUID syntax.
func managedTaskID(value string) bool {
	return value != "" && len(value) <= 256 && strings.IndexFunc(value, func(r rune) bool { return r <= ' ' || r > '~' }) < 0
}
