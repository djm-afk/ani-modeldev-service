package service

import (
	"context"
	"sort"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

func (q *Query) selectedPresets(ctx context.Context, scope GovernanceDelivery, selections []*modeldevv1.PresetReleaseSelection, limit int, after string) (*modeldevv1.ListPresetsResponse, error) {
	if len(selections) > 100 {
		return nil, materialsError(biz.ErrInvalidAdmission, scope.RequestID)
	}
	seen := map[string]bool{}
	presets := map[string]*modeldevv1.PresetView{}
	for _, selection := range selections {
		if selection == nil || !validMaterialMessage(selection) || !queryID(selection.PresetId) || !queryID(selection.ReleaseId) || !runtimeSHA256(selection.ReleaseDigest) || selection.BindingGeneration == 0 || seen[selection.PresetId] {
			return nil, materialsError(biz.ErrInvalidAdmission, scope.RequestID)
		}
		seen[selection.PresetId] = true
		release, err := q.materials.Release(ctx, scope.TenantID, selection.PresetId, selection.ReleaseId, selection.ReleaseDigest)
		if err != nil {
			return nil, materialsError(err, scope.RequestID)
		}
		p, err := selectedPresetView(release, selection)
		if err != nil {
			return nil, materialsError(err, scope.RequestID)
		}
		presets[p.PresetId] = p
	}
	ids := make([]string, 0, len(presets))
	for id := range presets {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	response := &modeldevv1.ListPresetsResponse{}
	if len(ids) > limit {
		ids = ids[:limit]
		response.NextPageToken = ids[len(ids)-1]
	}
	for _, id := range ids {
		response.Presets = append(response.Presets, presets[id])
	}
	return response, nil
}

func selectedPresetView(release cpup01.ReleaseDocument, selection *modeldevv1.PresetReleaseSelection) (*modeldevv1.PresetView, error) {
	p := &modeldevv1.PresetView{PresetId: release.PresetID, ActiveReleaseId: release.ReleaseID, ReleaseDigest: selection.ReleaseDigest, BindingGeneration: selection.BindingGeneration, ParameterContractVersion: release.Program.ParameterContractVersion, ImageVersionIds: []string{release.Program.ImageVersionID}}
	defaults := map[string]cpup01.Parameter{}
	for _, d := range release.Program.DefaultParameters {
		defaults[d.Name] = d
	}
	if release.Kind == "GENERAL_TRAINING" {
		p.Name = "General CPU MLP"
		p.Kind = trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING
		for _, d := range release.Program.DefaultParameters {
			v := parameterView(d)
			if v == nil {
				return nil, biz.ErrPersistence
			}
			rule := &modeldevv1.ParameterRule{Name: d.Name, DefaultValue: v}
			if d.Type == "INTEGER" {
				rule.Type = modeldevv1.ParameterType_PARAMETER_TYPE_INTEGER
				rule.AllowedValues = []*trainingv1.Parameter{v}
				rule.NonEditableReason = "platform fixed"
			} else {
				rule.Type = modeldevv1.ParameterType_PARAMETER_TYPE_DECIMAL
				rule.Maximum = parameterView(cpup01.Parameter{Name: d.Name, Type: "DECIMAL", Value: "0.1"})
				rule.Editable = true
			}
			p.ParameterRules = append(p.ParameterRules, rule)
		}
		return p, nil
	}
	if release.Kind != "FINETUNING" || release.Finetuning == nil {
		return nil, biz.ErrNoCompatibleRelease
	}
	if err := cpup01.ValidateFinetuningProgramParameters(release.Program); err != nil {
		return nil, err
	}
	p.Name = "Managed CPU LoRA SFT"
	p.Kind = trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING
	for _, rule := range release.Program.ParameterRules {
		d, ok := defaults[rule.Name]
		if !ok {
			return nil, biz.ErrPersistence
		}
		wire := &modeldevv1.ParameterRule{Name: rule.Name, Required: rule.Required, DefaultValue: parameterView(d), MinimumInclusive: rule.MinimumInclusive, MaximumInclusive: rule.MaximumInclusive, Editable: true}
		if wire.DefaultValue == nil {
			return nil, biz.ErrPersistence
		}
		if rule.Type == "INTEGER" {
			wire.Type = modeldevv1.ParameterType_PARAMETER_TYPE_INTEGER
		} else {
			wire.Type = modeldevv1.ParameterType_PARAMETER_TYPE_DECIMAL
		}
		for _, v := range rule.AllowedValues {
			value := parameterView(v)
			if value == nil {
				return nil, biz.ErrPersistence
			}
			wire.AllowedValues = append(wire.AllowedValues, value)
		}
		if len(wire.AllowedValues) == 1 {
			wire.Editable = false
			wire.NonEditableReason = "single approved value"
		}
		if rule.Minimum != nil {
			wire.Minimum = parameterView(*rule.Minimum)
			if wire.Minimum == nil {
				return nil, biz.ErrPersistence
			}
		}
		if rule.Maximum != nil {
			wire.Maximum = parameterView(*rule.Maximum)
			if wire.Maximum == nil {
				return nil, biz.ErrPersistence
			}
		}
		p.ParameterRules = append(p.ParameterRules, wire)
	}
	for _, set := range release.Program.AllowedParameterSets {
		wire := &modeldevv1.ParameterSelection{}
		for _, v := range set {
			value := parameterView(v)
			if value == nil {
				return nil, biz.ErrPersistence
			}
			wire.Values = append(wire.Values, value)
		}
		p.AllowedParameterSets = append(p.AllowedParameterSets, wire)
	}
	// Project only the typed safe fixed settings through the shared codec. The
	// storage/base archive and command information are never returned here.
	p.ManagedSettings = contractpb.EncodeManagedSettings(release.Finetuning.ManagedSettings)
	return p, nil
}
