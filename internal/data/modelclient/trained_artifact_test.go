package modelclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	modelv1 "github.com/zhangzhe-ctrl/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Only the external Model API is a boundary substitute. Original request
// projection, official helper validation, metadata and budgets are real.
type artifactBoundary struct {
	modelv1.ModelServiceClient
	prepare  func(context.Context, *modelv1.PrepareTrainedArtifactRequest) (*modelv1.PrepareTrainedArtifactResponse, error)
	read     func(context.Context, *modelv1.GetTrainedArtifactRegistrationRequest) (*modelv1.GetTrainedArtifactRegistrationResponse, error)
	ticket   func(context.Context, *modelv1.GetTrainedArtifactUploadURLRequest) (*modelv1.GetTrainedArtifactUploadURLResponse, error)
	complete func(context.Context, *modelv1.CompleteTrainedArtifactRequest) (*modelv1.CompleteTrainedArtifactResponse, error)
	abort    func(context.Context, *modelv1.AbortTrainedArtifactRequest) (*modelv1.AbortTrainedArtifactResponse, error)
}

func (b *artifactBoundary) PrepareTrainedArtifact(c context.Context, r *modelv1.PrepareTrainedArtifactRequest, _ ...grpc.CallOption) (*modelv1.PrepareTrainedArtifactResponse, error) {
	return b.prepare(c, r)
}
func (b *artifactBoundary) GetTrainedArtifactRegistration(c context.Context, r *modelv1.GetTrainedArtifactRegistrationRequest, _ ...grpc.CallOption) (*modelv1.GetTrainedArtifactRegistrationResponse, error) {
	return b.read(c, r)
}
func (b *artifactBoundary) GetTrainedArtifactUploadURL(c context.Context, r *modelv1.GetTrainedArtifactUploadURLRequest, _ ...grpc.CallOption) (*modelv1.GetTrainedArtifactUploadURLResponse, error) {
	return b.ticket(c, r)
}
func (b *artifactBoundary) CompleteTrainedArtifact(c context.Context, r *modelv1.CompleteTrainedArtifactRequest, _ ...grpc.CallOption) (*modelv1.CompleteTrainedArtifactResponse, error) {
	return b.complete(c, r)
}
func (b *artifactBoundary) AbortTrainedArtifact(c context.Context, r *modelv1.AbortTrainedArtifactRequest, _ ...grpc.CallOption) (*modelv1.AbortTrainedArtifactResponse, error) {
	return b.abort(c, r)
}

func artifactFixture() (biz.TrainedArtifactRequest, biz.TrainedArtifactSession, *modelv1.TrainedArtifactRegistration) {
	tenant := "11111111-1111-4111-8111-111111111111"
	exec := "22222222-2222-4222-8222-222222222222"
	name := "trained-original"
	base := "33333333-3333-4333-8333-333333333333"
	s := biz.TrainedArtifactSession{RegistrationID: "44444444-4444-4444-8444-444444444444", StoragePath: tenant + "/trained-artifacts/original/model.tar"}
	input := biz.TrainedArtifactRequest{TenantID: tenant, ExecutionID: exec, Registration: biz.FinetuningRegistration{ContainerKey: "md-container:" + exec, VersionKey: "md-" + exec, VersionTag: "exec-" + exec, NewModelName: &name, BaseModelVersionID: base, Bundle: cpup01.FixedObjectRef{SizeBytes: 2048, SHA256: strings.Repeat("a", 64)}}}
	r := &modelv1.TrainedArtifactRegistration{RegistrationId: s.RegistrationID, IdempotencyKey: "md-" + exec, ExecutionId: exec, ModelName: name, DisplayName: name, Description: "ModelDev execution " + exec, Capabilities: []string{"text-generation"}, SourceModelVersionId: base, Version: "exec-" + exec, FileName: "model.tar", Format: "safetensors", SizeBytes: 2048, ChecksumSha256: strings.Repeat("a", 64), StoragePath: s.StoragePath, Status: "pending", EngineType: "vllm", EngineImage: "registry.example/engine:v1", StartupCommand: "vllm serve /models"}
	return input, s, r
}

