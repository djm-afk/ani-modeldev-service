// Package protobuf adapts the single active pure contract to protobuf.
package protobuf

import (
	"fmt"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strconv"
	"strings"
)

func EncodeIntent(i cpup01.Intent) (*modeldevv1.UserIntent, error) {
	if _, _, err := cpup01.CanonicalIntent(i); err != nil {
		return nil, err
	}
	m := &modeldevv1.UserIntent{Name: i.Name, Kind: trainingv1.ExecutionKind(trainingv1.ExecutionKind_value["EXECUTION_KIND_"+i.Kind]), ImplementationId: i.ImplementationID, ComputePresetId: i.ComputePresetID, DatasetVersionId: i.DatasetVersionID, TimeoutSeconds: cloneUint64(i.TimeoutSeconds), BaseModelVersionId: cloneString(i.BaseModelVersionID), TargetModelId: cloneString(i.TargetModelID), NewModelName: cloneString(i.NewModelName), FinetuningTemplate: cloneString(i.FinetuningTemplate), TrainingImageId: cloneString(i.TrainingImageID), TrainingImageScope: cloneString(i.TrainingImageScope)}
	if i.MLP != nil {
		p := i.MLP
		m.Parameters = &modeldevv1.UserIntent_Mlp{Mlp: &modeldevv1.MLPParameters{LearningRate: cloneString(p.LearningRate), Epochs: cloneInt64(p.Epochs), BatchSize: cloneInt64(p.BatchSize), Seed: cloneUint32(p.Seed)}}
	}
	if i.Finetuning != nil {
		p := i.Finetuning
		m.Parameters = &modeldevv1.UserIntent_SftLora{SftLora: &modeldevv1.FinetuningParameters{LearningRate: cloneString(p.LearningRate), MaxSteps: cloneInt64(p.MaxSteps), PerDeviceTrainBatchSize: cloneInt64(p.PerDeviceTrainBatchSize), GradientAccumulationSteps: cloneInt64(p.GradientAccumulationSteps), CutoffLen: cloneInt64(p.CutoffLen), LoraRank: cloneInt64(p.LoraRank), LoraAlpha: cloneInt64(p.LoraAlpha), LoraDropout: cloneString(p.LoraDropout), Seed: cloneUint32(p.Seed)}}
	}
	return m, nil
}
func DecodeIntent(m *modeldevv1.UserIntent) (cpup01.Intent, error) {
	if m == nil || checkMessage(m.ProtoReflect(), 0) != nil {
		return cpup01.Intent{}, invalid("intent")
	}
	i := cpup01.Intent{Name: m.Name, Kind: strings.TrimPrefix(m.Kind.String(), "EXECUTION_KIND_"), ImplementationID: m.ImplementationId, ComputePresetID: m.ComputePresetId, DatasetVersionID: m.DatasetVersionId, TimeoutSeconds: cloneUint64(m.TimeoutSeconds), BaseModelVersionID: cloneString(m.BaseModelVersionId), TargetModelID: cloneString(m.TargetModelId), NewModelName: cloneString(m.NewModelName), FinetuningTemplate: cloneString(m.FinetuningTemplate), TrainingImageID: cloneString(m.TrainingImageId), TrainingImageScope: cloneString(m.TrainingImageScope)}
	switch p := m.Parameters.(type) {
	case *modeldevv1.UserIntent_Mlp:
		if p == nil || p.Mlp == nil {
			return cpup01.Intent{}, invalid("parameters")
		}
		v := p.Mlp
		i.MLP = &cpup01.MLPParameters{LearningRate: cloneString(v.LearningRate), Epochs: cloneInt64(v.Epochs), BatchSize: cloneInt64(v.BatchSize), Seed: cloneUint32(v.Seed)}
	case *modeldevv1.UserIntent_SftLora:
		if p == nil || p.SftLora == nil {
			return cpup01.Intent{}, invalid("parameters")
		}
		v := p.SftLora
		i.Finetuning = &cpup01.FinetuningParameters{LearningRate: cloneString(v.LearningRate), MaxSteps: cloneInt64(v.MaxSteps), PerDeviceTrainBatchSize: cloneInt64(v.PerDeviceTrainBatchSize), GradientAccumulationSteps: cloneInt64(v.GradientAccumulationSteps), CutoffLen: cloneInt64(v.CutoffLen), LoraRank: cloneInt64(v.LoraRank), LoraAlpha: cloneInt64(v.LoraAlpha), LoraDropout: cloneString(v.LoraDropout), Seed: cloneUint32(v.Seed)}
	default:
		return cpup01.Intent{}, invalid("parameters")
	}
	if _, _, err := cpup01.CanonicalIntent(i); err != nil {
		return cpup01.Intent{}, err
	}
	return i, nil
}
func cloneUint64(v *uint64) *uint64 {
	if v == nil {
		return nil
	}
	p := *v
	return &p
}
func cloneUint32(v *uint32) *uint32 {
	if v == nil {
		return nil
	}
	p := *v
	return &p
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
		files = append(files, &trainingv1.RequiredOutput{Role: trainingv1.FileRole(trainingv1.FileRole_value["FILE_ROLE_"+file.Role]), RelativePath: file.RelativePath, InTar: file.InTar})
	}
	message := &modeldevv1.ExecutionSnapshot{
		SchemaVersion: snapshot.SchemaVersion, Kind: trainingv1.ExecutionKind(trainingv1.ExecutionKind_value["EXECUTION_KIND_"+snapshot.Kind]),
		Limits:         &modeldevv1.ContentLimits{MaxContentBytes: snapshot.Limits.MaxContentBytes},
		DeliveryMode:   trainingv1.DeliveryMode_DELIVERY_MODE_SAVE_ARTIFACTS,
		IdentityRef:    encodeIdentity(snapshot.IdentityRef),
		Implementation: encodeImplementation(snapshot.Implementation),
		ComputeOrigin:  encodeComputeOrigin(snapshot.ComputeOrigin),
		Timing:         encodeTiming(snapshot.Timing),
		RuntimePlan:    encodeRuntimePlan(snapshot.RuntimePlan),
		Input:          &trainingv1.InputRef{InputVersionId: snapshot.Input.InputVersionID, Object: object, Format: snapshot.Input.Format, SchemaVersion: snapshot.Input.SchemaVersion, RowCount: snapshot.Input.RowCount, FeatureCount: snapshot.Input.FeatureCount, PreparedDataset: &trainingv1.PreparedDatasetFacts{Format: snapshot.Input.PreparedDataset.Format, TransformId: snapshot.Input.PreparedDataset.TransformID, SizeBytes: snapshot.Input.PreparedDataset.SizeBytes, Sha256: snapshot.Input.PreparedDataset.SHA256, RowCount: snapshot.Input.PreparedDataset.RowCount}},
		Program:        &trainingv1.ProgramRef{ImageId: snapshot.Program.ImageID, ImageVersion: snapshot.Program.ImageVersion, ImageScope: string(snapshot.Program.ImageScope), ImageDigest: snapshot.Program.ImageDigest, Command: cloneStrings(snapshot.Program.Command), ResolvedArgs: cloneStrings(snapshot.Program.ResolvedArgs), ResolvedParameters: parameters},
		Resources:      &trainingv1.CPUResources{Nodes: snapshot.Resources.Nodes, ProcessesPerNode: snapshot.Resources.ProcessesPerNode, RequestMillicpu: snapshot.Resources.RequestMillicpu, LimitMillicpu: snapshot.Resources.LimitMillicpu, RequestMemoryBytes: snapshot.Resources.RequestMemoryBytes, LimitMemoryBytes: snapshot.Resources.LimitMemoryBytes},
		Environment: &modeldevv1.EnvironmentBindingSnapshot{
			BindingId: snapshot.Environment.BindingID, BindingDigest: snapshot.Environment.BindingDigest,
			ClusterId: snapshot.Environment.ClusterID, NamespaceName: snapshot.Environment.NamespaceName, NamespaceUid: snapshot.Environment.NamespaceUID,
			KfpConnectionRef: snapshot.Environment.KFPConnectionRef, ExperimentId: snapshot.Environment.ExperimentID,
			Identities: &modeldevv1.RuntimeIdentityRefs{ModeldevControlIdentityRef: snapshot.Environment.Identities.ModeldevControlIdentityRef, KfpStepServiceAccount: snapshot.Environment.Identities.KFPStepServiceAccount, TrainerServiceAccount: snapshot.Environment.Identities.TrainerServiceAccount, VerifierServiceAccount: snapshot.Environment.Identities.VerifierServiceAccount, TenantProxyIdentity: snapshot.Environment.Identities.TenantProxyIdentity},
		},
		Workspace:        &trainingv1.WorkspaceContract{Mode: trainingv1.WorkspaceMode(trainingv1.WorkspaceMode_value["WORKSPACE_MODE_"+snapshot.Workspace.Mode]), StorageClass: snapshot.Workspace.StorageClass, CapacityBytes: snapshot.Workspace.CapacityBytes, InputSubpath: snapshot.Workspace.InputSubpath, TrainingSubpath: snapshot.Workspace.TrainingSubpath, ReportsSubpath: snapshot.Workspace.ReportsSubpath, PublicationSubpath: snapshot.Workspace.PublicationSubpath, WorkSubpath: snapshot.Workspace.WorkSubpath},
		PublicationScope: &modeldevv1.StorageScope{StorageConnectionId: snapshot.PublicationScope.StorageConnectionID, Bucket: snapshot.PublicationScope.Bucket, ApprovedPrefix: snapshot.PublicationScope.ApprovedPrefix, CredentialReference: snapshot.PublicationScope.CredentialReference},
		OutputContract:   &trainingv1.OutputContract{SchemaVersion: snapshot.OutputContract.SchemaVersion, OutputKind: trainingv1.OutputKind(trainingv1.OutputKind_value["OUTPUT_KIND_"+snapshot.OutputContract.OutputKind]), DeliveryMode: trainingv1.DeliveryMode_DELIVERY_MODE_SAVE_ARTIFACTS, RequiredFiles: files, CreateTarBundle: snapshot.OutputContract.CreateTarBundle},
		DeadlineAt:       timestamppb.New(snapshot.DeadlineAt.UTC()),
	}
	if snapshot.TrainingImage != nil {
		message.TrainingImage = encodeTrainingImage(*snapshot.TrainingImage)
	}
	if snapshot.Finetuning != nil {
		message.Finetuning = encodeFinetuningSnapshot(*snapshot.Finetuning)
	}
	if snapshot.GPU != nil {
		message.Gpu = encodeGPUPlan(snapshot.GPU)
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
	if message.Limits == nil || message.Implementation == nil || message.IdentityRef == nil || message.ComputeOrigin == nil || message.Timing == nil || message.Timing.AcceptedAt == nil || message.Timing.ExecutionDeadlineAt == nil || message.RuntimePlan == nil || message.Input == nil || message.Input.Object == nil || message.Input.PreparedDataset == nil || message.Program == nil || message.Resources == nil || message.Environment == nil || message.Environment.Identities == nil || message.Workspace == nil || message.PublicationScope == nil || message.OutputContract == nil || message.DeadlineAt == nil {
		return cpup01.Snapshot{}, invalid("missing snapshot fields")
	}
	if err := message.DeadlineAt.CheckValid(); err != nil {
		return cpup01.Snapshot{}, invalid("deadline timestamp")
	}
	if message.Timing.AcceptedAt.CheckValid() != nil || message.Timing.ExecutionDeadlineAt.CheckValid() != nil {
		return cpup01.Snapshot{}, invalid("timing timestamp")
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
		files = append(files, cpup01.RequiredOutput{Role: strings.TrimPrefix(file.Role.String(), "FILE_ROLE_"), RelativePath: file.RelativePath, InTar: file.InTar})
	}
	snapshot := cpup01.Snapshot{
		SchemaVersion: message.SchemaVersion, Kind: strings.TrimPrefix(message.Kind.String(), "EXECUTION_KIND_"), DeliveryMode: strings.TrimPrefix(message.DeliveryMode.String(), "DELIVERY_MODE_"),
		Limits:         cpup01.ContentLimits{MaxContentBytes: message.Limits.MaxContentBytes},
		IdentityRef:    decodeIdentity(message.IdentityRef),
		Implementation: decodeImplementation(message.Implementation),
		ComputeOrigin:  decodeComputeOrigin(message.ComputeOrigin),
		Timing:         decodeTiming(message.Timing),
		RuntimePlan:    decodeRuntimePlan(message.RuntimePlan),
		Input:          cpup01.InputRef{InputVersionID: message.Input.InputVersionId, Object: object, Format: message.Input.Format, SchemaVersion: message.Input.SchemaVersion, RowCount: message.Input.RowCount, FeatureCount: message.Input.FeatureCount, PreparedDataset: cpup01.PreparedDatasetFacts{Format: message.Input.PreparedDataset.Format, TransformID: message.Input.PreparedDataset.TransformId, SizeBytes: message.Input.PreparedDataset.SizeBytes, SHA256: message.Input.PreparedDataset.Sha256, RowCount: message.Input.PreparedDataset.RowCount}},
		Program:        cpup01.ProgramRef{ImageID: message.Program.ImageId, ImageVersion: message.Program.ImageVersion, ImageScope: cpup01.ImageScope(message.Program.ImageScope), ImageDigest: message.Program.ImageDigest, Command: cloneStrings(message.Program.Command), ResolvedArgs: cloneStrings(message.Program.ResolvedArgs), ResolvedParameters: parameters},
		Resources:      cpup01.CPUResources{Nodes: message.Resources.Nodes, ProcessesPerNode: message.Resources.ProcessesPerNode, RequestMillicpu: message.Resources.RequestMillicpu, LimitMillicpu: message.Resources.LimitMillicpu, RequestMemoryBytes: message.Resources.RequestMemoryBytes, LimitMemoryBytes: message.Resources.LimitMemoryBytes},
		Environment: cpup01.EnvironmentBindingSnapshot{
			BindingID: message.Environment.BindingId, BindingDigest: message.Environment.BindingDigest, ClusterID: message.Environment.ClusterId,
			NamespaceName: message.Environment.NamespaceName, NamespaceUID: message.Environment.NamespaceUid, KFPConnectionRef: message.Environment.KfpConnectionRef, ExperimentID: message.Environment.ExperimentId,
			Identities: cpup01.RuntimeIdentityRefs{ModeldevControlIdentityRef: message.Environment.Identities.ModeldevControlIdentityRef, KFPStepServiceAccount: message.Environment.Identities.KfpStepServiceAccount, TrainerServiceAccount: message.Environment.Identities.TrainerServiceAccount, VerifierServiceAccount: message.Environment.Identities.VerifierServiceAccount, TenantProxyIdentity: message.Environment.Identities.TenantProxyIdentity},
		},
		Workspace:        cpup01.WorkspaceContract{Mode: strings.TrimPrefix(message.Workspace.Mode.String(), "WORKSPACE_MODE_"), StorageClass: message.Workspace.StorageClass, CapacityBytes: message.Workspace.CapacityBytes, InputSubpath: message.Workspace.InputSubpath, TrainingSubpath: message.Workspace.TrainingSubpath, ReportsSubpath: message.Workspace.ReportsSubpath, PublicationSubpath: message.Workspace.PublicationSubpath, WorkSubpath: message.Workspace.WorkSubpath},
		PublicationScope: cpup01.StorageScope{StorageConnectionID: message.PublicationScope.StorageConnectionId, Bucket: message.PublicationScope.Bucket, ApprovedPrefix: message.PublicationScope.ApprovedPrefix, CredentialReference: message.PublicationScope.CredentialReference},
		OutputContract:   cpup01.OutputContract{SchemaVersion: message.OutputContract.SchemaVersion, OutputKind: strings.TrimPrefix(message.OutputContract.OutputKind.String(), "OUTPUT_KIND_"), DeliveryMode: strings.TrimPrefix(message.OutputContract.DeliveryMode.String(), "DELIVERY_MODE_"), RequiredFiles: files, CreateTarBundle: message.OutputContract.CreateTarBundle},
		DeadlineAt:       message.DeadlineAt.AsTime(),
	}
	if message.TrainingImage != nil {
		facts, err := decodeTrainingImage(message.TrainingImage)
		if err != nil {
			return cpup01.Snapshot{}, err
		}
		snapshot.TrainingImage = &facts
	}
	if message.Finetuning != nil {
		fine, err := decodeFinetuningSnapshot(message.Finetuning)
		if err != nil {
			return cpup01.Snapshot{}, err
		}
		snapshot.Finetuning = &fine
	}
	if message.Gpu != nil {
		plan, err := decodeGPUPlan(message.Gpu)
		if err != nil {
			return cpup01.Snapshot{}, err
		}
		snapshot.GPU = plan
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

func encodeTrainingImage(facts cpup01.TrainingImageFacts) *modeldevv1.TrainingImageFacts {
	return &modeldevv1.TrainingImageFacts{
		RegistryAuthority: facts.RegistryAuthority,
		Platform:          &modeldevv1.TrainingPlatform{Os: facts.Platform.OS, Architecture: facts.Platform.Architecture, Variant: facts.Platform.Variant},
		ProtocolVersion:   facts.ProtocolVersion,
		PullSecretRef:     facts.PullSecretRef,
	}
}

func decodeTrainingImage(message *modeldevv1.TrainingImageFacts) (cpup01.TrainingImageFacts, error) {
	if message == nil || message.Platform == nil {
		return cpup01.TrainingImageFacts{}, invalid("training image")
	}
	return cpup01.TrainingImageFacts{
		RegistryAuthority: message.RegistryAuthority,
		Platform: cpup01.TrainingPlatform{
			OS:           message.Platform.Os,
			Architecture: message.Platform.Architecture,
			Variant:      message.Platform.Variant,
		},
		ProtocolVersion: message.ProtocolVersion,
		PullSecretRef:   message.PullSecretRef,
	}, nil
}

func invalid(reason string) error {
	return fmt.Errorf("%w: protobuf %s", cpup01.ErrInvalidArgument, reason)
}
