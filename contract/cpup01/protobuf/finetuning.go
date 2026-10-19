package protobuf

import (
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// EncodeParameters preserves actual numeric values and rejects overflow and
// unsupported types. Query projections share this transport conversion.
func EncodeParameters(parameters []cpup01.Parameter) ([]*trainingv1.Parameter, error) {
	return encodeParameters(parameters)
}

func EncodeManagedSettings(value cpup01.FinetuningManagedSettings) *modeldevv1.FinetuningManagedSettings {
	return &modeldevv1.FinetuningManagedSettings{
		Stage: value.Stage, FinetuningType: value.FinetuningType, LoraTarget: value.LoraTarget, LoraRank: value.LoraRank, LoraAlpha: value.LoraAlpha, CutoffLen: value.CutoffLen, PerDeviceTrainBatchSize: value.PerDeviceTrainBatchSize, GradientAccumulationSteps: value.GradientAccumulationSteps, Seed: value.Seed, DataSeed: value.DataSeed, Device: value.Device, Fp16: value.FP16, Bf16: value.BF16, FlashAttn: value.FlashAttn, ReportTo: value.ReportTo, SaveStrategy: value.SaveStrategy, Template: value.Template, TrainDtype: value.TrainDtype, ExportDtype: value.ExportDtype, VerifyDtype: value.VerifyDtype, LrSchedulerType: value.LRSchedulerType, WarmupRatio: value.WarmupRatio, WeightDecay: value.WeightDecay, LoraDropout: value.LoraDropout, NumTrainEpochs: value.NumTrainEpochs, Optim: value.Optim, LoggingSteps: value.LoggingSteps,
	}
}

func decodeManagedSettings(value *modeldevv1.FinetuningManagedSettings) cpup01.FinetuningManagedSettings {
	return cpup01.FinetuningManagedSettings{
		Stage: value.Stage, FinetuningType: value.FinetuningType, LoraTarget: value.LoraTarget, LoraRank: value.LoraRank, LoraAlpha: value.LoraAlpha, CutoffLen: value.CutoffLen, PerDeviceTrainBatchSize: value.PerDeviceTrainBatchSize, GradientAccumulationSteps: value.GradientAccumulationSteps, Seed: value.Seed, DataSeed: value.DataSeed, Device: value.Device, FP16: value.Fp16, BF16: value.Bf16, FlashAttn: value.FlashAttn, ReportTo: value.ReportTo, SaveStrategy: value.SaveStrategy, Template: value.Template, TrainDtype: value.TrainDtype, ExportDtype: value.ExportDtype, VerifyDtype: value.VerifyDtype, LRSchedulerType: value.LrSchedulerType, WarmupRatio: value.WarmupRatio, WeightDecay: value.WeightDecay, LoraDropout: value.LoraDropout, NumTrainEpochs: value.NumTrainEpochs, Optim: value.Optim, LoggingSteps: value.LoggingSteps,
	}
}

func encodeFixedObject(object cpup01.FixedObjectRef) *trainingv1.FixedObjectRef {
	message := &trainingv1.FixedObjectRef{StorageConnectionId: object.StorageConnectionID, Bucket: object.Bucket, Key: object.Key, SizeBytes: object.SizeBytes, Sha256: object.SHA256}
	if object.VersionID != nil {
		message.Immutability = &trainingv1.FixedObjectRef_VersionId{VersionId: *object.VersionID}
	} else if object.ImmutableCopy != nil {
		message.Immutability = &trainingv1.FixedObjectRef_ImmutableCopy{ImmutableCopy: *object.ImmutableCopy}
	}
	return message
}

func decodeFixedObject(message *trainingv1.FixedObjectRef) (cpup01.FixedObjectRef, error) {
	if message == nil {
		return cpup01.FixedObjectRef{}, invalid("fixed model object")
	}
	object := cpup01.FixedObjectRef{StorageConnectionID: message.StorageConnectionId, Bucket: message.Bucket, Key: message.Key, SizeBytes: message.SizeBytes, SHA256: message.Sha256}
	switch value := message.Immutability.(type) {
	case *trainingv1.FixedObjectRef_VersionId:
		if value == nil {
			return cpup01.FixedObjectRef{}, invalid("model immutability")
		}
		object.VersionID = cloneString(&value.VersionId)
	case *trainingv1.FixedObjectRef_ImmutableCopy:
		if value == nil {
			return cpup01.FixedObjectRef{}, invalid("model immutability")
		}
		object.ImmutableCopy = cloneBool(&value.ImmutableCopy)
	default:
		return cpup01.FixedObjectRef{}, invalid("model immutability")
	}
	return object, nil
}

func encodeProgram(value cpup01.ProgramRef) *trainingv1.ProgramRef {
	parameters, _ := encodeParameters(value.ResolvedParameters)
	return &trainingv1.ProgramRef{ImageVersionId: value.ImageVersionID, ImageDigest: value.ImageDigest, Command: cloneStrings(value.Command), ResolvedArgs: cloneStrings(value.ResolvedArgs), ResolvedParameters: parameters}
}

func decodeProgram(value *trainingv1.ProgramRef) (cpup01.ProgramRef, error) {
	if value == nil {
		return cpup01.ProgramRef{}, invalid("component program")
	}
	parameters, err := decodeParameters(value.ResolvedParameters)
	if err != nil {
		return cpup01.ProgramRef{}, err
	}
	return cpup01.ProgramRef{ImageVersionID: value.ImageVersionId, ImageDigest: value.ImageDigest, Command: cloneStrings(value.Command), ResolvedArgs: cloneStrings(value.ResolvedArgs), ResolvedParameters: parameters}, nil
}

func encodeFinetuningSnapshot(value cpup01.FinetuningSnapshot) *modeldevv1.FinetuningSnapshot {
	files := make([]*modeldevv1.BaseModelFile, 0, len(value.BaseModel.Files))
	for _, file := range value.BaseModel.Files {
		files = append(files, &modeldevv1.BaseModelFile{Path: file.Path, SizeBytes: file.SizeBytes, Sha256: file.SHA256})
	}
	return &modeldevv1.FinetuningSnapshot{
		ParameterContractVersion: value.ParameterContractVersion, BaseModel: &modeldevv1.BaseModelRef{ModelId: value.BaseModel.ModelID, VersionId: value.BaseModel.VersionID, Family: value.BaseModel.Family, Format: value.BaseModel.Format, Object: encodeFixedObject(value.BaseModel.Object), Scope: &modeldevv1.StorageScope{StorageConnectionId: value.BaseModel.Scope.StorageConnectionID, Bucket: value.BaseModel.Scope.Bucket, ApprovedPrefix: value.BaseModel.Scope.ApprovedPrefix, CredentialReference: value.BaseModel.Scope.CredentialReference}, Files: files},
		TargetModelId: cloneString(value.TargetModelID), NewModelName: cloneString(value.NewModelName), ManagedSettings: EncodeManagedSettings(value.ManagedSettings), ResolvedConfigurationSha256: value.ResolvedConfigurationSHA256, ExportProgram: encodeProgram(value.ExportProgram), VerifyProgram: encodeProgram(value.VerifyProgram), RecipeSha256: value.RecipeSHA256, MappingSha256: value.MappingSHA256,
		Budgets: &modeldevv1.ComponentBudgets{TrainingTimeoutSeconds: value.Budgets.TrainingTimeoutSeconds, PrepareTimeoutSeconds: value.Budgets.PrepareTimeoutSeconds, ExportTimeoutSeconds: value.Budgets.ExportTimeoutSeconds, VerifyTimeoutSeconds: value.Budgets.VerifyTimeoutSeconds, CollectTimeoutSeconds: value.Budgets.CollectTimeoutSeconds, PublishTimeoutSeconds: value.Budgets.PublishTimeoutSeconds, RpcTimeoutSeconds: value.Budgets.RPCTimeoutSeconds, MaxBaseModelBytes: value.Budgets.MaxBaseModelBytes, MaxBaseModelExpandedBytes: value.Budgets.MaxBaseModelExpandedBytes, MaxSpoolBytes: value.Budgets.MaxSpoolBytes},
	}
}

func decodeFinetuningSnapshot(message *modeldevv1.FinetuningSnapshot) (cpup01.FinetuningSnapshot, error) {
	if message.BaseModel == nil || message.BaseModel.Scope == nil || message.ManagedSettings == nil || message.Budgets == nil {
		return cpup01.FinetuningSnapshot{}, invalid("missing finetuning snapshot fields")
	}
	object, err := decodeFixedObject(message.BaseModel.Object)
	if err != nil {
		return cpup01.FinetuningSnapshot{}, err
	}
	export, err := decodeProgram(message.ExportProgram)
	if err != nil {
		return cpup01.FinetuningSnapshot{}, err
	}
	verify, err := decodeProgram(message.VerifyProgram)
	if err != nil {
		return cpup01.FinetuningSnapshot{}, err
	}
	files := make([]cpup01.BaseModelFile, 0, len(message.BaseModel.Files))
	for _, file := range message.BaseModel.Files {
		if file == nil {
			return cpup01.FinetuningSnapshot{}, invalid("base model files")
		}
		files = append(files, cpup01.BaseModelFile{Path: file.Path, SizeBytes: file.SizeBytes, SHA256: file.Sha256})
	}
	return cpup01.FinetuningSnapshot{
		ParameterContractVersion: message.ParameterContractVersion, BaseModel: cpup01.BaseModelRef{ModelID: message.BaseModel.ModelId, VersionID: message.BaseModel.VersionId, Family: message.BaseModel.Family, Format: message.BaseModel.Format, Object: object, Scope: cpup01.StorageScope{StorageConnectionID: message.BaseModel.Scope.StorageConnectionId, Bucket: message.BaseModel.Scope.Bucket, ApprovedPrefix: message.BaseModel.Scope.ApprovedPrefix, CredentialReference: message.BaseModel.Scope.CredentialReference}, Files: files},
		TargetModelID: cloneString(message.TargetModelId), NewModelName: cloneString(message.NewModelName), ManagedSettings: decodeManagedSettings(message.ManagedSettings), ResolvedConfigurationSHA256: message.ResolvedConfigurationSha256, ExportProgram: export, VerifyProgram: verify, RecipeSHA256: message.RecipeSha256, MappingSHA256: message.MappingSha256,
		Budgets: cpup01.ComponentBudgets{TrainingTimeoutSeconds: message.Budgets.TrainingTimeoutSeconds, PrepareTimeoutSeconds: message.Budgets.PrepareTimeoutSeconds, ExportTimeoutSeconds: message.Budgets.ExportTimeoutSeconds, VerifyTimeoutSeconds: message.Budgets.VerifyTimeoutSeconds, CollectTimeoutSeconds: message.Budgets.CollectTimeoutSeconds, PublishTimeoutSeconds: message.Budgets.PublishTimeoutSeconds, RPCTimeoutSeconds: message.Budgets.RpcTimeoutSeconds, MaxBaseModelBytes: message.Budgets.MaxBaseModelBytes, MaxBaseModelExpandedBytes: message.Budgets.MaxBaseModelExpandedBytes, MaxSpoolBytes: message.Budgets.MaxSpoolBytes},
	}, nil
}

// EncodeFinetuningParameterRules derives a safe view from the validated exact
// Release. A single-value rule is read-only; its explicit value remains legal.
func EncodeFinetuningParameterRules(program cpup01.ReleaseProgram) ([]*modeldevv1.ParameterRule, error) {
	if err := cpup01.ValidateFinetuningProgramParameters(program); err != nil {
		return nil, err
	}
	result := make([]*modeldevv1.ParameterRule, 0, len(program.ParameterRules))
	for _, rule := range program.ParameterRules {
		allowed, err := encodeParameters(rule.AllowedValues)
		if err != nil {
			return nil, err
		}
		view := &modeldevv1.ParameterRule{Name: rule.Name, Type: modeldevv1.ParameterType(modeldevv1.ParameterType_value["PARAMETER_TYPE_"+rule.Type]), Required: rule.Required, AllowedValues: allowed, MinimumInclusive: cloneBool(rule.MinimumInclusive), MaximumInclusive: cloneBool(rule.MaximumInclusive), Editable: len(rule.AllowedValues) != 1}
		if !view.Editable {
			view.NonEditableReason = "PLATFORM_FIXED"
		}
		for _, parameter := range program.DefaultParameters {
			if parameter.Name == rule.Name {
				values, err := encodeParameters([]cpup01.Parameter{parameter})
				if err != nil {
					return nil, err
				}
				view.DefaultValue = values[0]
			}
		}
		if rule.Minimum != nil {
			values, err := encodeParameters([]cpup01.Parameter{*rule.Minimum})
			if err != nil {
				return nil, err
			}
			view.Minimum = values[0]
		}
		if rule.Maximum != nil {
			values, err := encodeParameters([]cpup01.Parameter{*rule.Maximum})
			if err != nil {
				return nil, err
			}
			view.Maximum = values[0]
		}
		result = append(result, view)
	}
	return result, nil
}
