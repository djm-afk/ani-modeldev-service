package modelclient

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"sort"
	"strings"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v3/errors"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type modelFactsTLSBoundary struct {
	modelv1.UnimplementedModelServiceServer
	t        *testing.T
	tenant   string
	response *modelv1.ResolveFinetuningFactsResponse
}

func (b *modelFactsTLSBoundary) ResolveFinetuningFacts(ctx context.Context, in *modelv1.ResolveFinetuningFactsRequest) (*modelv1.ResolveFinetuningFactsResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if in.TenantId != b.tenant || md.Get("x-ani-tenant-id")[0] != b.tenant || len(md.Get("authorization")) != 0 {
		b.t.Error("source read retained forged metadata")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	return b.response, nil
}
func (b *modelFactsTLSBoundary) GetModel(ctx context.Context, _ *modelv1.GetModelRequest) (*modelv1.Model, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(200 * time.Millisecond):
		return &modelv1.Model{}, nil
	}
}
func TestModelFactsReadUsesIndependentBudgetAndReceivesMoreThanFourMiB(t *testing.T) {
	config, certs := modelClientTestSetup(t, nil)
	config.Timeout = 50 * time.Millisecond
	config.ResolutionTimeout = 2 * time.Second
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tenant := "11111111-1111-4111-8111-111111111111"
	version := "22222222-2222-4222-8222-222222222222"
	raw := []byte(`{"model_type":"llama","padding":"` + strings.Repeat("a", 4<<20) + `"}`)
	tokenizer := []byte(`{"model_max_length":8192}`)
	response := &modelv1.ResolveFinetuningFactsResponse{TenantId: tenant, ModelId: "33333333-3333-4333-8333-333333333333", ModelVersionId: version, Format: "safetensors", Bucket: "models", ObjectKey: tenant + "/model.tar", ObjectVersionId: "fixed-version", SizeBytes: 8 << 20, Sha256: strings.Repeat("a", 64), ConfigJson: raw, TokenizerConfigJson: tokenizer, VerificationSource: modelv1.FinetuningVerificationSource}
	for name, body := range map[string][]byte{"config.json": raw, "tokenizer_config.json": tokenizer, "model.safetensors": []byte("actual")} {
		h := sha256.Sum256(body)
		response.Files = append(response.Files, &modelv1.FinetuningArtifactFile{Path: name, SizeBytes: int64(len(body)), Sha256: hex.EncodeToString(h[:])})
	}
	sort.Slice(response.Files, func(i, j int) bool { return response.Files[i].Path < response.Files[j].Path })
	server := grpc.NewServer(grpc.MaxRecvMsgSize(modelv1.MaxFinetuningControlRPCBytes), grpc.MaxSendMsgSize(modelv1.MaxFinetuningControlRPCBytes), grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certs.Server}, ClientCAs: certs.Roots, ClientAuth: tls.RequireAndVerifyClientCert})))
	modelv1.RegisterModelServiceServer(server, &modelFactsTLSBoundary{t: t, tenant: tenant, response: response})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	config.Endpoint = listener.Addr().String()
	client, closeClient, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeClient)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "forged", "x-ani-tenant-id", "forged"))
	out, err := client.ResolveFinetuningFacts(ctx, tenant, &modelv1.ResolveFinetuningFactsRequest{TenantId: "forged", BaseModelVersionId: version})
	if err != nil || len(out.ConfigJson) <= 4<<20 {
		t.Fatal("real source RPC kept short timeout or4MiB ceiling", err)
	}
	if _, err := client.GetModel(context.Background(), tenant, &modelv1.GetModelRequest{TenantId: tenant}); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal("source budget expanded unrelated catalogue RPC", err)
	}
	parent, stop := context.WithCancel(context.Background())
	stop()
	if _, err := client.ResolveFinetuningFacts(parent, tenant, &modelv1.ResolveFinetuningFactsRequest{BaseModelVersionId: version}); !errors.Is(err, context.Canceled) {
		t.Fatal("source read lost parent cancellation", err)
	}
}
func TestModelSourceErrorsDoNotBecomeUnknownRegistrationWrites(t *testing.T) {
	for _, tc := range []struct{ err, want error }{
		{status.Error(codes.NotFound, "private signed location"), biz.ErrModelSourceNotFound},
		{status.Error(codes.AlreadyExists, "private signed location"), biz.ErrModelTargetConflict},
		{status.Error(codes.PermissionDenied, "private signed location"), biz.ErrModelSourceForbidden},
		{status.Error(codes.DeadlineExceeded, "private signed location"), context.DeadlineExceeded},
		{kratoserrors.New(422, "BASE_MODEL_INVALID", "private signed location"), biz.ErrBaseModelInvalid},
		{kratoserrors.New(409, "MODEL_NOT_READY", "private signed location"), biz.ErrBaseModelInvalid},
		{status.Error(codes.Unavailable, "private signed location"), biz.ErrModelDependencyUnavailable},
	} {
		got := sourceRPCError(tc.err)
		if !errors.Is(got, tc.want) || strings.Contains(got.Error(), "private") || strings.Contains(got.Error(), "REGISTRATION") {
			t.Fatalf("incorrect source error: %v", got)
		}
	}
}
