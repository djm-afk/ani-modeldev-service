package service

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (step *Step) GetExecutionConfiguration(ctx context.Context, request *modeldevv1.GetExecutionConfigurationRequest) (*modeldevv1.GetExecutionConfigurationResponse, error) {
	requestedStep := request.GetContext().GetStep()
	tasks := map[modeldevv1.PipelineStep]string{
		modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE:    "prepare",
		modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT: "train-wait",
		modeldevv1.PipelineStep_PIPELINE_STEP_COLLECT:    "collect",
		modeldevv1.PipelineStep_PIPELINE_STEP_PUBLISH:    "publish",
		modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE:      "close",
		modeldevv1.PipelineStep_PIPELINE_STEP_EXPORT:     "export",
		modeldevv1.PipelineStep_PIPELINE_STEP_VERIFY:     "verify",
		modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER:   "register",
	}
	task, supported := tasks[requestedStep]
	if !supported {
		return nil, runtimeError(biz.ErrInvalidAdmission)
	}
	token, claim, err := step.runtimeRequest(ctx, request, request.GetContext(), requestedStep)
	if err != nil {
		return nil, err
	}
	result, err := step.runtime.Configuration(ctx, token, claim, task)
	if err != nil {
		return nil, runtimeError(err)
	}
	states, authority, err := runtimeReply(result, claim)
	if err != nil {
		return nil, err
	}
	snapshot, err := contractpb.EncodeSnapshot(result.Execution.Snapshot)
	if err != nil {
		return nil, runtimeError(biz.ErrManagedStepUnavailable)
	}
	intent, err := contractpb.EncodeIntent(result.Execution.Intent)
	if err != nil {
		return nil, runtimeError(biz.ErrManagedStepUnavailable)
	}
	return &modeldevv1.GetExecutionConfigurationResponse{
		Identity: runtimeIdentity(result.Execution), Snapshot: snapshot,
		Authority: authority, Workspace: encodeRuntimeWorkspace(result.Runtime.Workspace), States: states,
		KfpTaskId: result.TaskID,
		Timing:    existingStepTiming(result, task),
		Admission: &modeldevv1.AcceptExecutionRequest{Identity: runtimeIdentity(result.Execution), ResourceTenantId: result.Execution.TenantID, AdmittedActorId: result.Execution.Actor, IntentHash: result.Execution.IntentHash, Snapshot: snapshot, AcceptedAt: timestamppb.New(result.Execution.AcceptedAt), Intent: intent},
	}, nil
}

func (step *Step) EnsureTraining(ctx context.Context, request *modeldevv1.EnsureTrainingRequest) (*modeldevv1.EnsureTrainingResponse, error) {
	token, claim, err := step.runtimeRequest(ctx, request, request.GetContext(), modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT)
	if err != nil {
		return nil, err
	}
	result, err := step.runtime.Ensure(ctx, token, claim)
	if err != nil {
		return nil, runtimeError(err)
	}
	wire, err := runtimeTrainingStatus(result, claim)
	if err != nil {
		return nil, err
	}
	return &modeldevv1.EnsureTrainingResponse{Status: wire, Replayed: result.Replayed}, nil
}

func (step *Step) GetTrainingStatus(ctx context.Context, request *modeldevv1.GetTrainingStatusRequest) (*modeldevv1.GetTrainingStatusResponse, error) {
	token, claim, err := step.runtimeRequest(ctx, request, request.GetContext(), modeldevv1.PipelineStep_PIPELINE_STEP_TRAIN_WAIT)
	if err != nil {
		return nil, err
	}
	result, err := step.runtime.Status(ctx, token, claim)
	if err != nil {
		return nil, runtimeError(err)
	}
	wire, err := runtimeTrainingStatus(result, claim)
	if err != nil {
		return nil, err
	}
	return &modeldevv1.GetTrainingStatusResponse{Status: wire}, nil
}

