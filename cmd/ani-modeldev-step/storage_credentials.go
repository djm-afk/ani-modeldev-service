package main

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/component"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// A separate provider is created for this process's one execution and purpose.
// AWS refresh invokes the same authenticated owner API before the session ends;
// no permanent RustFS credential is mounted in or returned to the component.
func stepStorageCredentials(client modeldevv1.ModelDevStepServiceClient, tenant, tokenFile string, claim *modeldevv1.StepContext, baseRead ...bool) (*aws.CredentialsCache, error) {
	if client == nil || !canonicalUUID(tenant) || claim == nil || claim.Identity == nil || claim.Association == nil ||
		(claim.Step != modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE && claim.Step != modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH) {
		return nil, component.ErrConfiguration
	}
	if len(baseRead) > 1 {
		return nil, component.ErrConfiguration
	}
	readBase := len(baseRead) == 1 && baseRead[0]
	if readBase && claim.Step != modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE {
		return nil, component.ErrConfiguration
	}
	frozen := proto.Clone(claim).(*modeldevv1.StepContext)
	provider := aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		if ctx == nil || ctx.Err() != nil {
			return aws.Credentials{}, component.ErrConfiguration
		}
		data, err := readOwnerFile(tokenFile, 16<<10)
		token := strings.TrimSpace(string(data))
		if err != nil || token == "" || len(token) > 16384 || strings.ContainsAny(token, " \t\r\n,") {
			return aws.Credentials{}, component.ErrConfiguration
		}
		call := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token, "x-ani-tenant-id", tenant))
		// This response supplies only the persisted frozen scope. It cannot
		// replace the fresh workload/Run/close verification on issuance below.
		configuration, err := client.GetExecutionConfiguration(call, &modeldevv1.GetExecutionConfigurationRequest{Context: frozen})
		if err != nil || configuration == nil || configuration.Identity == nil || configuration.Identity.ExecutionId != frozen.Identity.ExecutionId || configuration.Identity.ExecutionSpecHash != frozen.Identity.ExecutionSpecHash ||
			(frozen.Identity.OperationId != "" && configuration.Identity.OperationId != frozen.Identity.OperationId) || configuration.Authority == nil ||
			configuration.Authority.KfpRunId != frozen.Association.KfpRunId || configuration.Authority.NamespaceUid != frozen.Association.NamespaceUid || configuration.Authority.WorkflowUid != frozen.Association.WorkflowUid {
			return aws.Credentials{}, component.ErrConfiguration
		}
		snapshot, err := contractpb.DecodeSnapshot(configuration.Snapshot)
		if err != nil {
			return aws.Credentials{}, component.ErrConfiguration
		}
		digest, err := snapshot.Digest()
		if err != nil || digest != frozen.Identity.ExecutionSpecHash || snapshot.Environment.NamespaceName != frozen.Association.NamespaceName || snapshot.Environment.NamespaceUID != frozen.Association.NamespaceUid {
			return aws.Credentials{}, component.ErrConfiguration
		}
		response, err := client.GetStorageCredentials(call, &modeldevv1.GetStorageCredentialsRequest{Context: frozen, ReadBaseModel: readBase})
		if err != nil || response == nil || !proto.Equal(response.Identity, configuration.Identity) || response.Step != frozen.Step {
			return aws.Credentials{}, component.ErrConfiguration
		}
		value := response.Credentials
		if value == nil || len(value.ProtoReflect().GetUnknown()) != 0 || value.ExpiresAt == nil || value.ExpiresAt.CheckValid() != nil || !value.ExpiresAt.AsTime().After(time.Now().Add(2*time.Minute)) || value.ExpiresAt.AsTime().After(time.Now().Add(16*time.Minute)) {
			return aws.Credentials{}, component.ErrConfiguration
		}
		for _, secret := range []string{value.AccessKeyId, value.SecretAccessKey, value.SessionToken} {
			if secret == "" || len(secret) > 16384 || strings.ContainsAny(secret, " \t\r\n") {
				return aws.Credentials{}, component.ErrConfiguration
			}
		}
		if frozen.Step == modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE {
			input := snapshot.Input.Object
			if readBase {
				if snapshot.Kind != "FINETUNING" || snapshot.Finetuning == nil {
					return aws.Credentials{}, component.ErrConfiguration
				}
				input = snapshot.Finetuning.BaseModel.Object
			}
			if value.StorageConnectionId != input.StorageConnectionID || value.Bucket != input.Bucket || value.GetObjectKey() != input.Key || value.GetObjectPrefix() != "" {
				return aws.Credentials{}, component.ErrConfiguration
			}
		} else {
			scope := snapshot.PublicationScope
			if value.StorageConnectionId != scope.StorageConnectionID || value.Bucket != scope.Bucket || value.GetObjectKey() != "" || value.GetObjectPrefix() != scope.ApprovedPrefix+"/"+frozen.Identity.ExecutionId+"/" {
				return aws.Credentials{}, component.ErrConfiguration
			}
		}
		return aws.Credentials{AccessKeyID: value.AccessKeyId, SecretAccessKey: value.SecretAccessKey, SessionToken: value.SessionToken, CanExpire: true, Expires: value.ExpiresAt.AsTime(), Source: "execution-owner-temporary-session"}, nil
	})
	return aws.NewCredentialsCache(provider, func(options *aws.CredentialsCacheOptions) { options.ExpiryWindow = 2 * time.Minute }), nil
}
