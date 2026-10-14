package component

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Runtime budgets come from the original frozen execution. Close-only before
// prepare retains the old bounded failure-close behavior without configuration.
func (r *Runner) invocationBudget(ctx context.Context, role string) (time.Duration, error) {
	if r.config.CloseOnly || role == "prepare" || role == "close" || role == "train-wait" && r.config.Context.Identity.OperationId != "" {
		return 30 * time.Minute, nil
	}
	e, _, err := r.configuration(ctx)
	if err != nil {
		return 0, err
	}
	if e.Snapshot.Kind != "FINETUNING" {
		return 30 * time.Minute, nil
	}
	fine := e.Snapshot.Finetuning
	if fine == nil {
		return 0, ErrConfiguration
	}
	var seconds uint32
	switch role {
	case "prepare":
		seconds = fine.Budgets.PrepareTimeoutSeconds
	case "train-wait":
		seconds = fine.Budgets.TrainingTimeoutSeconds
	case "export":
		seconds = fine.Budgets.ExportTimeoutSeconds
	case "verify":
		seconds = fine.Budgets.VerifyTimeoutSeconds
	case "collect":
		seconds = fine.Budgets.CollectTimeoutSeconds
	case "publish":
		seconds = fine.Budgets.PublishTimeoutSeconds
	case "register":
		seconds = fine.Budgets.RPCTimeoutSeconds
	case "close":
		seconds = 600
	default:
		return 0, ErrConfiguration
	}
	remaining := time.Until(e.Snapshot.DeadlineAt)
	budget := time.Duration(seconds) * time.Second
	if remaining < budget {
		budget = remaining
	}
	if budget <= 0 {
		return 0, context.DeadlineExceeded
	}
	return budget, nil
}

func (r *Runner) finetuningCompute(ctx context.Context, role string) error {
	e, _, err := r.configuration(ctx)
	if err != nil {
		return err
	}
	fine := e.Snapshot.Finetuning
	if e.Snapshot.Kind != "FINETUNING" || fine == nil {
		return ErrConfiguration
	}
	program := fine.ExportProgram
	manifestPath := "reports/export/merged_manifest.json"
	if role == "verify" {
		program = fine.VerifyProgram
		manifestPath = "reports/verify/verify.json"
	}
	call, err := r.callContext(ctx)
	if err != nil {
		return err
	}
	permission, err := r.client.BeginFinetuningStep(call, &modeldevv1.BeginFinetuningStepRequest{Context: r.config.Context})
	// An uncertain first reply and every replay are observation-only. Never
	// derive a second process start from the durable record's existence.
	if err != nil {
		return err
	}
	if permission == nil || !permission.StartPermitted || permission.AdmittedAt == nil || permission.DeadlineAt == nil || permission.AdmittedAt.CheckValid() != nil || permission.DeadlineAt.CheckValid() != nil {
		return ErrNeedsReview
	}
	deadline := permission.DeadlineAt.AsTime()
	if deadline.After(e.Snapshot.DeadlineAt) || !deadline.After(permission.AdmittedAt.AsTime()) {
		return ErrConfiguration
	}
	compute, stop := context.WithDeadline(ctx, deadline)
	defer stop()
	if err := compute.Err(); err != nil {
		return err
	}
	started, finished, exitCode, err := runFinetuningProcess(compute, program)
	if started.IsZero() {
		return err
	}
	report := &modeldevv1.ReportFinetuningStepResultRequest{Context: r.config.Context, StartedAt: timestamppb.New(started), FinishedAt: timestamppb.New(finished), ExitCode: exitCode}
	if exitCode == 0 {
		ref, readErr := computeManifest(r.config.WorkspaceDirectory, manifestPath)
		if readErr != nil {
			return readErr
		}
		report.ResultManifest = ref
	}
	// A finished original process can report its late result within one bounded
	// I/O window. This grants no new compute or continuation budget.
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	call, callErr := r.callContext(record)
	if callErr != nil {
		return callErr
	}
	if _, reportErr := r.client.ReportFinetuningStepResult(call, report); reportErr != nil {
		return reportErr
	}
	return err
}

func runFinetuningProcess(ctx context.Context, p cpup01.ProgramRef) (time.Time, time.Time, int32, error) {
	if len(p.Command) == 0 {
		return time.Time{}, time.Time{}, 1, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, time.Time{}, 1, err
	}
	args := append(append([]string{}, p.Command[1:]...), p.ResolvedArgs...)
	cmd := exec.Command(p.Command[0], args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return time.Time{}, time.Time{}, 1, err
	}
	started := time.Now().UTC()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var result error
	select {
	case result = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-done:
			timer.Stop()
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		result = ctx.Err()
	}
	// Also remove any descendant left by an abnormally exited original entry.
	// The Pod/container termination proof remains the independent close gate.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	// Context cancellation describes why we waited; the reaped ProcessState
	// remains the authority for the entry's actual exit, including signals.
	code := int32(1)
	if cmd.ProcessState != nil {
		code = int32(cmd.ProcessState.ExitCode())
	}
	return started, time.Now().UTC(), code, result
}

func computeManifest(root, relative string) (*trainingv1.ManifestRef, error) {
	if !absoluteClean(root) {
		return nil, ErrConfiguration
	}
	// Each directory is checked with no-follow before opening the leaf.
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrConfiguration
	}
	for _, part := range []string{"reports", filepath.Base(filepath.Dir(relative))} {
		next, err := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if err != nil {
			return nil, ErrConfiguration
		}
		fd = next
	}
	leaf, err := syscall.Openat(fd, filepath.Base(relative), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	syscall.Close(fd)
	if err != nil {
		return nil, ErrConfiguration
	}
	file := os.NewFile(uintptr(leaf), relative)
	defer file.Close()
	var before, after syscall.Stat_t
	if syscall.Fstat(leaf, &before) != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Size <= 0 || before.Size > 65536 {
		return nil, ErrConfiguration
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, 65537))
	if err != nil || n != before.Size || syscall.Fstat(leaf, &after) != nil || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim || after.Nlink != 1 {
		return nil, ErrConfiguration
	}
	return &trainingv1.ManifestRef{RelativePath: relative, SizeBytes: n, Sha256: hex.EncodeToString(h.Sum(nil))}, nil
}

func (r *Runner) register(ctx context.Context) error {
	// Confirm the publisher and exact remote bytes through the same candidate
	// path used by CPU close, while this execution's original register Pod is
	// the sole confirmation role. The Model call follows that durable receipt.
	if err := r.confirmPublication(ctx); err != nil {
		return err
	}
	call, err := r.callContext(ctx)
	if err != nil {
		return err
	}
	_, err = r.client.RegisterModel(call, &modeldevv1.RegisterModelRequest{Context: r.config.Context})
	return err
}
