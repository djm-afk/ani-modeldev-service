package biz

import (
	"context"
	"slices"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// ManagedReleaseSelection is an owner-approved immutable catalogue reference.
// It is not Governance's current Release pointer.
type ManagedReleaseSelection struct{ ReleaseID, ReleaseDigest string }
type ManagedMaterialsFacts interface {
	AdmissionFactsReader
	ListReleaseSelections(context.Context, string) ([]ManagedReleaseSelection, error)
}
type ManagedReleaseStore interface {
	AdmissionReleaseReader
	ImportRelease(context.Context, []byte, string) (cpup01.ReleaseDocument, bool, error)
}
type ManagedInputQuery interface {
	AdmissionInputReader
	List(context.Context, string, InputState, string, int) ([]InputVersion, string, error)
	Count(context.Context, string, InputState) (uint64, error)
}

type ManagedMaterials struct {
	releases ManagedReleaseStore
	facts    ManagedMaterialsFacts
	inputs   ManagedInputQuery
	importer *InputImporter
}

func NewManagedMaterials(releases ManagedReleaseStore, facts ManagedMaterialsFacts, inputs ManagedInputQuery, importer *InputImporter) *ManagedMaterials {
	return &ManagedMaterials{releases: releases, facts: facts, inputs: inputs, importer: importer}
}
func (m *ManagedMaterials) Release(ctx context.Context, tenant, preset, id, digest string) (cpup01.ReleaseDocument, error) {
	if !validAdmissionID(tenant) || !validAdmissionID(preset) || !validAdmissionID(id) || !closeSpecHashPattern.MatchString(digest) {
		return cpup01.ReleaseDocument{}, ErrInvalidAdmission
	}
	if m == nil || m.releases == nil || m.facts == nil {
		return cpup01.ReleaseDocument{}, ErrAdmissionEnvironmentNotReady
	}
	release, err := m.releases.ReadRelease(ctx, id, digest)
	if err != nil {
		return cpup01.ReleaseDocument{}, err
	}
	if release.ReleaseID != id || release.PresetID != preset {
		return cpup01.ReleaseDocument{}, ErrNoCompatibleRelease
	}
	facts, err := m.facts.ReadAdmissionFacts(ctx, tenant, id, digest)
	if err != nil {
		return cpup01.ReleaseDocument{}, err
	}
	if facts.TenantID != tenant || !compatibleReleaseFacts(facts, release) {
		return cpup01.ReleaseDocument{}, ErrNoCompatibleRelease
	}
	return release, nil
}
func compatibleReleaseFacts(facts TenantAdmissionFacts, release cpup01.ReleaseDocument) bool {
	want, actual := release.Runtime, facts.Runtime
	a, b := slices.Clone(want.TargetJobs), slices.Clone(actual.TargetJobs)
	slices.Sort(a)
	slices.Sort(b)
	return want.Name == actual.Name && want.Kind == actual.Kind && want.APIGroup == actual.APIGroup && want.ContentSHA256 == actual.ContentSHA256 && slices.Equal(a, b) && release.Workspace == facts.Workspace &&
		validAdmissionContainerPath(facts.InputFilePath) && validAdmissionContainerPath(facts.OutputDirectoryPath) && facts.InputFilePath != facts.OutputDirectoryPath && !strings.HasPrefix(facts.InputFilePath, facts.OutputDirectoryPath+"/") && !strings.HasPrefix(facts.OutputDirectoryPath, facts.InputFilePath+"/") &&
		ValidStorageReference(facts.InputScope.StorageConnectionID) && ValidStorageReference(facts.InputScope.Bucket) && ValidStorageKey(facts.InputScope.ApprovedPrefix) && ValidStorageReference(facts.PublicationScope.StorageConnectionID) && ValidStorageReference(facts.PublicationScope.Bucket) && ValidStorageKey(facts.PublicationScope.ApprovedPrefix)
}
func (m *ManagedMaterials) ImportRelease(ctx context.Context, tenant, preset, id, digest string, raw []byte) (cpup01.ReleaseDocument, bool, error) {
	release, actual, err := cpup01.ParseRelease(raw)
	if err != nil || actual != digest || release.ReleaseID != id || release.PresetID != preset || !validAdmissionID(tenant) {
		return cpup01.ReleaseDocument{}, false, ErrInvalidAdmission
	}
	if m == nil || m.facts == nil || m.releases == nil {
		return cpup01.ReleaseDocument{}, false, ErrAdmissionEnvironmentNotReady
	}
	facts, err := m.facts.ReadAdmissionFacts(ctx, tenant, id, digest)
	if err != nil {
		return cpup01.ReleaseDocument{}, false, err
	}
	if facts.TenantID != tenant || !compatibleReleaseFacts(facts, release) {
		return cpup01.ReleaseDocument{}, false, ErrNoCompatibleRelease
	}
	return m.releases.ImportRelease(ctx, raw, digest)
}
func (m *ManagedMaterials) ImportCSV(ctx context.Context, request InputImport, releaseID, digest string) (InputVersion, error) {
	if m == nil || m.facts == nil || m.importer == nil {
		return InputVersion{}, ErrAdmissionEnvironmentNotReady
	}
	facts, err := m.facts.ReadAdmissionFacts(ctx, request.TenantID, releaseID, digest)
	if err != nil {
		return InputVersion{}, err
	}
	request.Scope = facts.InputScope
	return m.importer.ImportCSV(ctx, request)
}
func (m *ManagedMaterials) Releases(ctx context.Context, tenant string) ([]cpup01.ReleaseDocument, error) {
	if !validAdmissionID(tenant) {
		return nil, ErrInvalidAdmission
	}
	if m == nil || m.facts == nil {
		return nil, ErrAdmissionEnvironmentNotReady
	}
	refs, err := m.facts.ListReleaseSelections(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]cpup01.ReleaseDocument, 0, len(refs))
	for _, ref := range refs {
		release, err := m.releases.ReadRelease(ctx, ref.ReleaseID, ref.ReleaseDigest)
		if err != nil {
			continue
		} // An allowed but unimported Release is not selectable.
		if _, err = m.Release(ctx, tenant, release.PresetID, ref.ReleaseID, ref.ReleaseDigest); err != nil {
			return nil, err
		}
		out = append(out, release)
	}
	return out, nil
}
func (m *ManagedMaterials) Input(ctx context.Context, tenant, id string) (InputVersion, error) {
	if m == nil || m.inputs == nil {
		return InputVersion{}, ErrPersistence
	}
	return m.inputs.Get(ctx, tenant, id)
}
func (m *ManagedMaterials) Inputs(ctx context.Context, tenant string, state InputState, after string, limit int) ([]InputVersion, string, error) {
	if m == nil || m.inputs == nil {
		return nil, "", ErrPersistence
	}
	return m.inputs.List(ctx, tenant, state, after, limit)
}
func (m *ManagedMaterials) InputsCount(ctx context.Context, tenant string, state InputState) (uint64, error) {
	if m == nil || m.inputs == nil {
		return 0, ErrPersistence
	}
	return m.inputs.Count(ctx, tenant, state)
}
