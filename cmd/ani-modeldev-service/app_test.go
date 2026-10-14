package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
)

func TestBuildAppRunsProductionComposition(t *testing.T) {
	grpcAddress := reserveAddress(t)
	adminAddress := reserveAddress(t)
	for adminAddress == grpcAddress {
		adminAddress = reserveAddress(t)
	}
	bc := &conf.Bootstrap{Server: &conf.Server{
		Grpc:            &conf.Server_GRPC{Network: "tcp", Addr: grpcAddress, Timeout: durationpb.New(time.Second)},
		Admin:           &conf.Server_Admin{Network: "tcp", Addr: adminAddress, Timeout: durationpb.New(time.Second)},
		ShutdownTimeout: durationpb.New(3 * time.Second),
	}}
	var logs bytes.Buffer
	app, err := buildApp(bc, newRuntimeLogger(&logs))
	if err != nil {
		t.Fatalf("buildApp() error = %v", err)
	}

	runResult := make(chan error, 1)
	go func() { runResult <- app.Run() }()
	waitForHTTP(t, "http://"+adminAddress+"/healthz")
	assertProductionAdmin(t, adminAddress)
	assertProductionBusinessPortClosed(t, grpcAddress)
	assertProductionNotReadyWithoutBusinessAdapters(t, adminAddress)

	if err := app.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("production composition did not stop within its timeout")
	}
	if !strings.Contains(logs.String(), `"service.name":"ani-modeldev-service"`) {
		t.Fatalf("structured service identity missing from logs: %s", logs.String())
	}
	if !regexp.MustCompile(`"trace_id":"[0-9a-f]{32}"`).MatchString(logs.String()) {
		t.Fatalf("request trace correlation missing from logs: %s", logs.String())
	}
	if !regexp.MustCompile(`"span_id":"[0-9a-f]{16}"`).MatchString(logs.String()) {
		t.Fatalf("request span correlation missing from logs: %s", logs.String())
	}
}

func TestBuildAppRejectsInvalidConfig(t *testing.T) {
	_, err := buildApp(&conf.Bootstrap{}, newRuntimeLogger(io.Discard))
	if err == nil {
		t.Fatal("buildApp() unexpectedly accepted an incomplete config")
	}
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved address: %v", err)
	}
	return address
}

func waitForHTTP(t *testing.T, endpoint string) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("endpoint did not become ready: %s", endpoint)
}

func assertProductionAdmin(t *testing.T, address string) {
	t.Helper()
	response, err := (&http.Client{Timeout: time.Second}).Get("http://" + address + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read /healthz: %v", err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("GET /healthz = %d %q", response.StatusCode, body)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("GET /healthz content type = %q", contentType)
	}
}

func assertProductionBusinessPortClosed(t *testing.T, address string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatalf("plaintext business port %s is reachable without a configured mTLS listener", address)
	}
}

func assertProductionNotReadyWithoutBusinessAdapters(t *testing.T, address string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		response, err := client.Get("http://" + address + "/readyz")
		if err != nil {
			t.Errorf("GET /readyz after healthy startup: %v", err)
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Errorf("read /readyz: %v", err)
			return
		}
		if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"reason":"NOT_READY"`) {
			t.Errorf("GET /readyz without business adapters = %d %q, want 503 NOT_READY", response.StatusCode, body)
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		// A listening process must not become business-ready as startup finishes.
		time.Sleep(10 * time.Millisecond)
	}
}
