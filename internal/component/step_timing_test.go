package component_test

import (
	"context"
	"errors"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestManagedNormalRolesConsumeOneOriginalWindowAndRejectRenewal(t *testing.T) {
	for role, step := range map[string]modeldevv1.PipelineStep{"prepare": modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE, "collect": modeldevv1.PipelineStep_PIPELINE_STEP_COLLECT, "publish": modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH, "register": modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER} {
		for _, kind := range []string{"expired", "renewed", "wrong-role", "valid-total-minimum"} {
			t.Run(role+"/"+kind, func(t *testing.T) {
				config := taskConfiguration(t, step)
				now := time.Now().UTC().Truncate(time.Microsecond)
				timing, err := (cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 300, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900}).Resolve(now.Add(-time.Minute), nil)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "expired" {
					timing, err = (cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 30}).Resolve(now.Add(-time.Minute), nil)
					if err != nil {
						t.Fatal(err)
					}
				}
				response := timedConfigurationResponse(t, config, timing)
				deadline, err := timing.DeadlineFor(timing.AcceptedAt)
				if err != nil {
					t.Fatal(err)
				}
				clock := &modeldevv1.StepTiming{Role: step, AdmittedAt: timestamppb.New(timing.AcceptedAt), DeadlineAt: timestamppb.New(deadline)}
				if kind == "renewed" {
					clock.DeadlineAt = timestamppb.New(deadline.Add(time.Second))
				}
				if kind == "wrong-role" {
					clock.Role = modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT
				}
				client := &taskConfigurationRPC{response: response, timingOverride: clock}
				runner, err := component.New(config, client, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				err = runner.Run(context.Background(), role)
				if kind == "expired" {
					if !errors.Is(err, context.DeadlineExceeded) || client.configurationCalls != 1 {
						t.Fatal("expired original role admitted work", err)
					}
					return
				}
				if kind == "renewed" || kind == "wrong-role" {
					if !errors.Is(err, component.ErrConfiguration) || client.configurationCalls != 1 {
						t.Fatal("role clock renewed or mixed", err)
					}
					return
				}
				// Missing external adapters stop the real Runner after the timing
				// boundary; any second configuration RPC must keep the original min.
				if !errors.Is(err, component.ErrConfiguration) {
					t.Fatal("missing actual external adapter did not stop role", err)
				}
				if client.configurationCalls > 1 && !client.configurationDeadline.Equal(timing.ExecutionDeadlineAt) {
					t.Fatal("normal role exceeded total execution deadline")
				}
			})
		}
	}
}
