package biz

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"strings"
	"time"
)

// InputQueryFilter narrows an input-version page. Both fields are applied where
// the page is counted, so Total always describes the same set the window shows.
type InputQueryFilter struct {
	State InputState
	// Format is the verified format (CSV or JSON); empty means no restriction.
	// Compatibility is decided by the format alone: CSV feeds general training,
	// JSON feeds finetuning.
	Format string
}

type ManagedInputQuery interface {
	AdmissionInputReader
	List(context.Context, string, InputQueryFilter, int, int) ([]InputVersion, error)
	Count(context.Context, string, InputQueryFilter) (uint64, error)
}

// ManagedMaterials projects the installed implementation and tenant facts.
// Software materials have no import, approval, enablement or combination CRUD.
type ManagedMaterials struct {
	materials AdmissionMaterialReader
	facts     AdmissionFactsReader
	inputs    ManagedInputQuery
	importer  *InputImporter
}

func NewManagedMaterials(materials AdmissionMaterialReader, facts AdmissionFactsReader, inputs ManagedInputQuery, importer *InputImporter) *ManagedMaterials {
	return &ManagedMaterials{materials: materials, facts: facts, inputs: inputs, importer: importer}
}

func (m *ManagedMaterials) Implementation(ctx context.Context, tenant, implementationID, deviceMode string) (cpup01.ImplementationMaterial, TenantAdmissionFacts, error) {
	if !validAdmissionID(tenant) || (implementationID != "builtin.mlp" && implementationID != "builtin.sft_lora") || (deviceMode != "cpu" && deviceMode != "gpu") {
		return cpup01.ImplementationMaterial{}, TenantAdmissionFacts{}, ErrInvalidAdmission
	}
	if m == nil || m.materials == nil || m.facts == nil {
		return cpup01.ImplementationMaterial{}, TenantAdmissionFacts{}, ErrAdmissionEnvironmentNotReady
	}
	material, err := m.materials.ReadImplementationMaterial(ctx, implementationID, deviceMode)
	if err != nil {
		return cpup01.ImplementationMaterial{}, TenantAdmissionFacts{}, err
	}
	facts, err := m.facts.ReadAdmissionFacts(ctx, tenant, implementationID)
	if err != nil {
		return cpup01.ImplementationMaterial{}, TenantAdmissionFacts{}, err
	}
	if material.Implementation.ImplementationID != implementationID || !strings.EqualFold(facts.TenantID, tenant) || material.Workspace != facts.Workspace {
		return cpup01.ImplementationMaterial{}, TenantAdmissionFacts{}, ErrAdmissionEnvironmentNotReady
	}
	if _, err := material.Canonical(); err != nil {
		return cpup01.ImplementationMaterial{}, TenantAdmissionFacts{}, ErrAdmissionEnvironmentNotReady
	}
	return material, facts, nil
}

func (m *ManagedMaterials) TimingFor(ctx context.Context, tenant, implementationID string) (cpup01.TimingConfiguration, error) {
	if m == nil || m.facts == nil || !validAdmissionID(tenant) {
		return cpup01.TimingConfiguration{}, ErrAdmissionEnvironmentNotReady
	}
	facts, err := m.facts.ReadAdmissionFacts(ctx, tenant, implementationID)
	if err != nil {
		return cpup01.TimingConfiguration{}, err
	}
	if !strings.EqualFold(facts.TenantID, tenant) {
		return cpup01.TimingConfiguration{}, ErrAdmissionEnvironmentNotReady
	}
	if _, err := facts.Timing.Resolve(time.Now().UTC().Truncate(time.Microsecond), nil); err != nil {
		return cpup01.TimingConfiguration{}, ErrAdmissionEnvironmentNotReady
	}
	return facts.Timing, nil
}

func (m *ManagedMaterials) ImportCSV(ctx context.Context, request InputImport, implementationID string) (InputVersion, error) {
	if implementationID != "builtin.mlp" || request.InputFormat() != "CSV" {
		return InputVersion{}, ErrInvalidInput
	}
	return m.importInput(ctx, request, implementationID)
}
func (m *ManagedMaterials) ImportFinetuningDataset(ctx context.Context, request InputImport, implementationID string) (InputVersion, error) {
	if implementationID != "builtin.sft_lora" || request.InputFormat() != "JSON" {
		return InputVersion{}, ErrInvalidInput
	}
	return m.importInput(ctx, request, implementationID)
}
func (m *ManagedMaterials) importInput(ctx context.Context, request InputImport, implementationID string) (InputVersion, error) {
	if m == nil || m.facts == nil || m.importer == nil {
		return InputVersion{}, ErrAdmissionEnvironmentNotReady
	}
	facts, err := m.facts.ReadAdmissionFacts(ctx, request.TenantID, implementationID)
	if err != nil {
		return InputVersion{}, err
	}
	if !strings.EqualFold(facts.TenantID, request.TenantID) {
		return InputVersion{}, ErrAdmissionEnvironmentNotReady
	}
	request.Scope = facts.InputScope
	if request.InputFormat() == "JSON" {
		return m.importer.ImportFinetuningDataset(ctx, request)
	}
	return m.importer.ImportCSV(ctx, request)
}

func (m *ManagedMaterials) Input(ctx context.Context, tenant, id string) (InputVersion, error) {
	if m == nil || m.inputs == nil {
		return InputVersion{}, ErrPersistence
	}
	return m.inputs.Get(ctx, tenant, id)
}
func (m *ManagedMaterials) Inputs(ctx context.Context, tenant string, filter InputQueryFilter, offset, limit int) ([]InputVersion, error) {
	if m == nil || m.inputs == nil {
		return nil, ErrPersistence
	}
	return m.inputs.List(ctx, tenant, filter, offset, limit)
}
func (m *ManagedMaterials) InputsCount(ctx context.Context, tenant string, filter InputQueryFilter) (uint64, error) {
	if m == nil || m.inputs == nil {
		return 0, ErrPersistence
	}
	return m.inputs.Count(ctx, tenant, filter)
}