func TestTrainedArtifactAdapterUsesOriginalFiniteProtocolOncePerOperation(t *testing.T) {
	in, session, stored := artifactFixture()
	prepareCalls, reads, tickets, completes := 0, 0, 0, 0
	boundary := &artifactBoundary{}
	boundary.prepare = func(ctx context.Context, r *modelv1.PrepareTrainedArtifactRequest) (*modelv1.PrepareTrainedArtifactResponse, error) {
		prepareCalls++
		if r.TenantId != in.TenantID || r.ExecutionId != in.ExecutionID || r.IdempotencyKey != stored.IdempotencyKey || r.SourceModelVersionId != stored.SourceModelVersionId || r.Version != stored.Version || r.SizeBytes != stored.SizeBytes || r.ChecksumSha256 != stored.ChecksumSha256 || r.FileName != "model.tar" || r.Format != "safetensors" || r.ModelName != stored.ModelName || r.DisplayName != stored.DisplayName || r.Description != stored.Description || len(r.Capabilities) != 1 || r.Capabilities[0] != "text-generation" || r.EngineType != "" || r.EngineImage != "" || r.StartupCommand != "" {
			t.Fatal("original intent changed or serving runtime invented")
		}
		md, _ := metadata.FromOutgoingContext(ctx)
		if len(md.Get("authorization")) != 0 || len(md.Get("cookie")) != 0 || len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != in.TenantID || md.Get("x-ani-actor")[0] != "modeldev:service" {
			t.Fatal("untrusted inbound metadata survived")
		}
		return &modelv1.PrepareTrainedArtifactResponse{RegistrationId: session.RegistrationID, StoragePath: session.StoragePath}, nil
	}
	boundary.read = func(_ context.Context, r *modelv1.GetTrainedArtifactRegistrationRequest) (*modelv1.GetTrainedArtifactRegistrationResponse, error) {
		reads++
		if r.IdempotencyKey != stored.IdempotencyKey || r.ExpectedExecutionId != in.ExecutionID || !r.VerifyUploadedObject {
			t.Fatal("read escaped original key or omitted object proof")
		}
		observation := &modelv1.TrainedArtifactObjectObservation{State: modelv1.TrainedArtifactObjectObservation_STATE_MISSING}
		if stored.Status == "ready" {
			observation = &modelv1.TrainedArtifactObjectObservation{State: modelv1.TrainedArtifactObjectObservation_STATE_VERIFIED, SizeBytes: stored.SizeBytes, Sha256: stored.ChecksumSha256}
		}
		return &modelv1.GetTrainedArtifactRegistrationResponse{Registration: proto.Clone(stored).(*modelv1.TrainedArtifactRegistration), UploadedObject: observation}, nil
	}
	boundary.ticket = func(_ context.Context, r *modelv1.GetTrainedArtifactUploadURLRequest) (*modelv1.GetTrainedArtifactUploadURLResponse, error) {
		tickets++
		if r.RegistrationId != session.RegistrationID || r.ExpectedStoragePath != session.StoragePath || r.IdempotencyKey != stored.IdempotencyKey || r.ExpectedExecutionId != in.ExecutionID {
			t.Fatal("ticket changed original session")
		}
		return &modelv1.GetTrainedArtifactUploadURLResponse{RegistrationId: session.RegistrationID, StoragePath: session.StoragePath, UploadUrl: "https://storage.example.test/original?signature=ephemeral", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}, nil
	}
	boundary.complete = func(_ context.Context, r *modelv1.CompleteTrainedArtifactRequest) (*modelv1.CompleteTrainedArtifactResponse, error) {
		completes++
		if r.RegistrationId != session.RegistrationID || r.IdempotencyKey != stored.IdempotencyKey {
			t.Fatal("complete changed original key")
		}
		return &modelv1.CompleteTrainedArtifactResponse{RegistrationId: session.RegistrationID, StoragePath: session.StoragePath, ModelId: "55555555-5555-4555-8555-555555555555", ModelVersionId: "66666666-6666-4666-8666-666666666666", ModelVersionStatus: "ready", SizeBytes: stored.SizeBytes, ChecksumSha256: stored.ChecksumSha256}, nil
	}
	client := &Client{client: boundary, actor: "modeldev:service", timeout: time.Hour}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "borrowed", "cookie", "session", "x-ani-tenant-id", "foreign"))
	got, err := client.PrepareArtifact(ctx, in)
	if err != nil || got != session || prepareCalls != 1 || reads != 0 {
		t.Fatal("prepare retried or performed hidden read", err)
	}
	pending, err := client.ReadArtifact(ctx, in)
	if err != nil || pending.State != "pending" || pending.ObjectVerified || pending.Receipt != nil {
		t.Fatal("missing object became READY", err)
	}
	if _, err := client.ArtifactUploadGrant(ctx, in, session); err != nil {
		t.Fatal(err)
	}
	receipt, err := client.CompleteArtifact(ctx, in, session)
	if err != nil || reads != 1 || completes != 1 {
		t.Fatal("complete hid retries/readback", err)
	}
	stored.Status, stored.ModelId, stored.ModelVersionId = "ready", receipt.ModelID, receipt.ModelVersionID
	ready, err := client.ReadArtifact(ctx, in)
	if err != nil || !ready.ObjectVerified || ready.Receipt == nil || *ready.Receipt != receipt {
		t.Fatal("original complete not independently readable", err)
	}
	if prepareCalls != 1 || reads != 2 || tickets != 1 || completes != 1 {
		t.Fatal("provider calls were repeated")
	}
}

