// Package protobuf adapts CPU-P01 pure contracts to the owned protobuf API.
// It is a transport boundary, not an authorization decision or live validator.
package protobuf

import (
	"fmt"
	"strconv"
	"strings"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func EncodeIntent(intent cpup01.Intent) (*modeldevv1.UserIntent, error) {
	if _, _, err := cpup01.CanonicalIntent(intent); err != nil {
		return nil, err
	}
	message := &modeldevv1.UserIntent{
		Name: intent.Name, Kind: trainingv1.ExecutionKind(trainingv1.ExecutionKind_value["EXECUTION_KIND_"+intent.Kind]),
		PresetId: intent.PresetID, DatasetVersionId: intent.DatasetVersionID,
		ImageVersionId: cloneString(intent.ImageVersionID), SourceExecutionId: cloneString(intent.SourceExecutionID),
		BaseModelVersionId: cloneString(intent.BaseModelVersionID), TargetModelId: cloneString(intent.TargetModelID), NewModelName: cloneString(intent.NewModelName),
	}
	if intent.Finetuning != nil {
		message.Finetuning = &modeldevv1.FinetuningParameters{LearningRate: cloneString(intent.Finetuning.LearningRate), MaxSteps: cloneInt64(intent.Finetuning.MaxSteps)}
	}
	if intent.GeneralParameters != nil {
		parameters, err := encodeParameters(*intent.GeneralParameters)
		if err != nil {
			return nil, err
		}
		message.GeneralParameters = &modeldevv1.ParameterSelection{Values: parameters}
	}
	return message, nil
}

func DecodeIntent(message *modeldevv1.UserIntent) (cpup01.Intent, error) {
	if message == nil {
		return cpup01.Intent{}, invalid("missing intent")
	}
	if err := checkMessage(message.ProtoReflect(), 0); err != nil {
		return cpup01.Intent{}, err
	}
	intent := cpup01.Intent{
		Name: message.Name, Kind: strings.TrimPrefix(message.Kind.String(), "EXECUTION_KIND_"),
		PresetID: message.PresetId, DatasetVersionID: message.DatasetVersionId,
		ImageVersionID: cloneString(message.ImageVersionId), SourceExecutionID: cloneString(message.SourceExecutionId),
		BaseModelVersionID: cloneString(message.BaseModelVersionId), TargetModelID: cloneString(message.TargetModelId), NewModelName: cloneString(message.NewModelName),
	}
	if message.Finetuning != nil {
		intent.Finetuning = &cpup01.FinetuningParameters{LearningRate: cloneString(message.Finetuning.LearningRate), MaxSteps: cloneInt64(message.Finetuning.MaxSteps)}
	}
	if message.GeneralParameters != nil {
		parameters, err := decodeParameters(message.GeneralParameters.Values)
		if err != nil {
			return cpup01.Intent{}, err
		}
		intent.GeneralParameters = &parameters
	}
	if _, _, err := cpup01.CanonicalIntent(intent); err != nil {
		return cpup01.Intent{}, err
	}
	return intent, nil
}

func EncodeSnapshot(snapshot cpup01.Snapshot) (*modeldevv1.ExecutionSnapshot, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	parameters, err := encodeParameters(snapshot.Program.ResolvedParameters)
	if err != nil {
		return nil, err
	}
	object := &trainingv1.FixedObjectRef{
		StorageConnectionId: snapshot.Input.Object.StorageConnectionID, Bucket: snapshot.Input.Object.Bucket, Key: snapshot.Input.Object.Key,
		SizeBytes: snapshot.Input.Object.SizeBytes, Sha256: snapshot.Input.Object.SHA256,
	}
	if snapshot.Input.Object.VersionID != nil {
		object.Immutability = &trainingv1.FixedObjectRef_VersionId{VersionId: *snapshot.Input.Object.VersionID}
	} else {
		object.Immutability = &trainingv1.FixedObjectRef_ImmutableCopy{ImmutableCopy: *snapshot.Input.Object.ImmutableCopy}
	}
	files := make([]*trainingv1.RequiredOutput, 0, len(snapshot.OutputContract.RequiredFiles))
	for _, file := range snapshot.OutputContract.RequiredFiles {
		files = append(files, &trainingv1.RequiredOutput{Role: trainingv1.FileRole(trainingv1.FileRole_value["FILE_ROLE_"+file.Role]), RelativePath: file.RelativePath, MaxSizeBytes: file.MaxSizeBytes, InTar: cloneBool(file.InTar)})
	}
	message := &modeldevv1.ExecutionSnapshot{
		SchemaVersion: snapshot.SchemaVersion, Kind: trainingv1.ExecutionKind(trainingv1.ExecutionKind_value["EXECUTION_KIND_"+snapshot.Kind]),
		DeliveryMode: trainingv1.DeliveryMode_DELIVERY_MODE_SAVE_ARTIFACTS,
		Release: &modeldevv1.ReleaseSnapshot{
			ReleaseId: snapshot.Release.ReleaseID, ReleaseDigest: snapshot.Release.ReleaseDigest, PresetId: snapshot.Release.PresetID,
			AcceptedBindingGeneration: snapshot.Release.AcceptedBindingGeneration, PipelineId: snapshot.Release.PipelineID,
			PipelineVersionId: snapshot.Release.PipelineVersionID, PipelineIrSha256: snapshot.Release.PipelineIRSHA256,
			Runtime: &trainingv1.RuntimeRef{Name: snapshot.Release.Runtime.Name, Kind: snapshot.Release.Runtime.Kind, ApiGroup: snapshot.Release.Runtime.APIGroup, ContentSha256: snapshot.Release.Runtime.ContentSHA256, TargetJobs: cloneStrings(snapshot.Release.Runtime.TargetJobs)},
		},
		Input:     &trainingv1.InputRef{InputVersionId: snapshot.Input.InputVersionID, Object: object, Format: snapshot.Input.Format, SchemaVersion: snapshot.Input.SchemaVersion, RowCount: snapshot.Input.RowCount, FeatureCount: snapshot.Input.FeatureCount},
		Program:   &trainingv1.ProgramRef{ImageVersionId: snapshot.Program.ImageVersionID, ImageDigest: snapshot.Program.ImageDigest, Command: cloneStrings(snapshot.Program.Command), ResolvedArgs: cloneStrings(snapshot.Program.ResolvedArgs), ResolvedParameters: parameters},
		Resources: &trainingv1.CPUResources{Nodes: snapshot.Resources.Nodes, ProcessesPerNode: snapshot.Resources.ProcessesPerNode, RequestMillicpu: snapshot.Resources.RequestMillicpu, LimitMillicpu: snapshot.Resources.LimitMillicpu, RequestMemoryBytes: snapshot.Resources.RequestMemoryBytes, LimitMemoryBytes: snapshot.Resources.LimitMemoryBytes},
		Environment: &modeldevv1.EnvironmentBindingSnapshot{
			BindingId: snapshot.Environment.BindingID, BindingDigest: snapshot.Environment.BindingDigest,
			ClusterId: snapshot.Environment.ClusterID, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUid: snapshot.Environment.NamespaceUID,
			KfpConnectionRef: snapshot.Environment.KFPConnectionRef, ExperimentId: snapshot.Environment.ExperimentID,
			Identities: &modeldevv1.RuntimeIdentityRefs{ModeldevControlIdentityRef: snapshot.Environment.Identities.ModeldevControlIdentityRef, KfpStepServiceAccount: snapshot.Environment.Identities.KFPStepServiceAccount, TrainerServiceAccount: snapshot.Environment.Identities.TrainerServiceAccount, VerifierServiceAccount: snapshot.Environment.Identities.VerifierServiceAccount, TenantProxyIdentity: snapshot.Environment.Identities.TenantProxyIdentity},
		},
		Workspace:        &trainingv1.WorkspaceContract{Mode: trainingv1.WorkspaceMode(trainingv1.WorkspaceMode_value["WORKSPACE_MODE_"+snapshot.Workspace.Mode]), StorageClass: snapshot.Workspace.StorageClass, CapacityBytes: snapshot.Workspace.CapacityBytes, InputSubpath: snapshot.Workspace.InputSubpath, TrainingSubpath: snapshot.Workspace.TrainingSubpath, ReportsSubpath: snapshot.Workspace.ReportsSubpath, PublicationSubpath: snapshot.Workspace.PublicationSubpath},
		PublicationScope: &modeldevv1.StorageScope{StorageConnectionId: snapshot.PublicationScope.StorageConnectionID, Bucket: snapshot.PublicationScope.Bucket, ApprovedPrefix: snapshot.PublicationScope.ApprovedPrefix, CredentialReference: snapshot.PublicationScope.CredentialReference},
		OutputContract:   &trainingv1.OutputContract{SchemaVersion: snapshot.OutputContract.SchemaVersion, OutputKind: trainingv1.OutputKind(trainingv1.OutputKind_value["OUTPUT_KIND_"+snapshot.OutputContract.OutputKind]), DeliveryMode: trainingv1.DeliveryMode_DELIVERY_MODE_SAVE_ARTIFACTS, RequiredFiles: files, MaxFileCount: snapshot.OutputContract.MaxFileCount, MaxTotalBytes: snapshot.OutputContract.MaxTotalBytes, CreateTarBundle: snapshot.OutputContract.CreateTarBundle, MaxTarBytes: cloneInt64(snapshot.OutputContract.MaxTarBytes)},
		DeadlineAt:       timestamppb.New(snapshot.DeadlineAt.UTC()),
	}
	if snapshot.Finetuning != nil {
		message.Finetuning = encodeFinetuningSnapshot(*snapshot.Finetuning)
	}
	return message, nil
}

func DecodeSnapshot(message *modeldevv1.ExecutionSnapshot) (cpup01.Snapshot, error) {
	if message == nil {
		return cpup01.Snapshot{}, invalid("missing snapshot")
	}
	if err := checkMessage(message.ProtoReflect(), 0); err != nil {
		return cpup01.Snapshot{}, err
	}
	if message.Release == nil || message.Release.Runtime == nil || message.Input == nil || message.Input.Object == nil || message.Program == nil || message.Resources == nil || message.Environment == nil || message.Environment.Identities == nil || message.Workspace == nil || message.PublicationScope == nil || message.OutputContract == nil || message.DeadlineAt == nil {
		return cpup01.Snapshot{}, invalid("missing snapshot fields")
	}
	if err := message.DeadlineAt.CheckValid(); err != nil {
		return cpup01.Snapshot{}, invalid("deadline timestamp")
	}
	parameters, err := decodeParameters(message.Program.ResolvedParameters)
	if err != nil {
		return cpup01.Snapshot{}, err
	}
	object := cpup01.FixedObjectRef{StorageConnectionID: message.Input.Object.StorageConnectionId, Bucket: message.Input.Object.Bucket, Key: message.Input.Object.Key, SizeBytes: message.Input.Object.SizeBytes, SHA256: message.Input.Object.Sha256}
	switch immutability := message.Input.Object.Immutability.(type) {
	case *trainingv1.FixedObjectRef_VersionId:
		if immutability == nil {
			return cpup01.Snapshot{}, invalid("input immutability")
		}
		version := immutability.VersionId
		object.VersionID = &version
	case *trainingv1.FixedObjectRef_ImmutableCopy:
		if immutability == nil {
			return cpup01.Snapshot{}, invalid("input immutability")
		}
		fixed := immutability.ImmutableCopy
		object.ImmutableCopy = &fixed
	default:
		return cpup01.Snapshot{}, invalid("input immutability")
	}
	files := make([]cpup01.RequiredOutput, 0, len(message.OutputContract.RequiredFiles))
	for _, file := range message.OutputContract.RequiredFiles {
		if file == nil {
			return cpup01.Snapshot{}, invalid("missing required output")
		}
		files = append(files, cpup01.RequiredOutput{Role: strings.TrimPrefix(file.Role.String(), "FILE_ROLE_"), RelativePath: file.RelativePath, MaxSizeBytes: file.MaxSizeBytes, InTar: cloneBool(file.InTar)})
	}
	snapshot := cpup01.Snapshot{
		SchemaVersion: message.SchemaVersion, Kind: strings.TrimPrefix(message.Kind.String(), "EXECUTION_KIND_"), DeliveryMode: strings.TrimPrefix(message.DeliveryMode.String(), "DELIVERY_MODE_"),
		Release: cpup01.ReleaseSnapshot{
			ReleaseID: message.Release.ReleaseId, ReleaseDigest: message.Release.ReleaseDigest, PresetID: message.Release.PresetId,
			AcceptedBindingGeneration: message.Release.AcceptedBindingGeneration, PipelineID: message.Release.PipelineId,
			PipelineVersionID: message.Release.PipelineVersionId, PipelineIRSHA256: message.Release.PipelineIrSha256,
			Runtime: cpup01.RuntimeRef{Name: message.Release.Runtime.Name, Kind: message.Release.Runtime.Kind, APIGroup: message.Release.Runtime.ApiGroup, ContentSHA256: message.Release.Runtime.ContentSha256, TargetJobs: cloneStrings(message.Release.Runtime.TargetJobs)},
		},
		Input:     cpup01.InputRef{InputVersionID: message.Input.InputVersionId, Object: object, Format: message.Input.Format, SchemaVersion: message.Input.SchemaVersion, RowCount: message.Input.RowCount, FeatureCount: message.Input.FeatureCount},
		Program:   cpup01.ProgramRef{ImageVersionID: message.Program.ImageVersionId, ImageDigest: message.Program.ImageDigest, Command: cloneStrings(message.Program.Command), ResolvedArgs: cloneStrings(message.Program.ResolvedArgs), ResolvedParameters: parameters},
		Resources: cpup01.CPUResources{Nodes: message.Resources.Nodes, ProcessesPerNode: message.Resources.ProcessesPerNode, RequestMillicpu: message.Resources.RequestMillicpu, LimitMillicpu: message.Resources.LimitMillicpu, RequestMemoryBytes: message.Resources.RequestMemoryBytes, LimitMemoryBytes: message.Resources.LimitMemoryBytes},
		Environment: cpup01.EnvironmentBindingSnapshot{
			BindingID: message.Environment.BindingId, BindingDigest: message.Environment.BindingDigest, ClusterID: message.Environment.ClusterId,
			NamespaceName: message.Environment.NamespaceName, NamespaceUID: message.Environment.NamespaceUid, KFPConnectionRef: message.Environment.KfpConnectionRef, ExperimentID: message.Environment.ExperimentId,
			Identities: cpup01.RuntimeIdentityRefs{ModeldevControlIdentityRef: message.Environment.Identities.ModeldevControlIdentityRef, KFPStepServiceAccount: message.Environment.Identities.KfpStepServiceAccount, TrainerServiceAccount: message.Environment.Identities.TrainerServiceAccount, VerifierServiceAccount: message.Environment.Identities.VerifierServiceAccount, TenantProxyIdentity: message.Environment.Identities.TenantProxyIdentity},
		},
		Workspace:        cpup01.WorkspaceContract{Mode: strings.TrimPrefix(message.Workspace.Mode.String(), "WORKSPACE_MODE_"), StorageClass: message.Workspace.StorageClass, CapacityBytes: message.Workspace.CapacityBytes, InputSubpath: message.Workspace.InputSubpath, TrainingSubpath: message.Workspace.TrainingSubpath, ReportsSubpath: message.Workspace.ReportsSubpath, PublicationSubpath: message.Workspace.PublicationSubpath},
		PublicationScope: cpup01.StorageScope{StorageConnectionID: message.PublicationScope.StorageConnectionId, Bucket: message.PublicationScope.Bucket, ApprovedPrefix: message.PublicationScope.ApprovedPrefix, CredentialReference: message.PublicationScope.CredentialReference},
		OutputContract:   cpup01.OutputContract{SchemaVersion: message.OutputContract.SchemaVersion, OutputKind: strings.TrimPrefix(message.OutputContract.OutputKind.String(), "OUTPUT_KIND_"), DeliveryMode: strings.TrimPrefix(message.OutputContract.DeliveryMode.String(), "DELIVERY_MODE_"), RequiredFiles: files, MaxFileCount: message.OutputContract.MaxFileCount, MaxTotalBytes: message.OutputContract.MaxTotalBytes, CreateTarBundle: message.OutputContract.CreateTarBundle, MaxTarBytes: cloneInt64(message.OutputContract.MaxTarBytes)},
		DeadlineAt:       message.DeadlineAt.AsTime(),
	}
	if message.Finetuning != nil {
		fine, err := decodeFinetuningSnapshot(message.Finetuning)
		if err != nil {
			return cpup01.Snapshot{}, err
		}
		snapshot.Finetuning = &fine
	}
	if err := snapshot.Validate(); err != nil {
		return cpup01.Snapshot{}, err
	}
	return snapshot, nil
}

func encodeParameters(parameters []cpup01.Parameter) ([]*trainingv1.Parameter, error) {
	result := make([]*trainingv1.Parameter, 0, len(parameters))
	for _, parameter := range parameters {
		message := &trainingv1.Parameter{Name: parameter.Name}
		switch parameter.Type {
		case "INTEGER":
			value, err := strconv.ParseInt(parameter.Value, 10, 64)
			if err != nil {
				return nil, invalid("integer parameter")
			}
			message.Value = &trainingv1.Parameter_IntegerValue{IntegerValue: value}
		case "DECIMAL":
			message.Value = &trainingv1.Parameter_DecimalValue{DecimalValue: parameter.Value}
		default:
			return nil, invalid("parameter type")
		}
		result = append(result, message)
	}
	return result, nil
}

func decodeParameters(parameters []*trainingv1.Parameter) ([]cpup01.Parameter, error) {
	result := make([]cpup01.Parameter, 0, len(parameters))
	for _, parameter := range parameters {
		if parameter == nil {
			return nil, invalid("missing parameter")
		}
		item := cpup01.Parameter{Name: parameter.Name}
		switch value := parameter.Value.(type) {
		case *trainingv1.Parameter_IntegerValue:
			if value == nil {
				return nil, invalid("parameter value")
			}
			item.Type, item.Value = "INTEGER", strconv.FormatInt(value.IntegerValue, 10)
		case *trainingv1.Parameter_DecimalValue:
			if value == nil {
				return nil, invalid("parameter value")
			}
			item.Type, item.Value = "DECIMAL", value.DecimalValue
		default:
			return nil, invalid("parameter value")
		}
		result = append(result, item)
	}
	return result, nil
}

// Unknown fields must not silently disappear before calculating a known v1
// digest. This also rejects unknown nested enum numbers and invalid messages.
func checkMessage(message protoreflect.Message, depth int) error {
	if !message.IsValid() || depth > 16 {
		return invalid("message shape")
	}
	if len(message.GetUnknown()) != 0 {
		return invalid("unknown protobuf fields")
	}
	var result error
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.IsMap() {
			result = invalid("unsupported protobuf map")
			return false
		}
		if field.IsList() {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				if result = checkValue(field, list.Get(i), depth); result != nil {
					return false
				}
			}
		} else {
			result = checkValue(field, value, depth)
		}
		return result == nil
	})
	return result
}

func checkValue(field protoreflect.FieldDescriptor, value protoreflect.Value, depth int) error {
	switch field.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return checkMessage(value.Message(), depth+1)
	case protoreflect.EnumKind:
		if field.Enum().Values().ByNumber(value.Enum()) == nil {
			return invalid("unknown protobuf enum")
		}
	}
	return nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func invalid(reason string) error {
	return fmt.Errorf("%w: protobuf %s", cpup01.ErrInvalidArgument, reason)
}
