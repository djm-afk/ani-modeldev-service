package resolutiontest

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	conf "github.com/zhangzhe-ctrl/ani-modeldev-service/internal/conf/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/execution"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/server"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/durationpb"
	"testing"
	"time"
)

func TestTwoPhaseRPCPreservesCandidateReadyInputAndFullPlanOverMTLS(t *testing.T) {
	f := prepareResolution(t)
	client := startAdmissionClient(t, f, f.resolver)
	request := admissionRPCRequest(t, f)
	response, e := client.PrepareAdmission(admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID), request)
	if e != nil {
		t.Fatal(e)
	}
	candidate, e := cpup01.DecodePreparedCandidate(response.CandidateCanonical, response.CandidateHash)
	if e != nil {
		t.Fatal(e)
	}
	before := f.factReader.reads
	finalized, e := client.FinalizeAdmission(admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID), &modeldevv1.FinalizeAdmissionRequest{CandidateCanonical: response.CandidateCanonical, CandidateHash: response.CandidateHash})
	if e != nil {
		t.Fatal(e)
	}
	s, e := contractpb.DecodeSnapshot(finalized.Snapshot)
	if e != nil {
		t.Fatal(e)
	}
	hash, _ := s.Digest()
	if hash != finalized.ExecutionSpecHash || s.IdentityRef != candidate.Snapshot.IdentityRef || s.Resources != candidate.Snapshot.Resources || f.factReader.reads != before {
		t.Fatal("wire finalize changed frozen facts")
	}
	second, e := client.FinalizeAdmission(admissionRPCContext(f.ctx, f.request.IdentityRef.TenantID), &modeldevv1.FinalizeAdmissionRequest{CandidateCanonical: response.CandidateCanonical, CandidateHash: response.CandidateHash})
	if e != nil || second.ExecutionSpecHash != finalized.ExecutionSpecHash {
		t.Fatal("wire retry changed spec")
	}
	raw, _ := s.Canonical()
	if bytes.Contains(raw, []byte(`"release"`)) {
		t.Fatal("retired snapshot contract escaped")
	}
	requireNoResolvedExecution(t, f)
}
func startAdmissionClient(t *testing.T, fixture resolutionFixture, resolver *biz.AdmissionResolver) modeldevv1.ModelDevAdmissionServiceClient {
	t.Helper()
	return modeldevv1.NewModelDevAdmissionServiceClient(startAdmissionConnection(t, fixture, service.NewAdmission(resolver)))
}

func startAdmissionConnection(t *testing.T, fixture resolutionFixture, admission modeldevv1.ModelDevAdmissionServiceServer) *grpc.ClientConn {
	t.Helper()
	certificates := commandtls.New(t)
	s, err := server.NewGovernanceCommandServer(
		&conf.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(5 * time.Second)},
		server.CommandTLS{Certificate: certificates.Server, ClientCAs: certificates.Roots, GovernanceDNSName: commandtls.GovernanceDNSName},
		service.NewCommand(execution.New(fixture.openPool())), admission,
	)
	if err != nil {
		t.Fatalf("ADMISSION_RPC_PREFLIGHT: TLS server construction failed; behavior NOT_RUN: %v", err)
	}
	endpoint, err := s.Endpoint()
	if err != nil {
		t.Fatalf("ADMISSION_RPC_PREFLIGHT: listener creation failed; behavior NOT_RUN: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Stop(ctx); err != nil {
			t.Errorf("stop admission server: %v", err)
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				t.Errorf("admission server exited: %v", err)
			}
		case <-ctx.Done():
			t.Error("admission server did not stop")
		}
	})
	connection, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: certificates.Roots, ServerName: commandtls.ServerDNSName,
		Certificates: []tls.Certificate{certificates.Governance},
	})))
	if err != nil {
		t.Fatalf("ADMISSION_RPC_PREFLIGHT: client construction failed; behavior NOT_RUN: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(fixture.ctx, 5*time.Second)
	defer cancel()
	connection.Connect()
	for state := connection.GetState(); state != connectivity.Ready; state = connection.GetState() {
		if !connection.WaitForStateChange(ctx, state) {
			t.Fatalf("ADMISSION_RPC_PREFLIGHT: mTLS channel did not become ready; behavior NOT_RUN: %v", ctx.Err())
		}
	}
	return connection
}
