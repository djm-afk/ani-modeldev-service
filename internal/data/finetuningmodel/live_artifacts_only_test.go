package finetuningmodel

import (
	"context"
	"errors"
	"testing"

	modelv1 "github.com/zhangzhe-ctrl/ani-model-service/api/model/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

type artifactsOnlyModelBoundary struct {
	*liveModelBoundary
	targetObserved bool
}

func (b *artifactsOnlyModelBoundary) ResolveFinetuningFacts(ctx context.Context, tenant string, in *modelv1.ResolveFinetuningFactsRequest) (*modelv1.ResolveFinetuningFactsResponse, error) {
	b.targetObserved = in.Target != nil
	return b.liveModelBoundary.ResolveFinetuningFacts(ctx, tenant, in)
}

func TestLiveModelReaderArtifactsOnlyKeepsVerifiedBaseWithoutTarget(t *testing.T) {
	for _, wrongTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "base only", true: "provider injects target"}[wrongTarget], func(t *testing.T) {
			remote, scope, intent, material := liveModelFixture(t)
			intent.TargetModelID, intent.NewModelName = nil, nil
			if !wrongTarget {
				remote.response.Target = nil
			}
			boundary := &artifactsOnlyModelBoundary{liveModelBoundary: remote}
			reader, err := NewLive(boundary, scope)
			if err != nil {
				t.Fatal(err)
			}
			base, err := reader.ResolveFinetuningModelFacts(context.Background(), remote.response.TenantId, intent, material)
			if wrongTarget {
				if !errors.Is(err, biz.ErrModelDependencyUnavailable) {
					t.Fatal("provider introduced a Model write target", err)
				}
			} else if err != nil || base.VersionID != *intent.BaseModelVersionID || base.Object.SHA256 != remote.response.Sha256 || base.Object.VersionID == nil || *base.Object.VersionID != remote.response.ObjectVersionId || len(base.Files) != 4 || base.Scope != scope {
				t.Fatalf("ARTIFACTS_ONLY_VERIFIED_BASE_READER_MISSING: %+v %v", base, err)
			}
			if remote.calls != 1 || boundary.targetObserved {
				t.Fatal("base-only request fabricated a write target or skipped Model verification")
			}
		})
	}
}
