package biz

import (
	"context"
	"errors"
	"testing"
)

type currentCloserBoundary struct {
	ManagedRunCloser
	OwnerWriterVerifier
	StepExitCleanupBoundary
	ComputeCleanupBoundary
}

type currentCloserStore struct {
	ExecutionRuntimeRepository
	StepExitCleanupRepository
	ComputeCleanupRepository
}

type currentStepReceiptStore struct {
	ExecutionRuntimeRepository
	StepExitCleanupRepository
}

type currentComputeReceiptStore struct {
	ExecutionRuntimeRepository
	ComputeCleanupRepository
}

func TestCurrentCloserRequiresAllCleanupDependencies(t *testing.T) {
	boundary := &currentCloserBoundary{}
	current := &ManagedRuntime{repository: &currentCloserStore{}}
	if _, err := NewExecutionCloser(current, boundary, boundary, boundary, boundary); err != nil {
		t.Fatal("complete current close assembly rejected", err)
	}
	for _, missing := range []string{"runtime", "run", "writer", "step cleanup", "compute cleanup", "step receipt port", "compute receipt port"} {
		runtime := current
		var run ManagedRunCloser = boundary
		var writer OwnerWriterVerifier = boundary
		var step StepExitCleanupBoundary = boundary
		var compute ComputeCleanupBoundary = boundary
		switch missing {
		case "runtime":
			runtime = nil
		case "run":
			run = nil
		case "writer":
			writer = nil
		case "step cleanup":
			step = nil
		case "compute cleanup":
			compute = nil
		case "step receipt port":
			runtime = &ManagedRuntime{repository: &currentComputeReceiptStore{}}
		case "compute receipt port":
			runtime = &ManagedRuntime{repository: &currentStepReceiptStore{}}
		}
		if closer, err := NewExecutionCloser(runtime, run, writer, step, compute); !errors.Is(err, ErrRuntimeNotReady) || closer != nil {
			t.Fatalf("missing %s accepted: %v", missing, err)
		}
	}
}

func TestCurrentCloserCannotSucceedWithoutCleanup(t *testing.T) {
	closer := &ExecutionCloser{}
	if _, err := closer.reconcileStepExits(context.Background(), ManagedRuntimeResult{}, RunAuthorityCandidate{}, false); !errors.Is(err, ErrRuntimeNotReady) {
		t.Fatal("missing step cleanup produced successful closure", err)
	}
	if _, err := closer.reconcileComputeCleanup(context.Background(), ManagedRuntimeResult{}, RunAuthorityCandidate{}, false); !errors.Is(err, ErrRuntimeNotReady) {
		t.Fatal("missing compute cleanup produced successful closure", err)
	}
}
