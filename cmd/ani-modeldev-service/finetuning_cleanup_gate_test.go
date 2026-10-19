//go:build finetuning_core_flow

package main

import (
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func TestCoreFlowCompletionWaitsForOriginalComputeCleanup(t *testing.T) {
	now := time.Now().UTC()
	for _, phase := range []string{"", "STARTED", "NEEDS_REVIEW", "APPLIED"} {
		t.Run(phase, func(t *testing.T) {
			state := biz.ExecutionRuntime{ClosedAt: &now, CloseEvidence: &biz.ManagedCloseEvidence{}, StepExitCleanup: &biz.StepExitCleanupReceipt{CompletedAt: &now}}
			if phase != "" {
				state.ComputeCleanup = &biz.ComputeCleanupReceipt{CleanupReceipt: biz.CleanupReceipt{Phase: phase}}
			}
			if coreFlowCleanupComplete(state) != (phase == "APPLIED") {
				t.Fatal("driver stopped its owner before automatic compute cleanup completed", phase)
			}
		})
	}
	if coreFlowCleanupComplete(biz.ExecutionRuntime{CloseEvidence: &biz.ManagedCloseEvidence{NoDispatch: true}}) {
		t.Fatal("no-dispatch without durable CLOSED completed")
	}
	if !coreFlowCleanupComplete(biz.ExecutionRuntime{ClosedAt: &now, CloseEvidence: &biz.ManagedCloseEvidence{NoDispatch: true}}) {
		t.Fatal("genuine undispatched closure required nonexistent compute resources")
	}
}
