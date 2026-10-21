package service

import (
	"context"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
	"time"
)

type implementationQueryFacts struct {
	timing                 cpup01.TimingConfiguration
	reads                  int
	missingFinetuningFacts bool
}

func (f *implementationQueryFacts) ReadImplementationMaterial(_ context.Context, id, device string) (cpup01.ImplementationMaterial, error) {
	if device != "cpu" {
		return cpup01.ImplementationMaterial{}, biz.ErrImplementationUnavailable
	}
	if id == "builtin.mlp" {
		return conformance.MaterialV21(), nil
	}
	if id == "builtin.sft_lora" {
		return conformance.FinetuningMaterialV21(), nil
	}
	return cpup01.ImplementationMaterial{}, biz.ErrImplementationUnavailable
}
func (f *implementationQueryFacts) ReadAdmissionFacts(_ context.Context, tenant, id string) (biz.TenantAdmissionFacts, error) {
	f.reads++
	if id == "builtin.sft_lora" && f.missingFinetuningFacts {
		return biz.TenantAdmissionFacts{}, biz.ErrAdmissionEnvironmentNotReady
	}
	s := conformance.SnapshotV1()
	return biz.TenantAdmissionFacts{TenantID: tenant, Environment: s.Environment, Workspace: s.Workspace, Timing: f.timing}, nil
}
func TestImplementationQueryKeepsInstalledCPUVisibleWithoutLoRAFacts(t *testing.T) {
	q, f := newImplementationQuery()
	f.missingFinetuningFacts = true
	response, err := q.ListImplementations(implementationQueryContext(), &modeldevv1.ListImplementationsRequest{})
	if err != nil || len(response.Implementations) != 2 || !response.Implementations[0].Available || response.Implementations[1].Available || response.Implementations[1].StepTimeoutSeconds != 900 {
		t.Fatal("uninstalled implementation hid real CPU facts", err)
	}
}
func implementationQueryContext() context.Context {
	s := conformance.SnapshotV1()
	return WithVerifiedGovernanceQuery(context.Background(), GovernanceDelivery{TenantID: s.IdentityRef.TenantID, Actor: s.IdentityRef.Actor, RequestID: "query-test"}, modeldevv1.ModelDevQueryService_ListImplementations_FullMethodName)
}
func newImplementationQuery() (*Query, *implementationQueryFacts) {
	f := &implementationQueryFacts{timing: cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900}}
	return NewQuery(nil, nil).WithMaterials(biz.NewManagedMaterials(f, f, nil, nil)).WithFinetuningFacts(directoryModelFacts{}), f
}

type directoryModelFacts struct{}

func (directoryModelFacts) ResolveFinetuningModelFacts(context.Context, string, cpup01.Intent, cpup01.ImplementationMaterial) (cpup01.BaseModelRef, error) {
	return cpup01.BaseModelRef{}, biz.ErrModelDependencyUnavailable
}

