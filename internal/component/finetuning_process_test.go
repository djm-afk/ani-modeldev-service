package component

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// This probes the process adapter with real subprocesses, independently of the
// LoRA workload. The writer inherits its parent's process group and ignores TERM.
func TestFinetuningProcessCancellationReapsWriters(t *testing.T) {
	for _, probe := range []struct {
		name string
		code int32
	}{
		{name: "term-exit", code: 27},
		{name: "requires-kill", code: -1},
	} {
		t.Run(probe.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() {
				if pid, err := finetuningProbePID(dir, "parent.pid"); err == nil {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
				if pid, err := finetuningProbePID(dir, "writer.pid"); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			started, finished, code, err := runFinetuningProcess(ctx, cpup01.ProgramRef{
				Command: []string{os.Args[0], "-test.run=^TestFinetuningProcessProbeHelper$", "--", "finetuning-process-probe", probe.name, dir},
			})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancellation error = %v", err)
			}
			if started.IsZero() || finished.IsZero() || !finished.After(started) || finished.Before(deadline) {
				t.Fatalf("invalid process timestamps: start=%s deadline=%s finish=%s", started, deadline, finished)
			}
			parent, err := finetuningProbePID(dir, "parent.pid")
			if err != nil {
				t.Fatalf("parent never reached ready: %v", err)
			}
			writer, err := finetuningProbePID(dir, "writer.pid")
			if err != nil {
				t.Fatalf("writer never reached ready: %v", err)
			}
			if probe.name == "term-exit" {
				if _, err := os.Stat(filepath.Join(dir, "term-observed")); err != nil {
					t.Fatalf("parent did not observe TERM: %v", err)
				}
				if finished.Sub(deadline) >= 5*time.Second {
					t.Error("TERM handler did not finish within the grace period")
				}
			} else if finished.Sub(deadline) < 5*time.Second {
				t.Error("TERM-ignoring entry escaped before the KILL grace period")
			}
			until := time.Now().Add(time.Second)
			for finetuningProbeAlive(parent) || finetuningProbeAlive(writer) {
				if time.Now().After(until) {
					t.Fatalf("live process remains: parent=%d writer=%d", parent, writer)
				}
				time.Sleep(10 * time.Millisecond)
			}
			before, err := os.ReadFile(filepath.Join(dir, "heartbeat"))
			if err != nil || len(before) == 0 {
				t.Fatalf("no actual writer activity: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
			after, err := os.ReadFile(filepath.Join(dir, "heartbeat"))
			if err != nil || string(after) != string(before) {
				t.Fatalf("writer changed output after termination: %v", err)
			}
			t.Logf("actual process: parent=%d writer=%d started=%s deadline=%s finished=%s exit=%d remaining_writers=0", parent, writer, started.Format(time.RFC3339Nano), deadline.UTC().Format(time.RFC3339Nano), finished.Format(time.RFC3339Nano), code)
			if code != probe.code {
				t.Errorf("reported exit = %d, actual process exit = %d", code, probe.code)
			}
		})
	}
}

func TestFinetuningProcessProbeHelper(t *testing.T) {
	args := os.Args
	marker := -1
	for i, arg := range args {
		if arg == "finetuning-process-probe" {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	if marker+3 != len(args) {
		os.Exit(90)
	}
	mode, dir := args[marker+1], args[marker+2]
	if mode == "writer" {
		signal.Ignore(syscall.SIGTERM)
		if os.WriteFile(filepath.Join(dir, "writer.pid"), []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
			os.Exit(91)
		}
		heartbeat, err := os.OpenFile(filepath.Join(dir, "heartbeat"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(92)
		}
		for {
			if _, err := fmt.Fprintln(heartbeat, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				os.Exit(92)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	term := make(chan os.Signal, 1)
	if mode == "requires-kill" {
		signal.Ignore(syscall.SIGTERM)
	} else if mode == "term-exit" {
		signal.Notify(term, syscall.SIGTERM)
	} else {
		os.Exit(93)
	}
	writer := exec.Command(os.Args[0], "-test.run=^TestFinetuningProcessProbeHelper$", "--", "finetuning-process-probe", "writer", dir)
	if writer.Start() != nil {
		os.Exit(94)
	}
	readyBy := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "heartbeat")); err == nil {
			break
		}
		if time.Now().After(readyBy) {
			os.Exit(95)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if os.WriteFile(filepath.Join(dir, "parent.pid"), []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
		os.Exit(96)
	}
	if mode == "term-exit" {
		<-term
		if os.WriteFile(filepath.Join(dir, "term-observed"), []byte("SIGTERM"), 0600) != nil {
			os.Exit(97)
		}
		os.Exit(27)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func finetuningProbePID(dir, name string) (int, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || pid <= 1 {
		return 0, fmt.Errorf("invalid probe pid: %q", data)
	}
	return pid, nil
}

func finetuningProbeAlive(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	// A briefly unreaped orphan is not a live writer. The state follows the final
	// ')' because the Linux comm field can contain spaces or parentheses.
	close := strings.LastIndexByte(string(stat), ')')
	if close < 0 || len(stat) <= close+2 {
		return true
	}
	return stat[close+2] != 'Z' && stat[close+2] != 'X'
}
