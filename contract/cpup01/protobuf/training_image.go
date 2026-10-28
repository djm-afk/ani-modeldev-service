package protobuf

import (
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// programParameters returns the resolved parameters of whichever program
// representation the message carries. Exactly one is present.
func programParameters(message *modeldevv1.ExecutionSnapshot) []*trainingv1.Parameter {
	if message.Program != nil {
		return message.Program.ResolvedParameters
	}
	if message.TrainingProgram != nil {
		return message.TrainingProgram.ResolvedParameters
	}
	return nil
}

// programDescriptor exposes the single present program as an any so the
// decoder can branch without a second kind check.
func programDescriptor(message *modeldevv1.ExecutionSnapshot) any {
	if message.Program != nil {
		return message.Program
	}
	if message.TrainingProgram != nil {
		return message.TrainingProgram
	}
	return nil
}

func encodeTrainingImageSelection(selection cpup01.TrainingImageSelection) *trainingv1.TrainingImageSelection {
	return &trainingv1.TrainingImageSelection{
		ImageId:           selection.ImageID,
		Scope:             selection.Scope,
		RegistryAuthority: selection.RegistryAuthority,
		Digest:            selection.Digest,
		Reference:         selection.Reference,
		Platform:          &trainingv1.TrainingPlatform{Os: selection.Platform.OS, Architecture: selection.Platform.Architecture, Variant: selection.Platform.Variant},
		ProtocolVersion:   selection.ProtocolVersion,
		PullSecretRef:     selection.PullSecretRef,
	}
}

func decodeTrainingImageSelection(message *trainingv1.TrainingImageSelection) cpup01.TrainingImageSelection {
	if message == nil {
		return cpup01.TrainingImageSelection{}
	}
	platform := message.GetPlatform()
	return cpup01.TrainingImageSelection{
		ImageID:           message.ImageId,
		Scope:             message.Scope,
		RegistryAuthority: message.RegistryAuthority,
		Digest:            message.Digest,
		Reference:         message.Reference,
		Platform:          cpup01.TrainingPlatform{OS: platform.GetOs(), Architecture: platform.GetArchitecture(), Variant: platform.GetVariant()},
		ProtocolVersion:   message.ProtocolVersion,
		PullSecretRef:     message.PullSecretRef,
	}
}