func (step *Step) ReportStepResult(ctx context.Context, request *modeldevv1.ReportStepResultRequest) (*modeldevv1.ReportStepResultResponse, error) {
	// Publication is confirmed by the downstream close task after the upload
	// task has completed. A running publisher cannot attest its own completion.
	expected := modeldevv1.PipelineStep_PIPELINE_STEP_PREPARE
	if request.GetPublication() != nil {
		expected = modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE
		if request.GetContext().GetStep() == modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER {
			expected = modeldevv1.PipelineStep_PIPELINE_STEP_REGISTER
		}
	}
	if request.GetTrainingResult() != nil || request.GetFailure() != nil {
		expected = request.GetContext().GetStep()
	}
	token, claim, err := step.runtimeRequest(ctx, request, request.GetContext(), expected)
	if err != nil {
		return nil, err
	}
	if !runtimeUUID(request.ReportId) {
		return nil, runtimeError(biz.ErrInvalidAdmission)
	}
	var result biz.ManagedRuntimeResult
	switch report := request.Result.(type) {
	case *modeldevv1.ReportStepResultRequest_Prepared:
		if report == nil {
			return nil, runtimeError(biz.ErrInvalidAdmission)
		}
		workspace, input, decodeErr := decodeRuntimePrepared(report.Prepared)
		if decodeErr != nil {
			return nil, runtimeError(decodeErr)
		}
		result, err = step.runtime.Prepared(ctx, token, claim, workspace, input)
	case *modeldevv1.ReportStepResultRequest_Publication:
		if report == nil {
			return nil, runtimeError(biz.ErrInvalidAdmission)
		}
		candidate, decodeErr := decodeRuntimePublication(report.Publication, claim)
		if decodeErr != nil {
			return nil, runtimeError(decodeErr)
		}
		result, err = step.runtime.Publish(ctx, token, claim, candidate)
	case *modeldevv1.ReportStepResultRequest_TrainingResult, *modeldevv1.ReportStepResultRequest_Failure:
		return nil, status.Error(codes.Unimplemented, "this managed result capability is unavailable")
	default:
		return nil, runtimeError(biz.ErrInvalidAdmission)
	}
	if err != nil {
		return nil, runtimeError(err)
	}
	states, _, err := runtimeReply(result, claim)
	if err != nil {
		return nil, err
	}
	response := &modeldevv1.ReportStepResultResponse{Replayed: result.Replayed, States: states}
	if request.GetPublication() != nil {
		response.Publication, err = encodeRuntimePublication(result)
		if err != nil {
			return nil, err
		}
	}
	return response, nil
}

func (step *Step) RequestExecutionClose(ctx context.Context, request *modeldevv1.RequestExecutionCloseRequest) (*modeldevv1.RequestExecutionCloseResponse, error) {
	token, claim, err := step.runtimeRequest(ctx, request, request.GetContext(), modeldevv1.PipelineStep_PIPELINE_STEP_CLOSE)
	if err != nil {
		return nil, err
	}
	var reason string
	switch request.Reason {
	case modeldevv1.CloseReason_CLOSE_REASON_NATURAL_TERMINAL:
		reason = "NATURAL_TERMINAL"
	case modeldevv1.CloseReason_CLOSE_REASON_DEADLINE:
		reason = "DEADLINE"
	case modeldevv1.CloseReason_CLOSE_REASON_STEP_FAILED:
		reason = "STEP_FAILED"
	default:
		return nil, runtimeError(biz.ErrInvalidAdmission)
	}
	result, err := step.runtime.Close(ctx, token, claim, reason)
	if err != nil {
		return nil, runtimeError(err)
	}
	states, _, err := runtimeReply(result, claim)
	if err != nil {
		return nil, err
	}
	acceptedAt, ok := runtimeTimestamp(result.Runtime.CloseRequestedAt)
	if !ok || result.Runtime.CloseGeneration == 0 ||
		(states.CloseState != modeldevv1.CloseState_CLOSE_STATE_CLOSING && states.CloseState != modeldevv1.CloseState_CLOSE_STATE_CLOSED && states.CloseState != modeldevv1.CloseState_CLOSE_STATE_NEEDS_REVIEW) {
		return nil, runtimeError(biz.ErrManagedStepUnavailable)
	}
	return &modeldevv1.RequestExecutionCloseResponse{
		CloseGeneration: result.Runtime.CloseGeneration, CloseState: states.CloseState,
		Replayed: result.Replayed, AcceptedAt: acceptedAt,
	}, nil
}

