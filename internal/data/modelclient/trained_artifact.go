package modelclient

import (
	"context"
	"encoding/hex"
	"errors"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var _ biz.TrainedArtifactRegistry = (*Client)(nil)

// Every operation makes exactly one provider request. Only the durable
// execution owner may grant a write permit; this adapter never retries one.
func (c *Client) PrepareArtifact(ctx context.Context, original biz.TrainedArtifactRequest) (biz.TrainedArtifactSession, error) {
	if c == nil || c.client == nil {
		return biz.TrainedArtifactSession{}, biz.ErrModelDependencyUnavailable
	}
	in, err := artifactRequest(original)
	if err != nil {
		return biz.TrainedArtifactSession{}, err
	}
	out, err := invoke(c, ctx, original.TenantID, in, c.client.PrepareTrainedArtifact)
	if err != nil {
		return biz.TrainedArtifactSession{}, artifactRPCError(err)
	}
	if out == nil || len(out.ProtoReflect().GetUnknown()) != 0 {
		return biz.TrainedArtifactSession{}, biz.ErrModelDependencyUnavailable
	}
	session := biz.TrainedArtifactSession{RegistrationID: out.RegistrationId, StoragePath: out.StoragePath}
	if !artifactSession(original, session) {
		return biz.TrainedArtifactSession{}, biz.ErrModelDependencyUnavailable
	}
	// Ignore this response's ephemeral URL. The owner first reads the original
	// session, then requests a fresh original-path ticket for controlled copy.
	return session, nil
}

func (c *Client) ReadArtifact(ctx context.Context, original biz.TrainedArtifactRequest) (biz.TrainedArtifactObservation, error) {
	if c == nil || c.client == nil {
		return biz.TrainedArtifactObservation{}, biz.ErrModelDependencyUnavailable
	}
	expected, err := artifactRequest(original)
	if err != nil {
		return biz.TrainedArtifactObservation{}, err
	}
	in := &modelv1.GetTrainedArtifactRegistrationRequest{TenantId: original.TenantID, IdempotencyKey: expected.IdempotencyKey, ExpectedExecutionId: original.ExecutionID, VerifyUploadedObject: true}
	out, err := invoke(c, ctx, original.TenantID, in, c.client.GetTrainedArtifactRegistration)
	if err != nil {
		return biz.TrainedArtifactObservation{}, artifactRPCError(err)
	}
	if out.ValidateFor(in) != nil || !originalArtifactMatches(expected, out.GetRegistration()) {
		return biz.TrainedArtifactObservation{}, biz.ErrModelDependencyUnavailable
	}
	r := out.Registration
	session := biz.TrainedArtifactSession{RegistrationID: r.RegistrationId, StoragePath: r.StoragePath}
	if !artifactSession(original, session) {
		return biz.TrainedArtifactObservation{}, biz.ErrModelDependencyUnavailable
	}
	observation := biz.TrainedArtifactObservation{Session: session, State: r.Status, ObjectVerified: out.UploadedObject.State == modelv1.TrainedArtifactObjectObservation_STATE_VERIFIED}
	if r.Status == "ready" {
		// A provider READY row without a readable original object does not
		// constitute the verified receipt this execution may publish as READY.
		if !observation.ObjectVerified {
			return biz.TrainedArtifactObservation{}, biz.ErrModelDependencyUnavailable
		}
		receipt := biz.FinetuningModelReceipt{RegistrationID: r.RegistrationId, ModelID: r.ModelId, ModelVersionID: r.ModelVersionId, StoragePath: r.StoragePath, SizeBytes: r.SizeBytes, SHA256: r.ChecksumSha256}
		if !artifactReceipt(original, session, receipt) {
			return biz.TrainedArtifactObservation{}, biz.ErrModelDependencyUnavailable
		}
		observation.Receipt = &receipt
	}
	return observation, nil
}

func (c *Client) ArtifactUploadGrant(ctx context.Context, original biz.TrainedArtifactRequest, session biz.TrainedArtifactSession) (biz.TrainedArtifactUploadGrant, error) {
	if c == nil || c.client == nil {
		return biz.TrainedArtifactUploadGrant{}, biz.ErrModelDependencyUnavailable
	}
	expected, err := artifactRequest(original)
	if err != nil || !artifactSession(original, session) {
		return biz.TrainedArtifactUploadGrant{}, biz.ErrModelDependencyUnavailable
	}
	in := &modelv1.GetTrainedArtifactUploadURLRequest{TenantId: original.TenantID, RegistrationId: session.RegistrationID, IdempotencyKey: expected.IdempotencyKey, ExpectedExecutionId: original.ExecutionID, ExpectedStoragePath: session.StoragePath}
	out, err := invoke(c, ctx, original.TenantID, in, c.client.GetTrainedArtifactUploadURL)
	if err != nil {
		return biz.TrainedArtifactUploadGrant{}, artifactRPCError(err)
	}
	if out == nil || len(out.ProtoReflect().GetUnknown()) != 0 || out.RegistrationId != session.RegistrationID || out.StoragePath != session.StoragePath || out.ExpiresAt == nil || out.ExpiresAt.CheckValid() != nil {
		return biz.TrainedArtifactUploadGrant{}, biz.ErrModelDependencyUnavailable
	}
	now := time.Now()
	if !out.ExpiresAt.AsTime().After(now) || out.ExpiresAt.AsTime().After(now.Add(24*time.Hour)) || !artifactUploadURL(out.UploadUrl) {
		return biz.TrainedArtifactUploadGrant{}, biz.ErrModelDependencyUnavailable
	}
	headers := make(map[string]string, len(out.UploadHeaders))
	for k, v := range out.UploadHeaders {
		if k == "" || len(k) > 256 || len(v) > 8192 || strings.ContainsAny(k, " \t\r\n:\x00") || strings.ContainsAny(v, "\r\n\x00") {
			return biz.TrainedArtifactUploadGrant{}, biz.ErrModelDependencyUnavailable
		}
		headers[k] = v
	}
	return biz.TrainedArtifactUploadGrant{Session: session, URL: out.UploadUrl, Headers: headers, ExpiresAt: out.ExpiresAt.AsTime()}, nil
}

func (c *Client) CompleteArtifact(ctx context.Context, original biz.TrainedArtifactRequest, session biz.TrainedArtifactSession) (biz.FinetuningModelReceipt, error) {
	if c == nil || c.client == nil {
		return biz.FinetuningModelReceipt{}, biz.ErrModelDependencyUnavailable
	}
	expected, err := artifactRequest(original)
	if err != nil || !artifactSession(original, session) {
		return biz.FinetuningModelReceipt{}, biz.ErrModelDependencyUnavailable
	}
	in := &modelv1.CompleteTrainedArtifactRequest{TenantId: original.TenantID, RegistrationId: session.RegistrationID, IdempotencyKey: expected.IdempotencyKey}
	out, err := invoke(c, ctx, original.TenantID, in, c.client.CompleteTrainedArtifact)
	if err != nil {
		return biz.FinetuningModelReceipt{}, artifactRPCError(err)
	}
	if out == nil || len(out.ProtoReflect().GetUnknown()) != 0 || out.ModelVersionStatus != "ready" {
		return biz.FinetuningModelReceipt{}, biz.ErrModelDependencyUnavailable
	}
	receipt := biz.FinetuningModelReceipt{RegistrationID: out.RegistrationId, ModelID: out.ModelId, ModelVersionID: out.ModelVersionId, StoragePath: out.StoragePath, SizeBytes: out.SizeBytes, SHA256: out.ChecksumSha256}
	if !artifactReceipt(original, session, receipt) {
		return biz.FinetuningModelReceipt{}, biz.ErrModelDependencyUnavailable
	}
	// Returning a typed response grants no READY transition. The owner must
	// next read the original key and verify the provider's durable result.
	return receipt, nil
}

// AbortArtifact is a single write against the original registration. It does
// not acquire a permit and must never be used to hide an unknown Complete.
func (c *Client) AbortArtifact(ctx context.Context, original biz.TrainedArtifactRequest, session biz.TrainedArtifactSession) error {
	if c == nil || c.client == nil {
		return biz.ErrModelDependencyUnavailable
	}
	if _, err := artifactRequest(original); err != nil || !artifactSession(original, session) {
		return biz.ErrModelDependencyUnavailable
	}
	in := &modelv1.AbortTrainedArtifactRequest{TenantId: original.TenantID, RegistrationId: session.RegistrationID, Reason: "ModelDev original registration canceled"}
	out, err := invoke(c, ctx, original.TenantID, in, c.client.AbortTrainedArtifact)
	if err != nil {
		return artifactRPCError(err)
	}
	if out == nil || len(out.ProtoReflect().GetUnknown()) != 0 {
		return biz.ErrModelDependencyUnavailable
	}
	return nil
}

func artifactRequest(original biz.TrainedArtifactRequest) (*modelv1.PrepareTrainedArtifactRequest, error) {
	r := original.Registration
	if !artifactID(original.TenantID) || !artifactID(original.ExecutionID) || !artifactID(r.BaseModelVersionID) || r.VersionKey != "md-"+original.ExecutionID || r.VersionTag != "exec-"+original.ExecutionID || (r.TargetModelID == nil) == (r.NewModelName == nil) || r.Bundle.SizeBytes <= 0 || !artifactSHA256(r.Bundle.SHA256) {
		return nil, biz.ErrModelDependencyUnavailable
	}
	in := &modelv1.PrepareTrainedArtifactRequest{TenantId: original.TenantID, ExecutionId: original.ExecutionID, IdempotencyKey: r.VersionKey, Version: r.VersionTag, SourceModelVersionId: r.BaseModelVersionID, FileName: "model.tar", Format: "safetensors", SizeBytes: r.Bundle.SizeBytes, ChecksumSha256: r.Bundle.SHA256, Description: "ModelDev execution " + original.ExecutionID, Capabilities: []string{"text-generation"}}
	if r.TargetModelID != nil {
		if !artifactID(*r.TargetModelID) {
			return nil, biz.ErrModelDependencyUnavailable
		}
		in.TargetModelId = *r.TargetModelID
	} else {
		if len(*r.NewModelName) == 0 || len(*r.NewModelName) > 63 || strings.Trim(*r.NewModelName, "abcdefghijklmnopqrstuvwxyz0123456789-.") != "" {
			return nil, biz.ErrModelDependencyUnavailable
		}
		in.ModelName, in.DisplayName = *r.NewModelName, *r.NewModelName
	}
	// An empty runtime triple deliberately selects the provider's existing
	// source-version inheritance. ModelDev cannot invent a serving runtime.
	return in, nil
}

func originalArtifactMatches(in *modelv1.PrepareTrainedArtifactRequest, r *modelv1.TrainedArtifactRegistration) bool {
	return r != nil && r.TargetModelId == in.TargetModelId && r.ModelName == in.ModelName && r.DisplayName == in.DisplayName && r.Description == in.Description && slices.Equal(r.Capabilities, in.Capabilities) && r.SourceModelVersionId == in.SourceModelVersionId && r.Version == in.Version && r.FileName == in.FileName && r.Format == in.Format && r.SizeBytes == in.SizeBytes && r.ChecksumSha256 == in.ChecksumSha256 && strings.TrimSpace(r.EngineType) != "" && strings.TrimSpace(r.EngineImage) != "" && strings.TrimSpace(r.StartupCommand) != "" && !strings.ContainsAny(r.StartupCommand, "\r\n")
}

func artifactReceipt(original biz.TrainedArtifactRequest, session biz.TrainedArtifactSession, receipt biz.FinetuningModelReceipt) bool {
	return artifactSession(original, session) && receipt.RegistrationID == session.RegistrationID && receipt.StoragePath == session.StoragePath && artifactID(receipt.ModelID) && artifactID(receipt.ModelVersionID) && receipt.SizeBytes == original.Registration.Bundle.SizeBytes && receipt.SHA256 == original.Registration.Bundle.SHA256 && (original.Registration.TargetModelID == nil || receipt.ModelID == *original.Registration.TargetModelID)
}

func artifactSession(original biz.TrainedArtifactRequest, session biz.TrainedArtifactSession) bool {
	p := session.StoragePath
	return artifactID(session.RegistrationID) && utf8.ValidString(p) && len(p) <= 1024 && path.Clean(p) == p && strings.HasPrefix(p, original.TenantID+"/") && !strings.ContainsAny(p, "\\\x00\r\n") && strings.IndexFunc(p, func(r rune) bool { return r < 32 || r == 127 }) < 0
}

func artifactID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
func artifactSHA256(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && strings.ToLower(value) == value
}
func artifactUploadURL(value string) bool {
	u, err := url.ParseRequestURI(value)
	return err == nil && len(value) <= 16384 && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && !strings.ContainsAny(value, "\r\n\x00")
}

func artifactRPCError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	code := status.Code(err)
	if code == codes.Canceled {
		return context.Canceled
	}
	if code == codes.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	if code != codes.NotFound && code != codes.AlreadyExists && code != codes.FailedPrecondition && code != codes.InvalidArgument {
		code = codes.Unavailable
	}
	// Upstream messages may contain signed storage locations. Only a finite
	// code survives this boundary; even NotFound never proves a write unsent.
	return errors.Join(biz.ErrModelDependencyUnavailable, status.Error(code, "MODEL_REGISTRATION_UNAVAILABLE"))
}
