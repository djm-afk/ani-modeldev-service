package service_test

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestReadinessRequiresAuthorizationForItsExactQueryMethod(t *testing.T) {
	query := service.NewQuery(nil, nil)
	for _, ctx := range []context.Context{
		context.Background(),
		service.WithVerifiedGovernanceQuery(context.Background(), service.GovernanceDelivery{}, modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName),
	} {
		if _, err := query.CheckFinetuningReadiness(ctx, &modeldevv1.CheckFinetuningReadinessRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("readiness accepted another method or lacked its own auth check: %v", err)
		}
	}
}

func newReadiness(t *testing.T, material cpup01.ImplementationMaterial, source *readinessSource) (*service.Query, *modeldevv1.CheckFinetuningReadinessRequest) {
	t.Helper()
	wire, err := contractpb.EncodeSnapshot(conformance.FinetuningSnapshotV1())
	if err != nil {
		t.Fatal(err)
	}
	catalogue := readinessCatalogue{material}
	query := service.NewQuery(nil, nil).WithMaterials(biz.NewManagedMaterials(catalogue, catalogue, nil, nil)).WithFinetuningReadiness(source, cpup01.ContentLimits{MaxContentBytes: 17179869184})
	return query, &modeldevv1.CheckFinetuningReadinessRequest{BaseModelVersionId: source.base.VersionID, ComputePresetId: wire.ComputeOrigin.ComputePresetId, ComputeOrigin: wire.ComputeOrigin}
}

func TestReadinessDistinguishesCompletedBusinessChecksFromSourceFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		code    codes.Code
		reason  modeldevv1.ErrorReason
	}{
		{"invalid model", biz.ErrBaseModelInvalid, codes.OK, modeldevv1.ErrorReason_ERROR_REASON_BASE_MODEL_INVALID},
		{"unsupported runtime", biz.ErrBaseModelUnsupported, codes.OK, modeldevv1.ErrorReason_ERROR_REASON_BASE_MODEL_UNSUPPORTED},
		{"unknown context", biz.ErrContextLimitUnknown, codes.OK, modeldevv1.ErrorReason_ERROR_REASON_CONTEXT_LIMIT_UNKNOWN},
		{"source content policy", &cpup01.LimitViolation{Reason: "CONTENT_LIMIT_EXCEEDED", Kind: "base_model", LimitName: "max_content_bytes", Actual: 9, Limit: 8}, codes.OK, modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED},
		{"absent visible source", biz.ErrModelSourceNotFound, codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND},
		{"forbidden source", biz.ErrModelSourceForbidden, codes.PermissionDenied, modeldevv1.ErrorReason_ERROR_REASON_FORBIDDEN},
		{"runtime dependency", biz.ErrModelDependencyUnavailable, codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_RUNTIME_UNAVAILABLE},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &readinessSource{base: conformance.FinetuningSnapshotV1().Finetuning.BaseModel, err: errors.Join(errors.New("external private diagnostic"), tc.failure)}
			query, request := newReadiness(t, conformance.FinetuningMaterialV21(), source)
			response, err := query.CheckFinetuningReadiness(readinessContext(), request)
			if status.Code(err) != tc.code {
				t.Fatalf("code=%v want %v err=%v", status.Code(err), tc.code, err)
			}
			if tc.code == codes.OK {
				if response.Assessment != modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_UNSUPPORTED || response.Reason != tc.reason || response.RuntimeCapabilitiesSha256 == "" || response.StructureLimits.GetMaxRecordBytes() != cpup01.MaxRecordBytes {
					t.Fatalf("incomplete business check: %+v", response)
				}
				return
			}
			if response != nil {
				t.Fatal("error returned a normal assessment")
			}
			if tc.reason != 0 {
				var found bool
				for _, d := range status.Convert(err).Details() {
					if detail, ok := d.(*modeldevv1.ErrorDetail); ok {
						found = detail.Reason == tc.reason && detail.CorrelationId != "" && detail.SafeMessage != "external private diagnostic"
					}
				}
				if !found {
					t.Fatalf("missing safe finite error detail: %v", err)
				}
			}
		})
	}
}

func TestReadinessRequiresExplicitTemplateWhenSourceAndMaterialHaveNoDeterministicChoice(t *testing.T) {
	material := conformance.FinetuningMaterialV21()
	material.Finetuning.ModelTemplateAssociations = nil
	source := &readinessSource{base: conformance.FinetuningSnapshotV1().Finetuning.BaseModel}
	source.base.Facts.TemplateSources = nil
	query, request := newReadiness(t, material, source)
	response, err := query.CheckFinetuningReadiness(readinessContext(), request)
	if err != nil || response.Assessment != modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_TEMPLATE_REQUIRED || response.Reason != modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_REQUIRED || len(response.TemplateCandidates) == 0 {
		t.Fatalf("template choice not represented: %+v/%v", response, err)
	}
	template := "unknown-valid-id"
	request.FinetuningTemplate = &template
	response, err = query.CheckFinetuningReadiness(readinessContext(), request)
	if err != nil || response.Assessment != modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_UNSUPPORTED || response.Reason != modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_UNSUPPORTED {
		t.Fatalf("unknown named template: %+v/%v", response, err)
	}
	template = "qwen"
	response, err = query.CheckFinetuningReadiness(readinessContext(), request)
	if err != nil || response.Assessment != modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_ELIGIBLE || response.TemplatePlan.GetTemplateId() != template {
		t.Fatalf("explicit allowed selection failed: %+v/%v", response, err)
	}
}

