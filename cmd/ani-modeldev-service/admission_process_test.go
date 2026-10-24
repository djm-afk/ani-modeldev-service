package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestConfiguredAdmissionProcessLoadsPrivateConfigAndStopsOnSIGTERM(t *testing.T) {
	fixture := prepareConfiguredAdmission(t)
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("ADMISSION_PROCESS_PREFLIGHT: command package path unavailable; behavior NOT_RUN")
	}
	commandDir := filepath.Dir(filename)
	repositoryRoot := filepath.Clean(filepath.Join(commandDir, "..", ".."))
	binaryPath := filepath.Join(t.TempDir(), "service")
	buildContext, buildCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildContext, "go", "build", "-trimpath", "-o", binaryPath, ".")
	build.Dir = commandDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("ADMISSION_PROCESS_PREFLIGHT: binary build failed; behavior NOT_RUN: %v\n%s", err, output)
	}
	configBytes, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(fixture.config)
	if err != nil {
		t.Fatalf("ADMISSION_PROCESS_PREFLIGHT: private config encoding failed; behavior NOT_RUN: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal("ADMISSION_PROCESS_PREFLIGHT: private config file unavailable; behavior NOT_RUN")
	}
	// The child must consume -conf through main's file source and Kratos Scan.
	// Ambient overrides and parent database test credentials are unnecessary;
	// its restricted runtime connection is available only by the mounted ref.
	process := exec.Command(binaryPath, "-conf", configPath)
	process.Dir = repositoryRoot
	process.Stdout, process.Stderr = io.Discard, io.Discard
	process.Env = []string{}
	for _, entry := range runtimeEnvironment() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "ANI" || strings.HasPrefix(key, "ANI_") || strings.HasPrefix(key, "CPU_P01_TEST_DATABASE_") {
			continue
		}
		process.Env = append(process.Env, entry)
	}
	if err := process.Start(); err != nil {
		t.Fatalf("ADMISSION_PROCESS_PREFLIGHT: service process did not start; behavior NOT_RUN: %v", err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- process.Wait() }()
	exited := false
	t.Cleanup(func() {
		if exited {
			return
		}
		_ = process.Process.Kill()
		select {
		case <-processDone:
			exited = true
		case <-time.After(3 * time.Second):
			t.Error("ADMISSION_PROCESS_CLEANUP: child did not exit after bounded kill cleanup")
		}
	})

	waitForHTTP(t, "http://"+fixture.config.Server.Admin.Addr+"/healthz")
	connection, err := grpc.NewClient(fixture.config.Server.Grpc.Addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: fixture.certificates.Roots, ServerName: commandtls.ServerDNSName,
		Certificates: []tls.Certificate{fixture.certificates.Governance},
	})))
	if err != nil {
		t.Fatalf("ADMISSION_PROCESS_PREFLIGHT: mTLS client unavailable; behavior NOT_RUN: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection.Connect()
	for state := connection.GetState(); state != connectivity.Ready; state = connection.GetState() {
		if !connection.WaitForStateChange(ctx, state) {
			t.Fatalf("ADMISSION_PROCESS_BEHAVIOR: private-config process did not expose its actual mTLS listener: %v", ctx.Err())
		}
	}
	rpcContext := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"x-ani-tenant-id", fixture.tenantID, "x-ani-actor", "governance:user:42", "x-ani-request-id", uuid.NewString(),
	))
	response, err := finalizeConfiguredAdmission(rpcContext, modeldevv1.NewModelDevAdmissionServiceClient(connection), fixture.request)
	if err != nil || response == nil {
		t.Fatalf("ADMISSION_PROCESS_BEHAVIOR: actual process could not resolve the configured candidate: %v", err)
	}
	snapshot, err := contractpb.DecodeSnapshot(response.Snapshot)
	if err != nil {
		t.Fatalf("ADMISSION_PROCESS_BEHAVIOR: invalid candidate snapshot: %v", err)
	}
	canonical, err := snapshot.Canonical()
	if err != nil || !bytes.Equal(canonical, fixture.wantCanonical) || response.ExecutionSpecHash != fixture.wantHash {
		t.Fatalf("ADMISSION_PROCESS_BEHAVIOR: candidate differs from the independently authored snapshot and hash: %v", err)
	}
	assertProductionNotReadyWithoutBusinessAdapters(t, fixture.config.Server.Admin.Addr)
	observer := fixture.openPool()
	if countConfiguredAdmissionConnections(t, observer, fixture.applicationName) == 0 {
		t.Fatal("ADMISSION_PROCESS_PREFLIGHT: actual child database connection was not observed; connection cleanup behavior NOT_RUN")
	}
	t.Log("ADMISSION_PROCESS_BEHAVIOR: private -conf and Kratos Scan reached actual mTLS resolution with the independent expected snapshot; readiness remains 503")
	_ = connection.Close()
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("ADMISSION_PROCESS_BEHAVIOR: could not send SIGTERM to the actual child: %v", err)
	}
	select {
	case err := <-processDone:
		exited = true
		if err != nil {
			t.Fatalf("ADMISSION_PROCESS_BEHAVIOR: actual process did not exit successfully after SIGTERM: %v", err)
		}
	case <-time.After(fixture.config.Server.ShutdownTimeout.AsDuration() + 3*time.Second):
		t.Fatal("ADMISSION_PROCESS_BEHAVIOR: actual process exceeded its SIGTERM shutdown bound")
	}
	for _, address := range []string{fixture.config.Server.Grpc.Addr, fixture.config.Server.Admin.Addr} {
		if remaining, err := net.DialTimeout("tcp", address, 250*time.Millisecond); err == nil {
			_ = remaining.Close()
			t.Fatal("ADMISSION_PROCESS_BEHAVIOR: listener remained reachable after child exit")
		}
	}
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cleanupCancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for countConfiguredAdmissionConnections(t, observer, fixture.applicationName) != 0 {
		select {
		case <-tick.C:
		case <-cleanupContext.Done():
			t.Fatal("ADMISSION_PROCESS_BEHAVIOR: exited child retained its exact owned database connection")
		}
	}
	t.Log("ADMISSION_PROCESS_BEHAVIOR: SIGTERM exited zero; both listeners closed and exact owned PostgreSQL connections released")
}