func (step *Step) runtimeRequest(ctx context.Context, message proto.Message, wire *modeldevv1.StepContext, expected modeldevv1.PipelineStep) (string, biz.BeginManagedExecutionRequest, error) {
	tenant, token, authenticated := managedStepCredentials(ctx)
	if !authenticated {
		return "", biz.BeginManagedExecutionRequest{}, runtimeError(biz.ErrManagedStepUnauthorized)
	}
	if err := ctx.Err(); err != nil {
		return "", biz.BeginManagedExecutionRequest{}, runtimeError(err)
	}
	if message == nil || !validRuntimeMessage(message.ProtoReflect(), 0) || wire == nil || wire.Identity == nil || wire.Association == nil ||
		expected == modeldevv1.PipelineStep_PIPELINE_STEP_UNSPECIFIED || wire.Step != expected {
		return "", biz.BeginManagedExecutionRequest{}, runtimeError(biz.ErrInvalidAdmission)
	}
	identity, association := wire.Identity, wire.Association
	for _, value := range []string{identity.ExecutionId, association.KfpRunId, association.NamespaceUid, association.PodUid} {
		if !runtimeUUID(value) {
			return "", biz.BeginManagedExecutionRequest{}, runtimeError(biz.ErrInvalidAdmission)
		}
	}
	if (identity.OperationId != "" && !runtimeUUID(identity.OperationId)) || !runtimeSHA256(identity.ExecutionSpecHash) || association.NamespaceName == "" || association.WorkflowName == "" || association.WorkflowUid == "" || association.PodName == "" {
		return "", biz.BeginManagedExecutionRequest{}, runtimeError(biz.ErrInvalidAdmission)
	}
	if step == nil || step.runtime == nil {
		return "", biz.BeginManagedExecutionRequest{}, runtimeError(biz.ErrRuntimeNotReady)
	}
	return token, biz.BeginManagedExecutionRequest{
		TenantID: tenant, OperationID: identity.OperationId, ExecutionID: identity.ExecutionId, SpecHash: identity.ExecutionSpecHash,
		Association: biz.ManagedStepAssociation{
			RunID: association.KfpRunId, NamespaceName: association.NamespaceName, NamespaceUID: association.NamespaceUid,
			WorkflowName: association.WorkflowName, WorkflowUID: association.WorkflowUid, PodName: association.PodName, PodUID: association.PodUid,
		},
	}, nil
}

// Walk the actual protobuf tree so a nested unknown field or enum cannot be
// silently discarded while adapting a prepared input or publication candidate.
func validRuntimeMessage(message protoreflect.Message, depth int) bool {
	if !message.IsValid() || depth > 16 || len(message.GetUnknown()) != 0 {
		return false
	}
	valid := true
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		check := func(item protoreflect.Value) bool {
			switch field.Kind() {
			case protoreflect.MessageKind, protoreflect.GroupKind:
				return validRuntimeMessage(item.Message(), depth+1)
			case protoreflect.EnumKind:
				return field.Enum().Values().ByNumber(item.Enum()) != nil
			default:
				return true
			}
		}
		if field.IsMap() {
			valid = false
			return false
		}
		if field.IsList() {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				if !check(list.Get(i)) {
					valid = false
					return false
				}
			}
		} else {
			valid = check(value)
		}
		return valid
	})
	return valid
}

