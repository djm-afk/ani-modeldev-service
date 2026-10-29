// Package finetuningmodel reads facts only from the formal Model owner.
package finetuningmodel

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

type ModelFactsClient interface {
	ResolveFinetuningFacts(context.Context, string, *modelv1.ResolveFinetuningFactsRequest) (*modelv1.ResolveFinetuningFactsResponse, error)
}
type LiveReader struct {
	client ModelFactsClient
	scope  cpup01.StorageScope
	limits cpup01.ContentLimits
}

var _ biz.FinetuningModelFactsReader = (*LiveReader)(nil)
var ErrInvalidModelFacts = errors.New("INVALID_MODEL_FACTS_CONFIGURATION")

func NewLive(client ModelFactsClient, scope cpup01.StorageScope, limits cpup01.ContentLimits) (*LiveReader, error) {
	if client == nil || scope.Validate() != nil || limits.Validate() != nil {
		return nil, ErrInvalidModelFacts
	}
	return &LiveReader{client: client, scope: scope, limits: limits}, nil
}
func (r *LiveReader) ResolveFinetuningModelFacts(ctx context.Context, tenant string, intent cpup01.Intent, material cpup01.ImplementationMaterial) (cpup01.BaseModelRef, error) {
	if err := ctx.Err(); err != nil {
		return cpup01.BaseModelRef{}, err
	}
	id, err := uuid.Parse(tenant)
	if r == nil || r.client == nil || err != nil || id == uuid.Nil || id.String() != tenant || intent.Kind != "FINETUNING" || intent.BaseModelVersionID == nil || material.Finetuning == nil {
		return cpup01.BaseModelRef{}, biz.ErrFinetuningNotEnabled
	}
	if _, _, err := cpup01.CanonicalIntent(intent); err != nil {
		return cpup01.BaseModelRef{}, biz.ErrInvalidAdmission
	}
	in := &modelv1.ResolveFinetuningFactsRequest{TenantId: tenant, BaseModelVersionId: *intent.BaseModelVersionID}
	if intent.TargetModelID != nil {
		in.Target = &modelv1.ResolveFinetuningFactsRequest_TargetModelId{TargetModelId: *intent.TargetModelID}
	} else if intent.NewModelName != nil {
		in.Target = &modelv1.ResolveFinetuningFactsRequest_NewModelName{NewModelName: *intent.NewModelName}
	}
	return r.resolve(ctx, tenant, in, material)
}

// ResolveFinetuningSourceFacts serves read-only readiness without fabricating a
// training Intent or reserving any registration target.
func (r *LiveReader) ResolveFinetuningSourceFacts(ctx context.Context, tenant, version string, material cpup01.ImplementationMaterial) (cpup01.BaseModelRef, error) {
	if err := ctx.Err(); err != nil {
		return cpup01.BaseModelRef{}, err
	}
	id, err := uuid.Parse(tenant)
	base, e := uuid.Parse(version)
	if r == nil || r.client == nil || err != nil || id == uuid.Nil || id.String() != tenant || e != nil || base == uuid.Nil || base.String() != version || material.Finetuning == nil {
		return cpup01.BaseModelRef{}, biz.ErrInvalidAdmission
	}
	return r.resolve(ctx, tenant, &modelv1.ResolveFinetuningFactsRequest{TenantId: tenant, BaseModelVersionId: version}, material)
}
func (r *LiveReader) resolve(ctx context.Context, tenant string, in *modelv1.ResolveFinetuningFactsRequest, material cpup01.ImplementationMaterial) (cpup01.BaseModelRef, error) {
	out, err := r.client.ResolveFinetuningFacts(ctx, tenant, in)
	if err != nil {
		return cpup01.BaseModelRef{}, err
	}
	if out.ValidateFor(in) != nil || out.Bucket != r.scope.Bucket || !strings.HasPrefix(out.ObjectKey, r.scope.ApprovedPrefix+"/") {
		return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
	}
	tarBound, err := cpup01.TarReadBound(r.limits.MaxContentBytes)
	if err != nil {
		return cpup01.BaseModelRef{}, err
	}
	if out.SizeBytes > tarBound {
		return cpup01.BaseModelRef{}, &cpup01.LimitViolation{Reason: "STRUCTURE_LIMIT_EXCEEDED", Kind: "base_model", LimitName: "tar_read_bound", Actual: out.SizeBytes, Limit: tarBound}
	}
	version := out.ObjectVersionId
	base := cpup01.BaseModelRef{ModelID: out.ModelId, VersionID: out.ModelVersionId, Format: out.Format, Scope: r.scope, Object: cpup01.FixedObjectRef{StorageConnectionID: r.scope.StorageConnectionID, Bucket: out.Bucket, Key: out.ObjectKey, VersionID: &version, SizeBytes: out.SizeBytes, SHA256: out.Sha256}}
	var logical int64
	for _, member := range out.Files {
		logical, err = cpup01.CheckedAdd(logical, member.SizeBytes)
		if err != nil {
			return cpup01.BaseModelRef{}, biz.ErrBaseModelInvalid
		}
		base.Files = append(base.Files, cpup01.BaseModelFile{Path: member.Path, SizeBytes: member.SizeBytes, SHA256: member.Sha256})
	}
	if err := r.limits.Check(cpup01.ContentBaseModel, logical); err != nil {
		return cpup01.BaseModelRef{}, err
	}
	facts, err := NormalizeModelFacts(base.Files, out.ConfigJson, out.TokenizerConfigJson, out.WeightIndexJson, material.Finetuning.RuntimeCapabilities)
	if err != nil {
		return cpup01.BaseModelRef{}, err
	}
	base.Facts = facts
	if _, err := base.BaseManifestSHA256(); err != nil {
		return cpup01.BaseModelRef{}, biz.ErrBaseModelInvalid
	}
	return base, nil
}
