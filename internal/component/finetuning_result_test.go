package component

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type delayedFinetuningResultServer struct {
	modeldevv1.UnimplementedModelDevStepServiceServer
	delay        time.Duration
	done         chan struct{}
	doneOnce     sync.Once
	mu           sync.Mutex
	calls        int
	report       *modeldevv1.ReportFinetuningStepResultRequest
	metadata     metadata.MD
	deadline     time.Time
	acknowledged bool
}

func (s *delayedFinetuningResultServer) ReportFinetuningStepResult(ctx context.Context, report *modeldevv1.ReportFinetuningStepResultRequest) (*modeldevv1.ReportFinetuningStepResultResponse, error) {
	s.mu.Lock()
	s.calls++
	s.report = proto.Clone(report).(*modeldevv1.ReportFinetuningStepResultRequest)
	s.metadata, _ = metadata.FromIncomingContext(ctx)
	s.deadline, _ = ctx.Deadline()
	s.mu.Unlock()
	defer s.doneOnce.Do(func() { close(s.done) })
	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-timer.C:
		s.mu.Lock()
		s.acknowledged = true
		s.mu.Unlock()
		return &modeldevv1.ReportFinetuningStepResultResponse{States: &modeldevv1.ExecutionStates{}}, nil
	}
}

func TestFinetuningResultAcknowledgementUsesFrozenRPCBudget(t *testing.T) {
	for _, test := range []struct {
		name     string
		step     modeldevv1.PipelineStep
		manifest string
		budget   uint32
		expired  bool
	}{
		{name: "export-ack-after-five-seconds", step: modeldevv1.PipelineStep_PIPELINE_STEP_EXPORT, manifest: "reports/export/merged_manifest.json", budget: 600},
		{name: "verify-ack-after-five-seconds", step: modeldevv1.PipelineStep_PIPELINE_STEP_VERIFY, manifest: "reports/verify/verify.json", budget: 600},
		{name: "report-remains-bounded-by-short-frozen-budget", step: modeldevv1.PipelineStep_PIPELINE_STEP_EXPORT, manifest: "reports/export/merged_manifest.json", budget: 1, expired: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := &delayedFinetuningResultServer{delay: 6 * time.Second, done: make(chan struct{})}
			listener := bufconn.Listen(1 << 20)
			transport := grpc.NewServer()
			modeldevv1.RegisterModelDevStepServiceServer(transport, server)
			go func() { _ = transport.Serve(listener) }()
			t.Cleanup(transport.Stop)
			t.Cleanup(func() { _ = listener.Close() })
			conn, err := grpc.NewClient("passthrough:///finetuning-result", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			work := t.TempDir()
			token := filepath.Join(work, "token")
			if err := os.WriteFile(token, []byte("projected-result-token"), 0600); err != nil {
				t.Fatal(err)
			}
			manifestFile := filepath.Join(work, filepath.FromSlash(test.manifest))
			if err := os.MkdirAll(filepath.Dir(manifestFile), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestFile, []byte("{\"actual_process_exit_code\":0}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			manifest, err := computeManifest(work, test.manifest)
			if err != nil {
				t.Fatal(err)
			}
			claim := &modeldevv1.StepContext{Identity: &trainingv1.ExecutionIdentity{OperationId: "33333333-3333-4333-8333-333333333333", ExecutionId: "22222222-2222-4222-8222-222222222222", ExecutionSpecHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Association: &modeldevv1.RunAssociation{KfpRunId: "44444444-4444-4444-8444-444444444444", NamespaceName: "fine-execution", NamespaceUid: "55555555-5555-4555-8555-555555555555", WorkflowName: "original-flow", WorkflowUid: "66666666-6666-4666-8666-666666666666", PodName: "original-step", PodUid: "77777777-7777-4777-8777-777777777777"}, Step: test.step}
			runner, err := New(Config{TenantID: "11111111-1111-4111-8111-111111111111", Context: claim, TokenFile: token, WorkspaceDirectory: work}, modeldevv1.NewModelDevStepServiceClient(conn), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			finished := time.Now().UTC()
			report := &modeldevv1.ReportFinetuningStepResultRequest{Context: claim, StartedAt: timestamppb.New(finished.Add(-time.Second)), FinishedAt: timestamppb.New(finished), ExitCode: 0, ResultManifest: manifest}
			// The original computation has exited and its context is canceled. Its
			// one late report still has the bounded frozen I/O budget, without
			// acquiring another start permit or retrying the completed process.
			compute, cancel := context.WithCancel(context.Background())
			cancel()
			started := time.Now()
			err = runner.reportFinetuningResult(compute, cpup01.ComponentBudgets{RPCTimeoutSeconds: test.budget}, report)
			elapsed := time.Since(started)
			select {
			case <-server.done:
			case <-time.After(time.Second):
				t.Fatal("original report handler did not finish")
			}
			server.mu.Lock()
			defer server.mu.Unlock()
			if server.calls != 1 || !proto.Equal(server.report, report) {
				t.Fatalf("original result changed or was retried: calls=%d report=%v", server.calls, server.report)
			}
			if got := server.metadata.Get("authorization"); len(got) != 1 || got[0] != "Bearer projected-result-token" {
				t.Fatalf("report lost workload token metadata: %v", got)
			}
			if got := server.metadata.Get("x-ani-tenant-id"); len(got) != 1 || got[0] != runner.config.TenantID {
				t.Fatalf("report lost tenant metadata: %v", got)
			}
			if server.deadline.IsZero() {
				t.Fatal("late report has no finite deadline")
			}
			if test.expired {
				if status.Code(err) != codes.DeadlineExceeded || server.acknowledged || elapsed > 3*time.Second {
					t.Fatalf("short frozen report budget was not enforced: err=%v acknowledged=%t elapsed=%s", err, server.acknowledged, elapsed)
				}
			} else if err != nil || !server.acknowledged {
				t.Fatalf("finished %s result rejected before ACK inside frozen %ds RPC budget: err=%v acknowledged=%t elapsed=%s", test.name, test.budget, err, server.acknowledged, elapsed)
			}
			t.Logf("one original result RPC; frozen_budget=%ds ack_delay=%s elapsed=%s acknowledged=%t", test.budget, server.delay, elapsed, server.acknowledged)
		})
	}
}
