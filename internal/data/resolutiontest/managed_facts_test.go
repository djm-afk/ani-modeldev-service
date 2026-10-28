package resolutiontest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/admissionfacts"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPrepareUsesPinnedTenantImplementationFactsAndOwnsCopies(t *testing.T) {
	f := prepareResolution(t)
	source := writeManagedFactsFixture(t, f)
	reader, e := admissionfacts.Load(f.ctx, []admissionfacts.FileSource{source})
	if e != nil {
		t.Fatal(e)
	}
	facts, e := reader.ReadAdmissionFacts(f.ctx, f.request.IdentityRef.TenantID, f.request.Intent.ImplementationID)
	if e != nil || !reflect.DeepEqual(facts, f.facts) {
		t.Fatal("fixed facts changed", e)
	}
	if facts.Environment.BindingDigest == source.SHA256 {
		t.Fatal("file pin replaced environment digest")
	}
	facts.Environment.Identities.TrainerServiceAccount = "caller-change"
	again, e := reader.ReadAdmissionFacts(f.ctx, f.request.IdentityRef.TenantID, f.request.Intent.ImplementationID)
	if e != nil || !reflect.DeepEqual(again, f.facts) {
		t.Fatal("consumer mutated loaded facts")
	}
	requireNoResolvedExecution(t, f)
}
func writeManagedFactsFixture(t *testing.T, f resolutionFixture) admissionfacts.FileSource {
	t.Helper()
	type evidence struct {
		Reference string `json:"reference"`
		SHA256    string `json:"sha256"`
	}
	document := struct {
		SchemaVersion       string                            `json:"schema_version"`
		ResourceTenantID    string                            `json:"resource_tenant_id"`
		ImplementationID    string                            `json:"implementation_id"`
		Environment         cpup01.EnvironmentBindingSnapshot `json:"environment"`
		InputScope          cpup01.StorageScope               `json:"input_scope"`
		PublicationScope    cpup01.StorageScope               `json:"publication_scope"`
		Timing              cpup01.TimingConfiguration        `json:"timing"`
		Workspace           cpup01.WorkspaceContract          `json:"workspace"`
		InputFilePath       string                            `json:"input_file_path"`
		OutputDirectoryPath string                            `json:"output_directory_path"`
		EnvironmentEvidence evidence                          `json:"environment_evidence"`
		ApplicationEvidence evidence                          `json:"application_evidence"`
	}{SchemaVersion: "ani.modeldev.managed-admission-facts.v3", ResourceTenantID: f.facts.TenantID, ImplementationID: f.request.Intent.ImplementationID, Environment: f.facts.Environment, InputScope: f.facts.InputScope, PublicationScope: f.facts.PublicationScope, Timing: f.facts.Timing, Workspace: f.facts.Workspace, InputFilePath: f.facts.InputFilePath, OutputDirectoryPath: f.facts.OutputDirectoryPath, EnvironmentEvidence: evidence{Reference: "module-fixture:environment-handoff", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, ApplicationEvidence: evidence{Reference: "module-fixture:application-verification", SHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}
	raw, e := json.Marshal(document)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "managed-admission-facts.json")
	if e := os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(raw)
	return admissionfacts.FileSource{Path: file, SHA256: hex.EncodeToString(sum[:])}
}
