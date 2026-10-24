package component

import (
	"context"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"time"
)

// The close invocation keeps its existing independent finite owner budget.
// No normal business role can borrow this budget or restart its own S window.
func (r *Runner) roleContext(ctx context.Context, role string) (context.Context, context.CancelFunc, error) {
	if role == "close" {
		c, cancel := context.WithTimeout(ctx, 30*time.Minute)
		return c, cancel, nil
	}
	call, err := r.callContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	var timing *modeldevv1.StepTiming
	if role == "prepare" {
		response, err := r.client.BeginExecution(call, &modeldevv1.BeginExecutionRequest{Context: r.config.Context})
		if err != nil {
			return nil, nil, err
		}
		timing = response.GetTiming()
	} else if role == "collect" || role == "publish" || role == "register" {
		response, err := r.client.BeginManagedStep(call, &modeldevv1.BeginManagedStepRequest{Context: r.config.Context})
		if err != nil {
			return nil, nil, err
		}
		timing = response.GetTiming()
	}
	execution, _, err := r.configuration(ctx)
	if err != nil {
		return nil, nil, err
	}
	deadline := execution.Snapshot.DeadlineAt
	if role == "prepare" || role == "collect" || role == "publish" || role == "register" {
		if timing == nil || timing.Role != r.config.Context.Step || timing.AdmittedAt == nil || timing.DeadlineAt == nil || timing.AdmittedAt.CheckValid() != nil || timing.DeadlineAt.CheckValid() != nil {
			return nil, nil, ErrConfiguration
		}
		expected, err := execution.Snapshot.Timing.DeadlineFor(timing.AdmittedAt.AsTime())
		if err != nil || !expected.Equal(timing.DeadlineAt.AsTime()) {
			return nil, nil, ErrConfiguration
		}
		deadline = expected
		r.stepTiming = timing
	}
	c, cancel := context.WithDeadline(ctx, deadline)
	if err := c.Err(); err != nil {
		cancel()
		return nil, nil, err
	}
	return c, cancel, nil
}
