package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRuntimeLoggerUsesKratosRedaction(t *testing.T) {
	var output bytes.Buffer
	logger := newRuntimeLogger(&output)
	logger.Info("redaction check", "token", "secret-token", "args", "secret-payload")

	line := output.String()
	if strings.Contains(line, "secret-token") || strings.Contains(line, "secret-payload") {
		t.Fatalf("runtime logger leaked filtered values: %s", line)
	}
	if strings.Count(line, `"***"`) != 2 {
		t.Fatalf("runtime logger did not use Kratos key filtering: %s", line)
	}
}

func TestRuntimeLoggerIncludesProcessIdentityAndSource(t *testing.T) {
	originalID, originalName, originalVersion := id, Name, Version
	id, Name, Version = "layout-test-instance", "ani-modeldev-service", "layout-test-version"
	t.Cleanup(func() { id, Name, Version = originalID, originalName, originalVersion })

	var output bytes.Buffer
	newRuntimeLogger(&output).Info("identity check")
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("decode structured log: %v; output=%q", err, output.String())
	}
	for key, want := range map[string]string{
		"msg":             "identity check",
		"service.id":      "layout-test-instance",
		"service.name":    "ani-modeldev-service",
		"service.version": "layout-test-version",
	} {
		if got, _ := record[key].(string); got != want {
			t.Fatalf("log field %s = %q, want %q; record=%v", key, got, want, record)
		}
	}
	if timestamp, _ := record["time"].(string); timestamp == "" {
		t.Fatalf("structured log has no timestamp: %v", record)
	}
	source, ok := record["source"].(map[string]any)
	if !ok || source["file"] == "" || source["line"] == nil {
		t.Fatalf("structured log has no caller source: %v", record)
	}
}

func TestMainProcessHandlesSignalAndClosesListeners(t *testing.T) {
	if testing.Short() {
		t.Skip("external process gate is disabled by -short")
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve command package path")
	}
	commandDir := filepath.Dir(filename)
	repositoryRoot := filepath.Clean(filepath.Join(commandDir, "..", ".."))
	binaryPath := filepath.Join(t.TempDir(), "service")
	build := exec.Command("go", "build", "-trimpath", "-o", binaryPath, ".")
	build.Dir = commandDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build process binary: %v\n%s", err, output)
	}

	grpcAddress := reserveAddress(t)
	adminAddress := reserveAddress(t)
	for adminAddress == grpcAddress {
		adminAddress = reserveAddress(t)
	}
	var stdout, stderr bytes.Buffer
	process := exec.Command(binaryPath, "-conf", filepath.Join(repositoryRoot, "configs"))
	process.Dir = repositoryRoot
	process.Stdout = &stdout
	process.Stderr = &stderr
	process.Env = runtimeEnvironment(
		"ANI_SERVER_GRPC_ADDR="+grpcAddress,
		"ANI_SERVER_ADMIN_ADDR="+adminAddress,
		"ANI_SERVER_SHUTDOWN_TIMEOUT=2s",
	)
	if err := process.Start(); err != nil {
		t.Fatalf("start process binary: %v", err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- process.Wait() }()
	t.Cleanup(func() {
		if process.ProcessState == nil || !process.ProcessState.Exited() {
			_ = process.Process.Kill()
			<-processDone
		}
	})

	waitForHTTP(t, "http://"+adminAddress+"/healthz")
	assertProductionBusinessPortClosed(t, grpcAddress)
	if err := process.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send interrupt: %v", err)
	}
	select {
	case err := <-processDone:
		if err != nil {
			t.Fatalf("process exit after interrupt: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("process exceeded graceful shutdown bound; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	client := &http.Client{Timeout: 250 * time.Millisecond}
	if response, err := client.Get("http://" + adminAddress + "/healthz"); err == nil {
		response.Body.Close()
		t.Fatalf("admin listener remained reachable after process exit: %s", response.Status)
	}
}

func runtimeEnvironment(overrides ...string) []string {
	blocked := make(map[string]struct{}, len(overrides))
	for _, override := range overrides {
		key, _, _ := strings.Cut(override, "=")
		blocked[key] = struct{}{}
	}
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, found := blocked[key]; !found {
			environment = append(environment, entry)
		}
	}
	return append(environment, overrides...)
}