func TestReadinessRejectsUntrustedSelectionBeforeAnySourceRead(t *testing.T) {
	for _, change := range []func(*modeldevv1.CheckFinetuningReadinessRequest){
		func(r *modeldevv1.CheckFinetuningReadinessRequest) {
			r.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		},
		func(r *modeldevv1.CheckFinetuningReadinessRequest) { r.ComputeOrigin = nil },
		func(r *modeldevv1.CheckFinetuningReadinessRequest) {
			r.ComputePresetId = "11111111-1111-4111-8111-111111111111"
		},
		func(r *modeldevv1.CheckFinetuningReadinessRequest) { v := ""; r.FinetuningTemplate = &v },
		func(r *modeldevv1.CheckFinetuningReadinessRequest) {
			r.ComputeOrigin.SelectionDigest = "browser-chosen"
		},
	} {
		source := &readinessSource{base: conformance.FinetuningSnapshotV1().Finetuning.BaseModel}
		query, original := newReadiness(t, conformance.FinetuningMaterialV21(), source)
		request := proto.Clone(original).(*modeldevv1.CheckFinetuningReadinessRequest)
		change(request)
		if response, err := query.CheckFinetuningReadiness(readinessContext(), request); response != nil || status.Code(err) != codes.InvalidArgument || source.calls != 0 {
			t.Fatalf("untrusted selection reached source: %+v/%v calls=%d", response, err, source.calls)
		}
	}
}

type readinessCatalogue struct{ material cpup01.ImplementationMaterial }

func (r readinessCatalogue) ReadImplementationMaterial(_ context.Context, id, mode string) (cpup01.ImplementationMaterial, error) {
	if id != "builtin.sft_lora" || mode != "cpu" {
		return cpup01.ImplementationMaterial{}, biz.ErrImplementationUnavailable
	}
	return r.material, nil
}
func (r readinessCatalogue) ReadAdmissionFacts(_ context.Context, tenant, id string) (biz.TenantAdmissionFacts, error) {
	return biz.TenantAdmissionFacts{TenantID: tenant, Workspace: r.material.Workspace}, nil
}

type readinessSource struct {
	base            cpup01.BaseModelRef
	calls           int
	tenant, version string
	err             error
}

func (r *readinessSource) ResolveFinetuningSourceFacts(_ context.Context, tenant, version string, _ cpup01.ImplementationMaterial) (cpup01.BaseModelRef, error) {
	r.calls++
	r.tenant, r.version = tenant, version
	return r.base, r.err
}
func readinessContext() context.Context {
	return service.WithVerifiedGovernanceQuery(context.Background(), service.GovernanceDelivery{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42", RequestID: "11111111-1111-4111-8111-111111111111"}, modeldevv1.ModelDevQueryService_CheckFinetuningReadiness_FullMethodName)
}
func TestReadinessUsesSourceOnlyFactsAndSharedTemplateSelectionWithoutDatasetOrTarget(t *testing.T) {
	snapshot := conformance.FinetuningSnapshotV1()
	wire, err := contractpb.EncodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	catalogue := readinessCatalogue{conformance.FinetuningMaterialV21()}
	source := &readinessSource{base: snapshot.Finetuning.BaseModel}
	limits := cpup01.ContentLimits{MaxContentBytes: 17179869184}
	query := service.NewQuery(nil, nil).WithMaterials(biz.NewManagedMaterials(catalogue, catalogue, nil, nil)).WithFinetuningReadiness(source, limits)
	template := "qwen"
	request := &modeldevv1.CheckFinetuningReadinessRequest{BaseModelVersionId: source.base.VersionID, ComputePresetId: wire.ComputeOrigin.ComputePresetId, ComputeOrigin: wire.ComputeOrigin, FinetuningTemplate: &template}
	response, err := query.CheckFinetuningReadiness(readinessContext(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Assessment != modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_ELIGIBLE || !response.RuntimeValidationRequired || response.ModelId != source.base.ModelID || response.ModelVersionId != source.base.VersionID || response.TemplatePlan.GetTemplateId() != template || response.Limits.GetMaxContentBytes() != limits.MaxContentBytes || response.ModelType != source.base.Facts.ModelType || response.ContextLimit != source.base.Facts.ContextLimit {
		t.Fatalf("readiness lost actual facts/template/limit: %+v", response)
	}
	if source.calls != 1 || source.tenant != "11111111-2222-4333-8444-555555555555" || source.version != request.BaseModelVersionId {
		t.Fatal("source lookup lost verified tenant/version scope")
	}
}