func runtimeReply(result biz.ManagedRuntimeResult, claim biz.BeginManagedExecutionRequest) (*modeldevv1.ExecutionStates, *modeldevv1.AuthorityBinding, error) {
	authority, execution := result.Authority, result.Execution
	if execution.TenantID != claim.TenantID || !runtimeUUID(execution.OperationID) || (claim.OperationID != "" && execution.OperationID != claim.OperationID) || execution.ExecutionID != claim.ExecutionID || execution.SpecHash != claim.SpecHash ||
		authority.TenantID != claim.TenantID || authority.OperationID != execution.OperationID || authority.ExecutionID != claim.ExecutionID || authority.SpecHash != claim.SpecHash ||
		authority.RunID != claim.Association.RunID || authority.NamespaceName != claim.Association.NamespaceName || authority.NamespaceUID != claim.Association.NamespaceUID ||
		authority.WorkflowName != claim.Association.WorkflowName || authority.WorkflowUID != claim.Association.WorkflowUID || authority.OwnerRevision == 0 || result.Runtime.OwnerRevision == 0 {
		return nil, nil, runtimeError(biz.ErrManagedStepUnavailable)
	}
	boundAt, ok := runtimeTimestamp(authority.BoundAt)
	if !ok {
		return nil, nil, runtimeError(biz.ErrManagedStepUnavailable)
	}
	states, ok := encodeRuntimeStates(result.States)
	if !ok {
		return nil, nil, runtimeError(biz.ErrManagedStepUnavailable)
	}
	return states, &modeldevv1.AuthorityBinding{KfpRunId: authority.RunID, NamespaceUid: authority.NamespaceUID, WorkflowUid: authority.WorkflowUID, BoundAt: boundAt}, nil
}

func encodeRuntimeStates(states biz.ExecutionStates) (*modeldevv1.ExecutionStates, bool) {
	compute, c := modeldevv1.ComputeState_value["COMPUTE_STATE_"+string(states.Compute)]
	delivery, d := modeldevv1.DeliveryState_value["DELIVERY_STATE_"+string(states.Delivery)]
	resource, r := modeldevv1.ResourceState_value["RESOURCE_STATE_"+string(states.Resource)]
	closeState, x := modeldevv1.CloseState_value["CLOSE_STATE_"+string(states.Close)]
	if !c || !d || !r || !x || compute == 0 || delivery == 0 || resource == 0 || closeState == 0 {
		return nil, false
	}
	return &modeldevv1.ExecutionStates{ComputeState: modeldevv1.ComputeState(compute), DeliveryState: modeldevv1.DeliveryState(delivery), ResourceState: modeldevv1.ResourceState(resource), CloseState: modeldevv1.CloseState(closeState)}, true
}

func runtimeTrainingStatus(result biz.ManagedRuntimeResult, claim biz.BeginManagedExecutionRequest) (*modeldevv1.TrainingStatus, error) {
	states, _, err := runtimeReply(result, claim)
	if err != nil {
		return nil, err
	}
	plan := result.Runtime.Training
	if plan == nil || plan.Name == "" || !runtimeSHA256(plan.RequestSHA256) {
		return nil, runtimeError(biz.ErrRuntimeNotReady)
	}
	wire := &modeldevv1.TrainingStatus{
		Identity: runtimeIdentity(result.Execution), CreateState: modeldevv1.ExternalCreateState_EXTERNAL_CREATE_STATE_INTENT_RECORDED,
		DeterministicTrainjobName: plan.Name, CreateRequestSha256: plan.RequestSHA256, States: states,
	}
	if timing := plan.Timing; timing != nil {
		admitted, a := runtimeTimestamp(timing.AdmittedAt)
		deadline, d := runtimeTimestamp(timing.DeadlineAt)
		if !a || !d || !timing.DeadlineAt.After(timing.AdmittedAt) {
			return nil, runtimeError(biz.ErrRuntimeNotReady)
		}
		wire.TrainingAdmittedAt, wire.TrainingDeadlineAt = admitted, deadline
	}
	if handle := result.Runtime.TrainingHandle; handle != nil {
		wire.CreateState = modeldevv1.ExternalCreateState_EXTERNAL_CREATE_STATE_CONFIRMED
		wire.Trainjob = &modeldevv1.ResourceRef{ApiVersion: "trainer.kubeflow.org/v1alpha1", Kind: "TrainJob", NamespaceName: plan.Workspace.NamespaceName, NamespaceUid: handle.NamespaceUID, Name: plan.Name, Uid: handle.TrainJobUID}
	}
	if observation := result.Runtime.Observation; observation != nil {
		observedAt, ok := runtimeTimestamp(observation.ObservedAt)
		if !ok {
			return nil, runtimeError(biz.ErrManagedStepUnavailable)
		}
		wire.ObservedAt = observedAt
		for _, resource := range observation.Resources {
			wire.ResourceHistory = append(wire.ResourceHistory, &modeldevv1.ResourceObservation{
				Resource: &modeldevv1.ResourceRef{ApiVersion: resource.APIVersion, Kind: resource.Kind, NamespaceName: resource.Namespace, NamespaceUid: observation.Handle.NamespaceUID, Name: resource.Name, Uid: resource.UID, OwnerUid: resource.OwnerUID},
				ExitCode: resource.ExitCode, ObservedAt: observedAt, ApiObjectPresent: resource.APIObjectPresent,
			})
		}
	}
	return wire, nil
}