func TestImplementationDirectoryRequiresActualModelDependencyForLoRA(t *testing.T) {
	q, _ := newImplementationQuery()
	q.WithFinetuningFacts(nil)
	response, err := q.ListImplementations(implementationQueryContext(), &modeldevv1.ListImplementationsRequest{})
	if err != nil || len(response.Implementations) != 2 || !response.Implementations[0].Available || response.Implementations[1].Available || len(response.Implementations[1].SupportedDeviceModes) != 0 {
		t.Fatal("installed program alone fabricated a Model dependency", err)
	}
}
func TestImplementationQueryProjectsSameDefaultsAndFiniteRules(t *testing.T) {
	q, _ := newImplementationQuery()
	response, e := q.ListImplementations(implementationQueryContext(), &modeldevv1.ListImplementationsRequest{})
	if e != nil {
		t.Fatal(e)
	}
	if response.Total != 2 || len(response.Implementations) != 2 {
		t.Fatal("wrong implementation page")
	}
	for _, view := range response.Implementations {
		if !view.Available || len(view.SupportedDeviceModes) != 1 || view.SupportedDeviceModes[0] != "cpu" || view.ExecutionTimeoutDefaultSeconds != 3600 || view.ExecutionTimeoutMaxSeconds != 7200 || view.StepTimeoutSeconds != 900 {
			t.Fatal("availability/timing provenance lost")
		}
		want := 4
		if view.ImplementationId == "builtin.sft_lora" {
			want = 9
			for _, rule := range view.ParameterRules {
				if rule.Name == "cutoff_len" && !strings.Contains(rule.ValidationDescription, "cpu implementation upper bound=32768") {
					t.Fatal("installed program bound hidden from rules query")
				}
			}
		}
		if len(view.ParameterRules) != want {
			t.Fatal("incomplete rules")
		}
		for _, rule := range view.ParameterRules {
			if !rule.Editable || len(rule.AllowedValues) != 0 || rule.DefaultValue == nil || rule.NumericRepresentation == "" || rule.ValidationDescription == "" {
				t.Fatal("rule became a parameter approval")
			}
		}
	}
}
func TestImplementationQueryAuthorizationUnknownFieldsAndPagination(t *testing.T) {
	q, f := newImplementationQuery()
	if _, e := q.ListImplementations(context.Background(), &modeldevv1.ListImplementationsRequest{}); status.Code(e) != codes.Unauthenticated || f.reads != 0 {
		t.Fatal("query read facts without authority")
	}
	request := &modeldevv1.ListImplementationsRequest{}
	request.ProtoReflect().SetUnknown([]byte{0x12, 0})
	if _, e := q.ListImplementations(implementationQueryContext(), request); status.Code(e) != codes.InvalidArgument {
		t.Fatal("old selection accepted")
	}
	page, size := uint32(2), uint32(1)
	response, e := q.ListImplementations(implementationQueryContext(), &modeldevv1.ListImplementationsRequest{Page: &modeldevv1.PageRequest{Page: &page, PageSize: &size}})
	if e != nil || response.Total != 2 || len(response.Implementations) != 1 || response.Implementations[0].ImplementationId != "builtin.sft_lora" {
		t.Fatal("pagination rebuilt total", e)
	}
	zero := uint32(0)
	if _, e := q.ListImplementations(implementationQueryContext(), &modeldevv1.ListImplementationsRequest{Page: &modeldevv1.PageRequest{Page: &zero}}); status.Code(e) != codes.InvalidArgument {
		t.Fatal("zero page accepted")
	}
}
func TestHistoricalExecutionProjectionPreservesOriginalParametersAndTiming(t *testing.T) {
	s := conformance.FinetuningSnapshotV1()
	i := conformance.FinetuningIntentV1()
	hash, _ := s.Digest()
	record := biz.QueryRecord{Execution: biz.Execution{Admission: biz.Admission{TenantID: s.IdentityRef.TenantID, Actor: s.IdentityRef.Actor, OperationID: s.IdentityRef.OperationID, ExecutionID: s.IdentityRef.ExecutionID, Intent: i, IntentHash: s.IdentityRef.IntentHash, Snapshot: s, SpecHash: hash, AcceptedAt: s.Timing.AcceptedAt}, States: biz.ExecutionStates{Compute: biz.ComputeStateAccepted, Delivery: biz.DeliveryStatePending, Resource: biz.ResourceStateNotApplicable, Close: biz.CloseStateOpen}}}
	view, e := executionView(record)
	if e != nil {
		t.Fatal(e)
	}
	if view.ExplicitFinetuning == nil || view.ExplicitFinetuning.MaxSteps != nil || len(view.ResolvedParameters) != 9 || view.ImplementationId != "builtin.sft_lora" || view.ComputePresetId != i.ComputePresetID || view.Timing.StepTimeoutSeconds != 900 || !view.Timing.AcceptedAt.AsTime().Equal(s.Timing.AcceptedAt) || !view.DeadlineAt.AsTime().Equal(s.DeadlineAt) {
		t.Fatal("historical frozen projection lost")
	}
	record.Runtime.TrainingRejection = &biz.TrainingCreationRejection{ObservedAt: s.Timing.AcceptedAt.Add(time.Minute)}
	view, e = executionView(record)
	if e != nil || !view.ObservedAt.AsTime().Equal(record.Runtime.TrainingRejection.ObservedAt) {
		t.Fatal("original observation changed")
	}
}
