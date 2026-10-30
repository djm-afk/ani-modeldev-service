package biz

import (
	"context"
	"errors"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

type recordingResolver struct {
	answer   TrainingImageResolution
	err      error
	requests []string
}

func (r *recordingResolver) ResolveTrainingImage(_ context.Context, _, imageID, scope string, platform cpup01.TrainingPlatform) (TrainingImageResolution, error) {
	r.requests = append(r.requests, imageID+"|"+scope)
	if r.err != nil {
		return TrainingImageResolution{}, r.err
	}
	answer := r.answer
	answer.ImageID, answer.Scope, answer.Platform = imageID, scope, platform
	return answer, nil
}

func resolutionInputs() (cpup01.ImplementationMaterial, TenantAdmissionFacts, cpup01.InputRef, []cpup01.Parameter) {
	snapshot := conformance.SnapshotV1()
	facts := TenantAdmissionFacts{
		TenantID:            snapshot.IdentityRef.TenantID,
		Environment:         snapshot.Environment,
		InputScope:          cpup01.StorageScope{StorageConnectionID: snapshot.Input.Object.StorageConnectionID, Bucket: snapshot.Input.Object.Bucket, ApprovedPrefix: "fixed", CredentialReference: "input-read"},
		Workspace:           snapshot.Workspace,
		InputFilePath:       "/inputs/data.csv",
		OutputDirectoryPath: "/outputs",
		TargetPlatform:      cpup01.TrainingPlatform{OS: "linux", Architecture: "amd64"},
	}
	return conformance.MaterialV21(), facts, snapshot.Input, snapshot.Program.ResolvedParameters
}

func validResolution() TrainingImageResolution {
	return TrainingImageResolution{
		ImageVersion:      1,
		Reference:         "registry.example.test/cpu/mlp@sha256:" + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		Digest:            "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		RegistryAuthority: "registry.example.test",
	}
}

// The user selection wins; the material default is used only when the intent
// names nothing. Both go through the same resolver and freeze the same facts.
func TestResolveTrainingProgramUsesSelectionOrDefault(t *testing.T) {
	material, facts, input, parameters := resolutionInputs()
	resolver := &recordingResolver{answer: validResolution()}
	admission := &AdmissionResolver{images: resolver}
	ctx := context.Background()

	_, factsFrozen, err := admission.resolveTrainingProgram(ctx, facts.TenantID, cpup01.Intent{}, material, parameters, facts, input)
	if err != nil {
		t.Fatalf("default resolution rejected: %v", err)
	}
	defaultImage := material.Training.DefaultImage
	if len(resolver.requests) != 1 || resolver.requests[0] != defaultImage.ImageID+"|"+string(defaultImage.Scope) {
		t.Fatalf("default selection not resolved: %v", resolver.requests)
	}
	secret, err := cpup01.PullSecretName(facts.TenantID, facts.Environment.ClusterID, facts.Environment.NamespaceName, "registry.example.test")
	if err != nil || factsFrozen.PullSecretRef != secret || factsFrozen.ProtocolVersion != cpup01.TrainingImageProtocolVersion {
		t.Fatalf("frozen facts %+v do not match the derived Secret %q: %v", factsFrozen, secret, err)
	}

	id, scope := "img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "tenant"
	intent := cpup01.Intent{TrainingImageID: &id, TrainingImageScope: &scope}
	if _, _, err := admission.resolveTrainingProgram(ctx, facts.TenantID, intent, material, parameters, facts, input); err != nil {
		t.Fatalf("explicit selection rejected: %v", err)
	}
	if resolver.requests[len(resolver.requests)-1] != id+"|"+scope {
		t.Fatalf("explicit selection not forwarded: %v", resolver.requests)
	}
}

// A missing platform, a missing resolver or a mismatched answer fails closed
// before anything is frozen.
func TestResolveTrainingProgramFailsClosed(t *testing.T) {
	material, facts, input, parameters := resolutionInputs()
	ctx := context.Background()

	noPlatform := facts
	noPlatform.TargetPlatform = cpup01.TrainingPlatform{}
	if _, _, err := (&AdmissionResolver{images: &recordingResolver{answer: validResolution()}}).resolveTrainingProgram(ctx, facts.TenantID, cpup01.Intent{}, material, parameters, noPlatform, input); !errors.Is(err, ErrAdmissionEnvironmentNotReady) {
		t.Fatalf("missing target platform accepted: %v", err)
	}
	if _, _, err := (&AdmissionResolver{}).resolveTrainingProgram(ctx, facts.TenantID, cpup01.Intent{}, material, parameters, facts, input); !errors.Is(err, ErrAdmissionEnvironmentNotReady) {
		t.Fatalf("missing resolver accepted: %v", err)
	}
	badAnswer := validResolution()
	badAnswer.Reference = "mirror.example.test/cpu/mlp@sha256:" + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, _, err := (&AdmissionResolver{images: &recordingResolver{answer: badAnswer}}).resolveTrainingProgram(ctx, facts.TenantID, cpup01.Intent{}, material, parameters, facts, input); !errors.Is(err, ErrImplementationUnavailable) {
		t.Fatalf("mismatched answer accepted: %v", err)
	}
	zeroVersion := validResolution()
	zeroVersion.ImageVersion = 0
	if _, _, err := (&AdmissionResolver{images: &recordingResolver{answer: zeroVersion}}).resolveTrainingProgram(ctx, facts.TenantID, cpup01.Intent{}, material, parameters, facts, input); !errors.Is(err, ErrImplementationUnavailable) {
		t.Fatalf("missing image version accepted: %v", err)
	}
}