func decodeRuntimePrepared(candidate *modeldevv1.PreparedInputCandidate) (biz.WorkspaceBinding, cpup01.InputRef, error) {
	if candidate == nil || candidate.Workspace == nil || candidate.Input == nil || candidate.PreparedManifest == nil ||
		candidate.PreparedManifest.RelativePath != "prepared-manifest.json" || candidate.PreparedManifest.SizeBytes <= 0 || !runtimeSHA256(candidate.PreparedManifest.Sha256) {
		return biz.WorkspaceBinding{}, cpup01.InputRef{}, biz.ErrInvalidAdmission
	}
	workspace := candidate.Workspace
	if workspace.Mode != trainingv1.WorkspaceMode_WORKSPACE_MODE_KFP_RUN_WORKSPACE && workspace.Mode != trainingv1.WorkspaceMode_WORKSPACE_MODE_EXECUTION_PVC {
		return biz.WorkspaceBinding{}, cpup01.InputRef{}, biz.ErrInvalidAdmission
	}
	object, err := decodeRuntimeObject(candidate.Input.Object)
	if err != nil {
		return biz.WorkspaceBinding{}, cpup01.InputRef{}, err
	}
	// The complete InputRef is compared with the immutable snapshot in biz;
	// sharing an input_version_id alone never validates prepared bytes.
	input := cpup01.InputRef{InputVersionID: candidate.Input.InputVersionId, Object: object, Format: candidate.Input.Format, SchemaVersion: candidate.Input.SchemaVersion, RowCount: candidate.Input.RowCount, FeatureCount: candidate.Input.FeatureCount}
	return biz.WorkspaceBinding{
		Mode: strings.TrimPrefix(workspace.Mode.String(), "WORKSPACE_MODE_"), NamespaceName: workspace.NamespaceName, NamespaceUID: workspace.NamespaceUid,
		PVCName: workspace.PvcName, PVCUID: workspace.PvcUid, InputSubpath: workspace.InputSubpath, TrainingSubpath: workspace.TrainingSubpath,
		ReportsSubpath: workspace.ReportsSubpath, PublicationSubpath: workspace.PublicationSubpath, WorkSubpath: workspace.WorkSubpath,
		PreparedManifestSHA256: candidate.PreparedManifest.Sha256, PreparedManifestBytes: candidate.PreparedManifest.SizeBytes,
	}, input, nil
}

func decodeRuntimePublication(candidate *modeldevv1.PublicationCandidate, claim biz.BeginManagedExecutionRequest) (biz.RuntimePublication, error) {
	if candidate == nil || candidate.Schema != cpup01.PublicationCandidateSchemaVersion || candidate.TenantId != claim.TenantID || candidate.ExecutionId != claim.ExecutionID || candidate.ExecutionSpecHash != claim.SpecHash || candidate.OperationId != claim.OperationID || candidate.LogicalPublicationKey == "" || candidate.UploadTaskId == "" || !runtimeUUID(candidate.UploadPodUid) || len(candidate.Files) == 0 {
		return biz.RuntimePublication{}, biz.ErrInvalidAdmission
	}
	manifest, err := decodeRuntimeObject(candidate.ManifestObject)
	if err != nil {
		return biz.RuntimePublication{}, err
	}
	publication := biz.RuntimePublication{
		LogicalKey: candidate.LogicalPublicationKey, Manifest: manifest,
		Upload: biz.UploadCompletion{RunID: claim.Association.RunID, WorkflowUID: claim.Association.WorkflowUID, TaskID: candidate.UploadTaskId, PodUID: candidate.UploadPodUid},
	}
	if candidate.BundleObject != nil {
		bundle, err := decodeRuntimeObject(candidate.BundleObject)
		if err != nil {
			return biz.RuntimePublication{}, err
		}
		publication.Bundle = &bundle
	}
	for _, file := range candidate.Files {
		if file == nil || file.File == nil || file.File.Role == trainingv1.FileRole_FILE_ROLE_UNSPECIFIED {
			return biz.RuntimePublication{}, biz.ErrInvalidAdmission
		}
		object, err := decodeRuntimeObject(file.Object)
		if err != nil {
			return biz.RuntimePublication{}, err
		}
		publication.Files = append(publication.Files, biz.PublishedRuntimeFile{
			File: cpup01.OutputFile{RelativePath: file.File.RelativePath, Role: strings.TrimPrefix(file.File.Role.String(), "FILE_ROLE_"), InTar: file.File.InTar, SizeBytes: file.File.SizeBytes, SHA256: file.File.Sha256}, Object: object,
		})
	}
	return publication, nil
}

