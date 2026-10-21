package modelclient

import (
	"context"
	"time"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TrainedArtifactRegistration is the Model transport adapter for the
// prepare/complete/abort registration session. It owns only wire mapping and
// caller identity: the durable dispatch arbitration still decides whether any
// write is permitted, and this adapter never retries on its own.
var _ biz.TrainedArtifactRegistration = (*Client)(nil)

// PrepareTrainedArtifact opens or replays one registration session. The request
// is built entirely from the frozen intent the caller supplies; nothing here
// selects a target, version or object.
//
// The provider's prepare response carries no size or digest echo, so the
// returned ticket is bound to the declared (request) bytes the session was
// opened for. That is not a weaker gate: Model re-verifies the landed object
// against the session's stored size and SHA256 at Complete, and the READY check
// compares the settled receipt to the same frozen bundle.
func (c *Client) PrepareTrainedArtifact(ctx context.Context, tenant string, prepare biz.TrainedArtifactPrepare) (biz.TrainedArtifactTicket, error) {
	request := &modelv1.PrepareTrainedArtifactRequest{
		IdempotencyKey:       prepare.IdempotencyKey,
		TargetModelId:        prepare.TargetModelID,
		ModelName:            prepare.ModelName,
		DisplayName:          prepare.DisplayName,
		Description:          prepare.Description,
		Capabilities:         prepare.Capabilities,
		SourceModelVersionId: prepare.SourceModelVersionID,
		Version:              prepare.Version,
		FileName:             prepare.FileName,
		Format:               prepare.Format,
		SizeBytes:            prepare.SizeBytes,
		ChecksumSha256:       prepare.ChecksumSHA256,
	}
	response, err := invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.PrepareTrainedArtifactRequest, opts ...grpc.CallOption) (*modelv1.PrepareTrainedArtifactResponse, error) {
		in.TenantId = tenant
		return c.client.PrepareTrainedArtifact(ctx, in, opts...)
	})
	if err != nil {
		return biz.TrainedArtifactTicket{}, err
	}
	if response.GetRegistrationId() == "" || response.GetUploadUrl() == "" || response.GetStoragePath() == "" {
		return biz.TrainedArtifactTicket{}, statusUnavailable("MODEL_PREPARE_INCOMPLETE")
	}
	headers := response.GetUploadHeaders()
	if len(headers) == 0 {
		headers = nil
	}
	return biz.TrainedArtifactTicket{
		RegistrationID: response.GetRegistrationId(),
		Upload: biz.UploadTicket{
			URL:         response.GetUploadUrl(),
			StoragePath: response.GetStoragePath(),
			Headers:     headers,
			SizeBytes:   prepare.SizeBytes,
			SHA256:      prepare.ChecksumSHA256,
			ExpiresAt:   expiresAt(response.GetExpiresAt()),
		},
	}, nil
}

// CompleteTrainedArtifact finalises the session after the object landed. The
// provider replays the same receipt for a repeated call under the original
// session id, so an unresolved attempt is recovered without a second session.
func (c *Client) CompleteTrainedArtifact(ctx context.Context, tenant, registrationID, idempotencyKey string) (biz.TrainedArtifactReceipt, error) {
	if registrationID == "" {
		return biz.TrainedArtifactReceipt{}, statusUnavailable("MODEL_REGISTRATION_REQUIRED")
	}
	request := &modelv1.CompleteTrainedArtifactRequest{RegistrationId: registrationID, IdempotencyKey: idempotencyKey}
	response, err := invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.CompleteTrainedArtifactRequest, opts ...grpc.CallOption) (*modelv1.CompleteTrainedArtifactResponse, error) {
		in.TenantId = tenant
		return c.client.CompleteTrainedArtifact(ctx, in, opts...)
	})
	if err != nil {
		return biz.TrainedArtifactReceipt{}, err
	}
	return biz.TrainedArtifactReceipt{
		RegistrationID:     response.GetRegistrationId(),
		ModelID:            response.GetModelId(),
		ModelVersionID:     response.GetModelVersionId(),
		ModelVersionStatus: response.GetModelVersionStatus(),
		StoragePath:        response.GetStoragePath(),
		SizeBytes:          response.GetSizeBytes(),
		ChecksumSHA256:     response.GetChecksumSha256(),
	}, nil
}

// AbortTrainedArtifact terminates a session that will not be completed. The
// reason is bounded audit text and never a caller-supplied command.
func (c *Client) AbortTrainedArtifact(ctx context.Context, tenant, registrationID, reason string) error {
	if registrationID == "" {
		return statusUnavailable("MODEL_REGISTRATION_REQUIRED")
	}
	request := &modelv1.AbortTrainedArtifactRequest{RegistrationId: registrationID, Reason: reason}
	_, err := invoke(c, ctx, tenant, request, func(ctx context.Context, in *modelv1.AbortTrainedArtifactRequest, opts ...grpc.CallOption) (*modelv1.AbortTrainedArtifactResponse, error) {
		in.TenantId = tenant
		return c.client.AbortTrainedArtifact(ctx, in, opts...)
	})
	return err
}

// statusUnavailable reports a provider response that is structurally unusable.
// It is a transport-level observation, not a business failure: the caller
// records it as an unresolved attempt rather than inventing a result.
func statusUnavailable(message string) error {
	return status.Error(codes.Unavailable, message)
}

// expiresAt converts the provider ticket expiry. A missing timestamp yields the
// zero time, which the orchestrator treats as an unusable ticket rather than an
// unbounded one.
func expiresAt(value *timestamppb.Timestamp) time.Time {
	if value == nil || !value.IsValid() {
		return time.Time{}
	}
	return value.AsTime()
}
