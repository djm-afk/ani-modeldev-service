package resolutiontest

import (
	"context"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// fixtureImages is the synthetic Resource answer for the contract fixture: the
// same catalogue identity and registry the conformance snapshot freezes.
type fixtureImages struct{}

func (fixtureImages) ResolveTrainingImage(_ context.Context, _ string, id, scope string, platform cpup01.TrainingPlatform) (biz.TrainingImageResolution, error) {
	digest := "sha256:" + strings.Repeat("e", 64)
	return biz.TrainingImageResolution{ImageID: id, Scope: scope, ImageVersion: 1, Reference: "registry.example.test/cpu/mlp@" + digest, Digest: digest, RegistryAuthority: "registry.example.test", Platform: platform}, nil
}

func (fixtureImages) EnsurePullSecret(_ context.Context, tenant, cluster, namespace, _, registry string) (string, error) {
	return cpup01.PullSecretName(tenant, cluster, namespace, registry)
}
