package biz

import (
	"context"
	"errors"
	"strconv"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

var ErrFinetuningNotEnabled = errors.New("FINETUNING_NOT_ENABLED")
var ErrModelDependencyUnavailable = errors.New("MODEL_DEPENDENCY_UNAVAILABLE")

// FinetuningModelFactsReader is a read-only Model boundary. A production
// implementation verifies tenant-owned READY source/target facts and fixed
// bytes. Governance rechecks the current user's Model rights before delegation.
// Isolated boundary fixtures are not evidence of those permissions or bytes.
type FinetuningModelFactsReader interface {
	ResolveFinetuningModelFacts(context.Context, string, cpup01.Intent, cpup01.ImplementationMaterial) (cpup01.BaseModelRef, error)
}

func NewFinetuningAdmissionResolver(materials AdmissionMaterialReader, inputs AdmissionInputReader, facts AdmissionFactsReader, models FinetuningModelFactsReader, limits cpup01.ContentLimits) *AdmissionResolver {
	return &AdmissionResolver{materials: materials, inputs: inputs, facts: facts, models: models, limits: limits}
}

func verifiedAdmissionInput(v InputVersion, kind string) (cpup01.InputRef, error) {
	if v.State != InputStateReady || v.Failure != nil {
		return cpup01.InputRef{}, ErrAdmissionInputNotReady
	}
	ref := cpup01.InputRef{InputVersionID: v.Import.InputVersionID, Object: v.Import.Object, Format: v.Import.InputFormat()}
	switch kind {
	case "GENERAL_TRAINING":
		if v.Verification == nil || v.AlpacaVerification != nil || v.Verification.ValidateFor(v.Import) != nil {
			return cpup01.InputRef{}, ErrAdmissionInputNotReady
		}
		ref.SchemaVersion, ref.RowCount, ref.FeatureCount = v.Verification.SchemaVersion, v.Verification.RowCount, v.Verification.FeatureCount
		ref.PreparedDataset = v.Verification.PreparedDataset
	case "FINETUNING":
		if v.AlpacaVerification == nil || v.Verification != nil || v.AlpacaVerification.ValidateFor(v.Import) != nil {
			return cpup01.InputRef{}, ErrAdmissionInputNotReady
		}
		ref.SchemaVersion, ref.RowCount = v.AlpacaVerification.SchemaVersion, v.AlpacaVerification.RowCount
		ref.PreparedDataset = v.AlpacaVerification.PreparedDataset
	default:
		return cpup01.InputRef{}, ErrImplementationUnavailable
	}
	return ref, nil
}

func (r *AdmissionResolver) resolveFinetuningModelFacts(ctx context.Context, tenant string, intent cpup01.Intent, material cpup01.ImplementationMaterial, parameters []cpup01.Parameter, input cpup01.InputRef, device string) (*cpup01.FinetuningSnapshot, error) {
	if r.models == nil || material.Finetuning == nil || intent.BaseModelVersionID == nil {
		return nil, ErrFinetuningNotEnabled
	}
	base, err := r.models.ResolveFinetuningModelFacts(ctx, tenant, intent, material)
	if err != nil {
		return nil, err
	}
	if base.VersionID != *intent.BaseModelVersionID {
		return nil, ErrImplementationUnavailable
	}
	plan, err := ResolveFinetuningTemplate(intent, base, material)
	if err != nil {
		return nil, err
	}
	fine := material.Finetuning
	settings := fine.ManagedSettings
	for _, p := range parameters {
		v, _ := strconv.ParseInt(p.Value, 10, 64)
		switch p.Name {
		case "lora_rank":
			settings.LoraRank = v
		case "lora_alpha":
			settings.LoraAlpha = v
		case "cutoff_len":
			if v > base.Facts.ContextLimit {
				return nil, ErrContextLimitExceeded
			}
			settings.CutoffLen = v
		case "per_device_train_batch_size":
			settings.PerDeviceTrainBatchSize = v
		case "gradient_accumulation_steps":
			settings.GradientAccumulationSteps = v
		case "seed":
			settings.Seed = v
			settings.DataSeed = v
		case "lora_dropout":
			settings.LoraDropout = p.Value
		}
	}
	settings.Device = "cpu"
	if device == "gpu" {
		settings.Device = "cuda"
	}
	configuration := cpup01.FinetuningConfiguration{ParameterContractVersion: material.Implementation.ParameterContractVersion, Parameters: parameters, ManagedSettings: settings}
	digest, err := configuration.Digest()
	if err != nil {
		return nil, ErrImplementationUnavailable
	}
	exportArgs, err := resolveFinetuningArguments(fine.ExportProgram.ArgsTemplate, input, base, plan)
	if err != nil {
		return nil, err
	}
	verifyArgs, err := resolveFinetuningArguments(fine.VerifyProgram.ArgsTemplate, input, base, plan)
	if err != nil {
		return nil, err
	}
	return &cpup01.FinetuningSnapshot{ParameterContractVersion: material.Implementation.ParameterContractVersion, BaseModel: base, TargetModelID: intent.TargetModelID, NewModelName: intent.NewModelName, ManagedSettings: settings, ResolvedConfigurationSHA256: digest,
		ExportProgram: cpup01.ProgramRef{ImageID: fine.ExportProgram.ImageID, ImageVersion: fine.ExportProgram.ImageVersion, ImageScope: fine.ExportProgram.ImageScope, ImageDigest: fine.ExportProgram.ImageDigest, Command: fine.ExportProgram.Command, ResolvedArgs: exportArgs},
		VerifyProgram: cpup01.ProgramRef{ImageID: fine.VerifyProgram.ImageID, ImageVersion: fine.VerifyProgram.ImageVersion, ImageScope: fine.VerifyProgram.ImageScope, ImageDigest: fine.VerifyProgram.ImageDigest, Command: fine.VerifyProgram.Command, ResolvedArgs: verifyArgs},
		RecipeSHA256:  fine.RecipeSHA256, MappingSHA256: fine.MappingSHA256, RuntimeCapabilities: fine.RuntimeCapabilities, TemplatePlan: plan}, nil
}

func resolveFinetuningArguments(template []cpup01.MaterialArgument, input cpup01.InputRef, base cpup01.BaseModelRef, plan cpup01.TemplatePlan) ([]string, error) {
	baseDigest, err := base.BaseManifestSHA256()
	if err != nil {
		return nil, ErrImplementationUnavailable
	}
	manifest, err := cpup01.BuildDatasetManifest(input)
	if err != nil {
		return nil, ErrImplementationUnavailable
	}
	digest, err := manifest.Digest()
	if err != nil {
		return nil, ErrImplementationUnavailable
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return nil, ErrImplementationUnavailable
	}
	values := map[string]string{"DATASET_MANIFEST_SHA256": digest, "BASE_MANIFEST_SHA256": baseDigest, "TEMPLATE_PLAN_SHA256": planDigest}
	args := make([]string, 0, len(template))
	for _, a := range template {
		if (a.Literal == "") == (a.Source == "") {
			return nil, ErrImplementationUnavailable
		}
		value := a.Literal
		if a.Source != "" {
			value = values[a.Source]
			if value == "" {
				return nil, ErrImplementationUnavailable
			}
		}
		args = append(args, value)
	}
	return args, nil
}
