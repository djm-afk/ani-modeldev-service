package service

import (
	"context"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (step *Step) GetStorageCredentials(ctx context.Context, request *modeldevv1.GetStorageCredentialsRequest) (*modeldevv1.GetStorageCredentialsResponse, error) {
	requested := request.GetContext().GetStep()
	purpose := ""
	switch requested {
	case modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE:
		purpose = "prepare"
		if request.GetReadBaseModel() {
			purpose = "prepare-base-model"
		}
	case modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH:
		if request.GetReadBaseModel() {
			return nil, runtimeError(biz.ErrManagedStepUnauthorized)
		}
		purpose = "publish"
	default:
		return nil, runtimeError(biz.ErrManagedStepUnauthorized)
	}
	token, claim, err := step.runtimeRequest(ctx, request, request.GetContext(), requested)
	if err != nil {
		return nil, err
	}
	result, err := step.runtime.StorageCredentials(ctx, token, claim, purpose, step.storage)
	if err != nil {
		return nil, runtimeError(err)
	}
	if _, _, err := runtimeReply(result.Runtime, claim); err != nil {
		return nil, err
	}
	value := result.Credentials
	wire := &modeldevv1.TemporaryStorageCredentials{
		AccessKeyId: value.AccessKeyID, SecretAccessKey: value.SecretAccessKey, SessionToken: value.SessionToken,
		ExpiresAt: timestamppb.New(value.ExpiresAt), StorageConnectionId: value.StorageConnectionID, Bucket: value.Bucket,
	}
	if purpose == "prepare" || purpose == "prepare-base-model" {
		wire.Scope = &modeldevv1.TemporaryStorageCredentials_ObjectKey{ObjectKey: value.ObjectKey}
	} else {
		wire.Scope = &modeldevv1.TemporaryStorageCredentials_ObjectPrefix{ObjectPrefix: value.ObjectPrefix}
	}
	return &modeldevv1.GetStorageCredentialsResponse{Identity: runtimeIdentity(result.Runtime.Execution), Step: requested, Credentials: wire}, nil
}
