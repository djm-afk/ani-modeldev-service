//go:build cpu_mainflow

package submittest_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/kfp"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/lifecycle"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/objectstore"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/runtimeproof"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/stepidentity"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/trainer"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

type recoveryProcessConfig struct {
	Request                 biz.PipelineDispatchRequest
	Schema, KubeURL, KFPURL string
	KubeCA, KFPCA           []byte
}

// This subprocess uses the restricted runtime role and production owner code.
// It never installs a schema, accepts a command, or creates a training resource.
func TestOwnerRecoveryProcess(t *testing.T) {
	path := os.Getenv("CPU_P01_OWNER_PROCESS_CONFIG")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("owner recovery configuration unavailable")
	}
	var config recoveryProcessConfig
	if json.Unmarshal(raw, &config) != nil {
		t.Fatal("invalid recovery configuration")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	database, err := pgxpool.ParseConfig(os.Getenv("CPU_P01_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("restricted runtime database configuration invalid")
	}
	database.ConnConfig.RuntimeParams["search_path"] = config.Schema
	database.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, database)
	if err != nil {
		t.Fatal("owner recovery database unavailable")
	}
	defer pool.Close()
	admission := config.Request.Admission
	facts, err := lifecycle.New(pool).GetRuntime(ctx, admission.TenantID, admission.ExecutionID)
	if err != nil || facts.CloseGeneration == 0 {
		t.Fatal("recovery process did not recover the persisted fence")
	}
	mode := os.Getenv("CPU_P01_OWNER_PROCESS_MODE")
	if err := os.WriteFile(path+"."+mode+".ready", []byte("durable-fence-loaded"), 0600); err != nil {
		t.Fatal(err)
	}
	if mode == "before-crash" {
		<-ctx.Done()
		return
	}
	roots := x509.NewCertPool()
	certificate, err := x509.ParseCertificate(config.KFPCA)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(certificate)
	runs, err := kfp.New(kfp.Config{ConnectionRef: admission.Snapshot.Environment.KFPConnectionRef, Endpoint: config.KFPURL, RootCAs: roots, Timeout: 5 * time.Second}, tokenProviderFunc(func(context.Context, string, cpup01.EnvironmentBindingSnapshot) (string, error) {
		return "synthetic-kfp-owner", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	kube, err := dynamic.NewForConfig(&rest.Config{Host: config.KubeURL, BearerToken: "synthetic-kube-owner", Timeout: 10 * time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: config.KubeCA})}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := stepidentity.New(kube, "ani-modeldev-managed-step")
	if err != nil {
		t.Fatal(err)
	}
	dispatch := submission.New(pool)
	proof := runtimeproof.New(kube, runs, objectstore.NewVerifier(s3.New(s3.Options{Region: "us-east-1"}), admission.Snapshot.PublicationScope.StorageConnectionID), dispatch)
	steps, err := biz.NewManagedSteps(dispatch, execution.New(pool), identity, proof)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := biz.NewManagedRuntime(steps, lifecycle.New(pool), trainer.New(kube), proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	closer, err := biz.NewExecutionCloser(managed, runs, proof, proof, proof)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := biz.NewCloseWorker(lifecycle.New(pool), closer, biz.PipelineDispatchBinding{TenantID: admission.TenantID, Environment: admission.Snapshot.Environment, Owner: config.Request.Owner}, 10, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatal("recovered owner worker failed")
	}
}

func restartRecoveryProcess(t *testing.T, ctx context.Context, f *completeFixture, pool *pgxpool.Pool) (context.CancelFunc, <-chan error) {
	t.Helper()
	config := recoveryProcessConfig{Request: f.request, Schema: pool.Config().ConnConfig.RuntimeParams["search_path"], KubeURL: f.kube.URL, KFPURL: f.kfp.URL, KubeCA: f.kube.Certificate().Raw, KFPCA: f.kfp.Certificate().Raw}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "owner.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	spawn := func(mode string) (*exec.Cmd, <-chan error) {
		command := exec.Command(os.Args[0], "-test.run=^TestOwnerRecoveryProcess$", "-test.v")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CPU_P01_TEST_DATABASE_ADMIN_URL=") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CPU_P01_OWNER_PROCESS_CONFIG="+path, "CPU_P01_OWNER_PROCESS_MODE="+mode)
		var output bytes.Buffer
		command.Stdout, command.Stderr = &output, &output
		if err := command.Start(); err != nil {
			t.Fatal("owner child process could not start")
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait(); close(done) }()
		t.Cleanup(func() {
			select {
			case <-done:
				return
			default:
			}
			_ = command.Process.Kill()
			<-done
		})
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(path + "." + mode + ".ready"); err == nil {
				return command, done
			}
			select {
			case err := <-done:
				t.Fatalf("owner child failed before recovering database facts: %v: %s", err, output.String())
			case <-ctx.Done():
				t.Fatal("owner child did not read the durable fence")
			case <-ticker.C:
			}
		}
	}
	first, stopped := spawn("before-crash")
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err == nil {
		t.Fatal("owner crash injection did not terminate the process")
	}
	second, done := spawn("recovered")
	if first.Process.Pid == second.Process.Pid {
		t.Fatal("owner process was not replaced")
	}
	t.Logf("OWNER_PROCESS_RECOVERY: killed task-owned PID %d after durable fence read; restarted PID %d against the same restricted database", first.Process.Pid, second.Process.Pid)
	return func() { _ = second.Process.Signal(syscall.SIGTERM) }, done
}