func TestTrainedArtifactReadRejectsChangedOriginalSemantics(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*modelv1.TrainedArtifactRegistration)
	}{
		{"target", func(r *modelv1.TrainedArtifactRegistration) { r.TargetModelId = "77777777-7777-4777-8777-777777777777" }},
		{"name", func(r *modelv1.TrainedArtifactRegistration) { r.ModelName = "another" }},
		{"display", func(r *modelv1.TrainedArtifactRegistration) { r.DisplayName = "another" }},
		{"description", func(r *modelv1.TrainedArtifactRegistration) { r.Description = "another" }},
		{"capabilities", func(r *modelv1.TrainedArtifactRegistration) { r.Capabilities = []string{"image-generation"} }},
		{"base", func(r *modelv1.TrainedArtifactRegistration) {
			r.SourceModelVersionId = "77777777-7777-4777-8777-777777777777"
		}},
		{"tag", func(r *modelv1.TrainedArtifactRegistration) { r.Version = "another" }},
		{"bytes", func(r *modelv1.TrainedArtifactRegistration) { r.SizeBytes++ }},
		{"hash", func(r *modelv1.TrainedArtifactRegistration) { r.ChecksumSha256 = strings.Repeat("b", 64) }},
		{"key", func(r *modelv1.TrainedArtifactRegistration) { r.IdempotencyKey = "another" }},
		{"execution", func(r *modelv1.TrainedArtifactRegistration) { r.ExecutionId = "77777777-7777-4777-8777-777777777777" }},
		{"tenant path", func(r *modelv1.TrainedArtifactRegistration) { r.StoragePath = "other/registration/model.tar" }},
		{"absent inherited runtime", func(r *modelv1.TrainedArtifactRegistration) { r.EngineImage = "" }},
		{"ready without original object", func(r *modelv1.TrainedArtifactRegistration) {
			r.Status = "ready"
			r.ModelId = "55555555-5555-4555-8555-555555555555"
			r.ModelVersionId = "66666666-6666-4666-8666-666666666666"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			in, _, r := artifactFixture()
			test.change(r)
			calls := 0
			b := &artifactBoundary{read: func(context.Context, *modelv1.GetTrainedArtifactRegistrationRequest) (*modelv1.GetTrainedArtifactRegistrationResponse, error) {
				calls++
				return &modelv1.GetTrainedArtifactRegistrationResponse{Registration: r, UploadedObject: &modelv1.TrainedArtifactObjectObservation{State: modelv1.TrainedArtifactObjectObservation_STATE_MISSING}}, nil
			}}
			_, err := (&Client{client: b, actor: "modeldev:service", timeout: time.Hour}).ReadArtifact(context.Background(), in)
			if !errors.Is(err, biz.ErrModelDependencyUnavailable) || calls != 1 {
				t.Fatal("changed original session accepted", err)
			}
		})
	}
}