func decodeRuntimeObject(wire *trainingv1.FixedObjectRef) (cpup01.FixedObjectRef, error) {
	if wire == nil || wire.StorageConnectionId == "" || wire.Bucket == "" || wire.Key == "" || wire.SizeBytes < 0 || !runtimeSHA256(wire.Sha256) {
		return cpup01.FixedObjectRef{}, biz.ErrInvalidAdmission
	}
	object := cpup01.FixedObjectRef{StorageConnectionID: wire.StorageConnectionId, Bucket: wire.Bucket, Key: wire.Key, SizeBytes: wire.SizeBytes, SHA256: wire.Sha256}
	switch fixed := wire.Immutability.(type) {
	case *trainingv1.FixedObjectRef_VersionId:
		if fixed == nil || fixed.VersionId == "" {
			return cpup01.FixedObjectRef{}, biz.ErrInvalidAdmission
		}
		value := fixed.VersionId
		object.VersionID = &value
	case *trainingv1.FixedObjectRef_ImmutableCopy:
		if fixed == nil || !fixed.ImmutableCopy {
			return cpup01.FixedObjectRef{}, biz.ErrInvalidAdmission
		}
		value := fixed.ImmutableCopy
		object.ImmutableCopy = &value
	default:
		return cpup01.FixedObjectRef{}, biz.ErrInvalidAdmission
	}
	return object, nil
}

func encodeRuntimePublication(result biz.ManagedRuntimeResult) (*modeldevv1.VerifiedPublication, error) {
	publication := result.Runtime.Publication
	if publication == nil || publication.ID == "" || publication.ReceiptID == "" || len(publication.Files) == 0 {
		return nil, runtimeError(biz.ErrManagedStepUnavailable)
	}
	verifiedAt, verified := runtimeTimestamp(publication.VerifiedAt)
	completedAt, completed := runtimeTimestamp(publication.Upload.CompletedAt)
	observedAt, observed := runtimeTimestamp(publication.Upload.ObservedAt)
	if !verified || !completed || !observed {
		return nil, runtimeError(biz.ErrManagedStepUnavailable)
	}
	wire := &modeldevv1.VerifiedPublication{
		PublicationId: publication.ID, Identity: runtimeIdentity(result.Execution), LogicalPublicationKey: publication.LogicalKey,
		ManifestObject: encodeRuntimeObject(publication.Manifest), RemoteVerifiedAt: verifiedAt, VerificationReceiptId: publication.ReceiptID,
		UploadCompletion: &modeldevv1.UploadCompletionEvidence{KfpRunId: publication.Upload.RunID, WorkflowUid: publication.Upload.WorkflowUID, TaskId: publication.Upload.TaskID, PodUid: publication.Upload.PodUID, UploaderContainerName: publication.Upload.ContainerName, CompletedAt: completedAt, ObservedAt: observedAt},
	}
	if publication.Bundle != nil {
		wire.BundleObject = encodeRuntimeObject(*publication.Bundle)
	}
	for _, file := range publication.Files {
		wire.Files = append(wire.Files, &modeldevv1.VerifiedPublishedFile{
			ArtifactId: file.ArtifactID,
			File:       &trainingv1.FileEntry{RelativePath: file.File.RelativePath, Role: trainingv1.FileRole(trainingv1.FileRole_value["FILE_ROLE_"+file.File.Role]), SizeBytes: file.File.SizeBytes, Sha256: file.File.SHA256, InTar: file.File.InTar},
			Object:     encodeRuntimeObject(file.Object), RemoteVerifiedAt: verifiedAt,
		})
	}
	return wire, nil
}

