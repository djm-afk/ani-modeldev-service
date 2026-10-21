package main

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/commandtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStepRefreshesScopedStorageSessionWithCurrentTokenBeforeExpiry(t *testing.T) {
	client, claim, tokenFile, peer := storageRPCFixture(t, "")
	provider, err := stepStorageCredentials(client, "11111111-2222-4333-8444-555555555555", tokenFile, claim)
	if err != nil {
		t.Fatal(err)
	}
	first, err := provider.Retrieve(context.Background())
	if err != nil || first.AccessKeyID != "synthetic-step-1" || first.SessionToken == "" || !first.CanExpire || peer.issued.Load() != 1 {
		t.Fatalf("obtain this execution's temporary session through TLS RPC: %v", err)
	}
	if _, err := provider.Retrieve(context.Background()); err != nil || peer.issued.Load() != 1 {
		t.Fatal("unexpired credentials should use the SDK's bounded cache", err)
	}
	if err := os.WriteFile(tokenFile, []byte("synthetic-rotated-workload"), 0600); err != nil {
		t.Fatal(err)
	}
	// The first synthetic session expires after 121 seconds. The configured
	// two-minute SDK expiry window therefore requests a real refresh in ~1s.
	wait := time.Until(first.Expires) + 50*time.Millisecond
	if wait < 0 || wait > 2*time.Second {
		t.Fatalf("SDK failed to apply the two-minute renewal window: %v", wait)
	}
	time.Sleep(wait)
	renewed, err := provider.Retrieve(context.Background())
	if err != nil || renewed.AccessKeyID != "synthetic-step-2" || peer.issued.Load() != 2 {
		t.Fatalf("renew with the same frozen execution and purpose: %v", err)
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if len(peer.tokens) != 2 || peer.tokens[0] != "Bearer synthetic-first-workload" || peer.tokens[1] != "Bearer synthetic-rotated-workload" {
		t.Fatalf("refresh did not re-read the projected current workload identity: %v", peer.tokens)
	}
}

func TestStepRejectsExpiredWrongExecutionAndBroadenedStorageSessions(t *testing.T) {
	for _, mode := range []string{"expired", "missing token", "wrong execution", "wrong step", "broader prefix", "wrong connection", "wrong bucket"} {
		t.Run(mode, func(t *testing.T) {
			client, claim, tokenFile, _ := storageRPCFixture(t, mode)
			provider, err := stepStorageCredentials(client, "11111111-2222-4333-8444-555555555555", tokenFile, claim)
			if err != nil {
				t.Fatal(err)
			}
			if credentials, err := provider.Retrieve(context.Background()); err == nil || credentials.AccessKeyID != "" {
				t.Fatal("unusable or broadened response became usable AWS credentials")
			}
		})
	}
}

type credentialRPCPeer struct {
	modeldevv1.UnimplementedModelDevStepServiceServer
	configuration *modeldevv1.GetExecutionConfigurationResponse
	claim         *modeldevv1.StepContext
	mode          string
	issued        atomic.Int32
	mu            sync.Mutex
	tokens        []string
}

func (peer *credentialRPCPeer) GetExecutionConfiguration(context.Context, *modeldevv1.GetExecutionConfigurationRequest) (*modeldevv1.GetExecutionConfigurationResponse, error) {
	return proto.Clone(peer.configuration).(*modeldevv1.GetExecutionConfigurationResponse), nil
}

func (peer *credentialRPCPeer) GetStorageCredentials(ctx context.Context, request *modeldevv1.GetStorageCredentialsRequest) (*modeldevv1.GetStorageCredentialsResponse, error) {
	n := peer.issued.Add(1)
	md, _ := metadata.FromIncomingContext(ctx)
	peer.mu.Lock()
	peer.tokens = append(peer.tokens, strings.Join(md.Get("authorization"), ","))
	peer.mu.Unlock()
	key := "synthetic-step-1"
	expiry := time.Now().Add(121 * time.Second)
	if n > 1 {
		key, expiry = "synthetic-step-2", time.Now().Add(15*time.Minute)
	}
	value := &modeldevv1.TemporaryStorageCredentials{AccessKeyId: key, SecretAccessKey: "synthetic-step-secret", SessionToken: "synthetic-step-session", ExpiresAt: timestamppb.New(expiry), StorageConnectionId: "artifact-store-v1", Bucket: "cpu-artifacts", Scope: &modeldevv1.TemporaryStorageCredentials_ObjectPrefix{ObjectPrefix: "tenant-fixed/executions/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee/"}}
	response := &modeldevv1.GetStorageCredentialsResponse{Identity: proto.Clone(peer.configuration.Identity).(*trainingv1.ExecutionIdentity), Step: request.Context.Step, Credentials: value}
	switch peer.mode {
	case "expired":
		value.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
	case "missing token":
		value.SessionToken = ""
	case "wrong execution":
		response.Identity.ExecutionId = "99999999-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	case "wrong step":
		response.Step = modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE
	case "broader prefix":
		value.Scope = &modeldevv1.TemporaryStorageCredentials_ObjectPrefix{ObjectPrefix: "tenant-fixed/executions/"}
	case "wrong connection":
		value.StorageConnectionId = "other-store"
	case "wrong bucket":
		value.Bucket = "other-artifacts"
	}
	return response, nil
}

func storageRPCFixture(t *testing.T, mode string) (modeldevv1.ModelDevStepServiceClient, *modeldevv1.StepContext, string, *credentialRPCPeer) {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	timing, err := (cpup01.TimingConfiguration{
		ExecutionTimeoutDefaultSeconds: snapshot.Timing.ExecutionTimeoutSeconds,
		ExecutionTimeoutMaxSeconds:     snapshot.Timing.ExecutionTimeoutMaxSeconds,
		StepTimeoutSeconds:             snapshot.Timing.StepTimeoutSeconds,
	}).Resolve(time.Now().UTC().Truncate(time.Microsecond), nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Timing, snapshot.DeadlineAt = timing, timing.ExecutionDeadlineAt
	digest, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := contractpb.EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	claim := &modeldevv1.StepContext{Identity: &trainingv1.ExecutionIdentity{ExecutionId: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", ExecutionSpecHash: digest}, Step: modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH,
		Association: &modeldevv1.RunAssociation{KfpRunId: "55555555-6666-4777-8888-999999999999", NamespaceName: snapshot.Environment.NamespaceName, NamespaceUid: snapshot.Environment.NamespaceUID, WorkflowName: "storage-flow", WorkflowUid: "aaaaaaaa-1111-4111-8111-bbbbbbbbbbbb", PodName: "storage-publish", PodUid: "cccccccc-1111-4111-8111-dddddddddddd"}}
	identity := proto.Clone(claim.Identity).(*trainingv1.ExecutionIdentity)
	identity.OperationId = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"
	peer := &credentialRPCPeer{configuration: &modeldevv1.GetExecutionConfigurationResponse{Identity: identity, Snapshot: wire, Authority: &modeldevv1.AuthorityBinding{KfpRunId: claim.Association.KfpRunId, NamespaceUid: claim.Association.NamespaceUid, WorkflowUid: claim.Association.WorkflowUid}}, claim: claim, mode: mode}
	certificates := commandtls.New(t)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificates.Server}})))
	modeldevv1.RegisterModelDevStepServiceServer(server, peer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: certificates.Roots, ServerName: commandtls.ServerDNSName})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	tokenFile := filepath.Join(t.TempDir(), "projected-workload-token")
	if err := os.WriteFile(tokenFile, []byte("synthetic-first-workload"), 0600); err != nil {
		t.Fatal(err)
	}
	return modeldevv1.NewModelDevStepServiceClient(connection), claim, tokenFile, peer
}
