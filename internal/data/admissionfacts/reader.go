// Package admissionfacts reads current published tenant execution authority.
// Business admission is read-only: missing environments are never provisioned.
package admissionfacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	projection "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/tenantenvironment"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

var ErrInvalidFacts = errors.New("INVALID_MANAGED_ADMISSION_FACTS")

type EnvironmentReader interface {
	ReadReadyBinding(context.Context, string, string) (projection.Binding, error)
}
type IdentityVerifier interface {
	VerifyBinding(context.Context, string, cpup01.EnvironmentBindingSnapshot) error
}
type Reader struct {
	environments EnvironmentReader
	identities   IdentityVerifier
	clusterID    string
	materials    biz.AdmissionMaterialReader
}

var _ biz.AdmissionFactsReader = (*Reader)(nil)

func New(environments EnvironmentReader, identities IdentityVerifier, materials biz.AdmissionMaterialReader, clusterID string) (*Reader, error) {
	if environments == nil || identities == nil || materials == nil || !cpup01.CanonicalTenantID(clusterID) {
		return nil, ErrInvalidFacts
	}
	return &Reader{environments: environments, identities: identities, clusterID: clusterID, materials: materials}, nil
}
func (reader *Reader) ReadAdmissionFacts(ctx context.Context, tenantID, implementationID string) (biz.TenantAdmissionFacts, error) {
	if err := ctx.Err(); err != nil {
		return biz.TenantAdmissionFacts{}, err
	}
	if !cpup01.CanonicalTenantID(tenantID) || (implementationID != "builtin.mlp" && implementationID != "builtin.sft_lora") {
		return biz.TenantAdmissionFacts{}, biz.ErrInvalidAdmission
	}
	if reader == nil || reader.environments == nil || reader.identities == nil {
		return biz.TenantAdmissionFacts{}, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	binding, err := reader.environments.ReadReadyBinding(ctx, tenantID, reader.clusterID)
	if err != nil {
		if errors.Is(err, biz.ErrTenantEnvironmentNotRegistered) {
			return biz.TenantAdmissionFacts{}, biz.EnvironmentError("ENVIRONMENT_NOT_REGISTERED")
		}
		return biz.TenantAdmissionFacts{}, err
	}
	raw, err := binding.Canonical()
	if err != nil || binding.TenantID != tenantID || binding.Environment.ClusterID != reader.clusterID {
		return biz.TenantAdmissionFacts{}, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	for _, profile := range binding.Profiles {
		implementation, mode := "builtin.mlp", "cpu"
		switch profile.ID {
		case "modeldev-mlp-cpu":
		case "modeldev-mlp-gpu":
			mode = "gpu"
		case "modeldev-sft-lora-cpu":
			implementation = "builtin.sft_lora"
		case "modeldev-sft-lora-gpu":
			implementation, mode = "builtin.sft_lora", "gpu"
		default:
			return biz.TenantAdmissionFacts{}, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
		}
		material, e := reader.materials.ReadImplementationMaterial(ctx, implementation, mode)
		if e != nil {
			return biz.TenantAdmissionFacts{}, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
		}
		canonical, e := material.Canonical()
		if e != nil {
			return biz.TenantAdmissionFacts{}, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
		}
		hash := sha256.Sum256(canonical)
		if profile.PipelineID != material.Implementation.PipelineID || profile.VersionID != material.Implementation.PipelineVersionID || profile.IRSHA256 != material.Implementation.PipelineIRSHA256 || profile.MaterialSHA256 != hex.EncodeToString(hash[:]) || binding.Workspace != material.Workspace {
			return biz.TenantAdmissionFacts{}, biz.EnvironmentError("FIXED_PIPELINE_MATERIAL_MISMATCH")
		}
	}
	environment := binding.Snapshot(projection.Digest(raw))
	if err := reader.identities.VerifyBinding(ctx, tenantID, environment); err != nil {
		return biz.TenantAdmissionFacts{}, err
	}
	// The environment freezes the tenant base root. Prepare appends exactly one
	// execution UUID, before its candidate digest can be presented to Governance.
	if !strings.HasSuffix(binding.DispatchOwner.PipelineRoot, "/artifacts/"+tenantID) || binding.ManagedImageAccess.Validate() != nil {
		return biz.TenantAdmissionFacts{}, biz.EnvironmentError("ENVIRONMENT_DEPENDENCY_UNAVAILABLE")
	}
	inputPath, outputPath := "/inputs/data.csv", "/outputs"
	if implementationID == "builtin.sft_lora" {
		inputPath, outputPath = "/workspace/inputs/data/finetune_data.jsonl", "/workspace/training"
	}
	return biz.TenantAdmissionFacts{TenantID: tenantID, Environment: environment, InputScope: binding.InputScope, ModelScope: binding.ModelScope, PublicationScope: binding.PublicationScope, Workspace: binding.Workspace, InputFilePath: inputPath, OutputDirectoryPath: outputPath, Timing: binding.Timing, TargetPlatform: binding.TargetPlatform, DispatchOwner: binding.DispatchOwner, ManagedImageAccess: binding.ManagedImageAccess}, nil
}