func TestTrainedArtifactMissAndUnknownWriteNeverRetryOrLeakTicket(t *testing.T) {
	in, _, _ := artifactFixture()
	for _, code := range []codes.Code{codes.NotFound, codes.Unavailable} {
		calls := 0
		b := &artifactBoundary{read: func(context.Context, *modelv1.GetTrainedArtifactRegistrationRequest) (*modelv1.GetTrainedArtifactRegistrationResponse, error) {
			calls++
			return nil, status.Error(code, "https://storage.example/?secret=must-not-escape")
		}}
		_, err := (&Client{client: b, actor: "modeldev:service", timeout: time.Hour}).ReadArtifact(context.Background(), in)
		if !errors.Is(err, biz.ErrModelDependencyUnavailable) || strings.Contains(err.Error(), "secret") || calls != 1 {
			t.Fatal("miss/unknown retried or exposed signed storage data")
		}
	}
	calls := 0
	b := &artifactBoundary{prepare: func(context.Context, *modelv1.PrepareTrainedArtifactRequest) (*modelv1.PrepareTrainedArtifactResponse, error) {
		calls++
		return nil, status.Error(codes.Unavailable, "https://storage.example/?secret=must-not-escape")
	}}
	_, err := (&Client{client: b, actor: "modeldev:service", timeout: time.Hour}).PrepareArtifact(context.Background(), in)
	if !errors.Is(err, biz.ErrModelDependencyUnavailable) || strings.Contains(err.Error(), "secret") || calls != 1 {
		t.Fatal("unknown original Prepare resent or leaked ticket")
	}
}

func TestAbortArtifactRemainsOneOriginalSessionWrite(t *testing.T) {
	in, session, _ := artifactFixture()
	calls := 0
	b := &artifactBoundary{abort: func(_ context.Context, r *modelv1.AbortTrainedArtifactRequest) (*modelv1.AbortTrainedArtifactResponse, error) {
		calls++
		if r.TenantId != in.TenantID || r.RegistrationId != session.RegistrationID {
			t.Fatal("abort changed original registration")
		}
		return &modelv1.AbortTrainedArtifactResponse{}, nil
	}}
	if err := (&Client{client: b, actor: "modeldev:service", timeout: time.Hour}).AbortArtifact(context.Background(), in, session); err != nil || calls != 1 {
		t.Fatal("abort retried or changed session", err)
	}
}

func TestModelArtifactRPCConsumesOriginalParentDeadline(t *testing.T) {
	in, _, _ := artifactFixture()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	b := &artifactBoundary{read: func(ctx context.Context, _ *modelv1.GetTrainedArtifactRegistrationRequest) (*modelv1.GetTrainedArtifactRegistrationResponse, error) {
		got, ok := ctx.Deadline()
		if !ok || !got.Equal(want) {
			t.Fatal("configured 3600s renewed original caller budget")
		}
		return nil, status.Error(codes.DeadlineExceeded, "bounded")
	}}
	_, err := (&Client{client: b, actor: "modeldev:service", timeout: time.Hour}).ReadArtifact(ctx, in)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("parent deadline lost", err)
	}
}
