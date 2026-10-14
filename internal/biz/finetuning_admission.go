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
// implementation must verify current tenant/base use and target/create rights,
// approval/protection and fixed bytes. Isolated tests explicitly replace only
// this provider boundary; their fixture is not evidence of Model permissions.
type FinetuningModelFactsReader interface {
	ResolveFinetuningModelFacts(context.Context, string, cpup01.Intent, cpup01.ReleaseDocument) (cpup01.BaseModelRef, error)
}

func NewFinetuningAdmissionResolver(releases AdmissionReleaseReader, inputs AdmissionInputReader, facts AdmissionFactsReader, models FinetuningModelFactsReader) *AdmissionResolver {
	return &AdmissionResolver{releases: releases, inputs: inputs, facts: facts, models: models}
}

func supportedAdmissionProgram(release cpup01.ReleaseDocument) bool {
	switch release.Kind {
	case "GENERAL_TRAINING":
		return release.Program.ParameterContractVersion == "ani.cpu.mlp.parameters.v1" && release.Finetuning == nil
	case "FINETUNING":
		return release.Program.ParameterContractVersion == cpup01.FinetuningParameterContractVersion && release.Finetuning != nil
	default:
		return false
	}
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
	case "FINETUNING":
		if v.AlpacaVerification == nil || v.Verification != nil || v.AlpacaVerification.ValidateFor(v.Import) != nil || v.AlpacaVerification.RowCount != 64 {
			return cpup01.InputRef{}, ErrAdmissionInputNotReady
		}
		ref.SchemaVersion, ref.RowCount = v.AlpacaVerification.SchemaVersion, v.AlpacaVerification.RowCount
	default:
		return cpup01.InputRef{}, ErrNoCompatibleRelease
	}
	return ref, nil
}

func (r *AdmissionResolver) resolveFinetuningModelFacts(ctx context.Context, tenant string, intent cpup01.Intent, release cpup01.ReleaseDocument, parameters []cpup01.Parameter, input cpup01.FixedObjectRef) (*cpup01.FinetuningSnapshot, error) {
	if r.models == nil || release.Finetuning == nil || intent.BaseModelVersionID == nil || *intent.BaseModelVersionID != release.Finetuning.BaseModelVersionID {
		return nil, ErrFinetuningNotEnabled
	}
	base, err := r.models.ResolveFinetuningModelFacts(ctx, tenant, intent, release)
	if err != nil {
		return nil, err
	}
	if base.VersionID != *intent.BaseModelVersionID {
		return nil, ErrNoCompatibleRelease
	}
	fine := release.Finetuning
	configuration := cpup01.FinetuningConfiguration{ParameterContractVersion: release.Program.ParameterContractVersion, Parameters: parameters, ManagedSettings: fine.ManagedSettings}
	digest, err := configuration.Digest()
	if err != nil {
		return nil, ErrNoCompatibleRelease
	}
	exportArgs, err := resolveFinetuningArguments(fine.ExportProgram.ArgsTemplate, input, base)
	if err != nil {
		return nil, err
	}
	verifyArgs, err := resolveFinetuningArguments(fine.VerifyProgram.ArgsTemplate, input, base)
	if err != nil {
		return nil, err
	}
	return &cpup01.FinetuningSnapshot{ParameterContractVersion: release.Program.ParameterContractVersion, BaseModel: base, TargetModelID: intent.TargetModelID, NewModelName: intent.NewModelName, ManagedSettings: fine.ManagedSettings, ResolvedConfigurationSHA256: digest,
		ExportProgram: cpup01.ProgramRef{ImageVersionID: fine.ExportProgram.ImageVersionID, ImageDigest: fine.ExportProgram.ImageDigest, Command: fine.ExportProgram.Command, ResolvedArgs: exportArgs},
		VerifyProgram: cpup01.ProgramRef{ImageVersionID: fine.VerifyProgram.ImageVersionID, ImageDigest: fine.VerifyProgram.ImageDigest, Command: fine.VerifyProgram.Command, ResolvedArgs: verifyArgs},
		RecipeSHA256:  fine.RecipeSHA256, MappingSHA256: fine.MappingSHA256, Budgets: fine.Budgets}, nil
}

func resolveFinetuningArguments(template []cpup01.ReleaseArgument, input cpup01.FixedObjectRef, base cpup01.BaseModelRef) ([]string, error) {
	baseDigest, err := base.BaseManifestSHA256()
	if err != nil {
		return nil, ErrNoCompatibleRelease
	}
	values := map[string]string{"INPUT_SHA256": input.SHA256, "INPUT_BYTES": strconv.FormatInt(input.SizeBytes, 10), "BASE_MANIFEST_SHA256": baseDigest}
	args := make([]string, 0, len(template))
	for _, a := range template {
		if (a.Literal == "") == (a.Source == "") {
			return nil, ErrNoCompatibleRelease
		}
		value := a.Literal
		if a.Source != "" {
			value = values[a.Source]
			if value == "" {
				return nil, ErrNoCompatibleRelease
			}
		}
		args = append(args, value)
	}
	return args, nil
}