func encodeRuntimeWorkspace(workspace *biz.WorkspaceBinding) *trainingv1.WorkspaceRef {
	if workspace == nil {
		return nil
	}
	return &trainingv1.WorkspaceRef{
		Mode: trainingv1.WorkspaceMode(trainingv1.WorkspaceMode_value["WORKSPACE_MODE_"+workspace.Mode]), NamespaceName: workspace.NamespaceName, NamespaceUid: workspace.NamespaceUID,
		PvcName: workspace.PVCName, PvcUid: workspace.PVCUID, InputSubpath: workspace.InputSubpath, TrainingSubpath: workspace.TrainingSubpath, ReportsSubpath: workspace.ReportsSubpath, PublicationSubpath: workspace.PublicationSubpath, WorkSubpath: workspace.WorkSubpath,
	}
}

func encodeRuntimeObject(object cpup01.FixedObjectRef) *trainingv1.FixedObjectRef {
	wire := &trainingv1.FixedObjectRef{StorageConnectionId: object.StorageConnectionID, Bucket: object.Bucket, Key: object.Key, SizeBytes: object.SizeBytes, Sha256: object.SHA256}
	if object.VersionID != nil {
		wire.Immutability = &trainingv1.FixedObjectRef_VersionId{VersionId: *object.VersionID}
	}
	if object.ImmutableCopy != nil {
		wire.Immutability = &trainingv1.FixedObjectRef_ImmutableCopy{ImmutableCopy: *object.ImmutableCopy}
	}
	return wire
}

func runtimeIdentity(execution biz.Execution) *trainingv1.ExecutionIdentity {
	return &trainingv1.ExecutionIdentity{OperationId: execution.OperationID, ExecutionId: execution.ExecutionID, ExecutionSpecHash: execution.SpecHash}
}

func runtimeTimestamp(value time.Time) (*timestamppb.Timestamp, bool) {
	wire := timestamppb.New(value)
	return wire, !value.IsZero() && wire.CheckValid() == nil
}

func runtimeUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func runtimeSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func runtimeError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case errors.Is(err, biz.ErrInvalidAdmission), errors.Is(err, cpup01.ErrInvalidArgument):
		return commandError(codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "invalid managed runtime request", "")
	case errors.Is(err, biz.ErrManagedStepUnauthorized):
		return commandError(codes.Unauthenticated, modeldevv1.ErrorReason_ERROR_REASON_UNAUTHENTICATED, "managed workload authentication failed", "")
	case errors.Is(err, biz.ErrRunAuthorityConflict), errors.Is(err, biz.ErrAdmissionConflict), errors.Is(err, biz.ErrExecutionNotFound):
		return commandError(codes.PermissionDenied, modeldevv1.ErrorReason_ERROR_REASON_AUTHORITY_MISMATCH, "managed workload association denied", "")
	case errors.Is(err, biz.ErrRuntimeConflict):
		return commandError(codes.AlreadyExists, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, "runtime report conflicts with committed facts", "")
	case errors.Is(err, biz.ErrPipelineDispatchBlocked):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_EXECUTION_CLOSING, "execution does not permit new runtime work", "")
	case errors.Is(err, biz.ErrTrainingUncertain):
		return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_RESULT_UNCERTAIN, "training creation requires reconciliation", "")
	case errors.Is(err, biz.ErrTrainingRejected):
		return commandError(codes.Aborted, modeldevv1.ErrorReason_ERROR_REASON_TRAINING_FAILED, "training creation was rejected", "")
	case errors.Is(err, biz.ErrModelDependencyUnavailable):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "Model dependency is not connected", "")
	case errors.Is(err, biz.ErrRuntimeNotReady), errors.Is(err, biz.ErrTrainingNotFound):
		return commandError(codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, "managed runtime prerequisites are not ready", "")
	default:
		return commandError(codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "managed runtime unavailable", "")
	}
}
