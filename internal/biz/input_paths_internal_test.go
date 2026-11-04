package biz

import (
	"errors"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

// fixtureInputVersion builds a READY input with the proof its own format
// requires. Only the caller's format and proof presence are varied.
func fixtureInputVersion(format, schema string, rows int64, features uint32, prepared cpup01.PreparedDatasetFacts) InputVersion {
	version := "version-1"
	requested := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	importRequest := InputImport{
		Format: format, TenantID: "77777777-7777-4777-8777-777777777777",
		RequestID: "88888888-8888-4888-8888-888888888888", InputVersionID: "99999999-9999-4999-8999-999999999999",
		Actor: "governance:user:contract-test", RequestedAt: requested,
		Scope:  cpup01.StorageScope{StorageConnectionID: "input-store-v1", Bucket: "cpu-inputs", ApprovedPrefix: "inputs/fixed", CredentialReference: "input-read"},
		Object: cpup01.FixedObjectRef{StorageConnectionID: "input-store-v1", Bucket: "cpu-inputs", Key: "inputs/fixed/data", VersionID: &version, SizeBytes: 4096, SHA256: sha64("d")},
	}
	verified := VerifiedObject{Object: importRequest.Object, VerifiedAt: requested.Add(time.Second)}
	versionValue := InputVersion{Import: importRequest, State: InputStateReady}
	if format == "CSV" {
		versionValue.Verification = &VerifiedCSV{VerifiedObject: verified, SchemaVersion: schema, RowCount: rows, FeatureCount: features, PreparedDataset: prepared}
	} else {
		versionValue.AlpacaVerification = &VerifiedAlpaca{VerifiedObject: verified, SchemaVersion: schema, RowCount: rows, PreparedDataset: prepared}
	}
	return versionValue
}

func sha64(fill string) string {
	result := ""
	for len(result) < 64 {
		result += fill
	}
	return result[:64]
}

func csvProof() cpup01.PreparedDatasetFacts {
	return cpup01.PreparedDatasetFacts{Format: "CPU_CSV", TransformID: "csv-identity-v3", SizeBytes: 4096, SHA256: sha64("d"), RowCount: 1027}
}

func jsonProof() cpup01.PreparedDatasetFacts {
	return cpup01.PreparedDatasetFacts{Format: "ALPACA_JSONL", TransformID: "alpaca-array-compact-jsonl-v3", SizeBytes: 3900, SHA256: sha64("1"), RowCount: 64}
}

// Both kinds accept exactly their own format, and the proof supplies the
// values rather than the branch defaulting them.
func TestVerifiedAdmissionInputAcceptsEachLegalCombination(t *testing.T) {
	csv, err := verifiedAdmissionInput(fixtureInputVersion("CSV", "ani.cpu.csv.v1", 1027, 16, csvProof()), "GENERAL_TRAINING")
	if err != nil || csv.SchemaVersion != "ani.cpu.csv.v1" || csv.FeatureCount != 16 || csv.RowCount != 1027 {
		t.Fatalf("CSV general training rejected: %+v %v", csv, err)
	}
	json, err := verifiedAdmissionInput(fixtureInputVersion("JSON", AlpacaSchemaVersion, 64, 0, jsonProof()), "GENERAL_TRAINING")
	if err != nil || json.SchemaVersion != AlpacaSchemaVersion || json.FeatureCount != 0 || json.RowCount != 64 {
		t.Fatalf("JSON general training rejected: %+v %v", json, err)
	}
	fine, err := verifiedAdmissionInput(fixtureInputVersion("JSON", AlpacaSchemaVersion, 64, 0, jsonProof()), "FINETUNING")
	if err != nil || fine.SchemaVersion != AlpacaSchemaVersion || fine.RowCount != 64 {
		t.Fatalf("JSON finetuning rejected: %+v %v", fine, err)
	}
}

// A36: a READY input whose format the kind does not accept is a mismatch, not
// "not ready". Operators must not be sent after a dataset-state problem.
func TestVerifiedAdmissionInputRejectsIllegalCombinationAsMismatch(t *testing.T) {
	// FINETUNING+CSV: the input is genuinely READY with a valid CSV proof.
	csvReady := fixtureInputVersion("CSV", "ani.cpu.csv.v1", 1027, 16, csvProof())
	if _, err := verifiedAdmissionInput(csvReady, "FINETUNING"); !errors.Is(err, ErrInputKindFormatMismatch) {
		t.Fatalf("FINETUNING+CSV returned %v, want ErrInputKindFormatMismatch", err)
	}
	if errors.Is(ErrInputKindFormatMismatch, ErrAdmissionInputNotReady) {
		t.Fatal("mismatch must not be aliased to INPUT_NOT_READY")
	}
	// An unknown kind stays an availability answer, not a data answer.
	if _, err := verifiedAdmissionInput(csvReady, "SOMETHING_ELSE"); !errors.Is(err, ErrImplementationUnavailable) {
		t.Fatalf("unknown kind returned %v", err)
	}
}

// A ready-state problem is still reported as "not ready" and is checked first.
func TestVerifiedAdmissionInputPrefersNotReadyOverMismatch(t *testing.T) {
	notReady := fixtureInputVersion("CSV", "ani.cpu.csv.v1", 1027, 16, csvProof())
	notReady.State = InputStateValidating
	if _, err := verifiedAdmissionInput(notReady, "FINETUNING"); !errors.Is(err, ErrAdmissionInputNotReady) {
		t.Fatalf("VALIDATING input returned %v, want ErrAdmissionInputNotReady", err)
	}
	failed := fixtureInputVersion("CSV", "ani.cpu.csv.v1", 1027, 16, csvProof())
	failed.State = InputStateRejected
	failed.Failure = &InputValidationFailure{Code: InputFailureContentRejected}
	if _, err := verifiedAdmissionInput(failed, "GENERAL_TRAINING"); !errors.Is(err, ErrAdmissionInputNotReady) {
		t.Fatalf("failed input returned %v, want ErrAdmissionInputNotReady", err)
	}
}

// The third step is a persistence-integrity fault, not a public reason: a
// READY legal combination without its own proof cannot come from legal data.
func TestVerifiedAdmissionInputTreatsMissingProofAsIntegrityFault(t *testing.T) {
	noProof := fixtureInputVersion("JSON", AlpacaSchemaVersion, 64, 0, jsonProof())
	noProof.AlpacaVerification = nil
	if _, err := verifiedAdmissionInput(noProof, "GENERAL_TRAINING"); !errors.Is(err, ErrInputProofUnavailable) {
		t.Fatalf("READY without proof returned %v, want ErrInputProofUnavailable", err)
	}
	if errors.Is(ErrInputProofUnavailable, ErrInputKindFormatMismatch) || errors.Is(ErrInputProofUnavailable, ErrAdmissionInputNotReady) {
		t.Fatal("integrity fault must stay distinct from both business reasons")
	}
	// The same shaped input carrying the *other* format's proof is also corrupt:
	// a JSON READY row that also has a CSV proof was written by a bypassing writer.
	crossed := fixtureInputVersion("JSON", AlpacaSchemaVersion, 64, 0, jsonProof())
	crossed.Verification = &VerifiedCSV{VerifiedObject: VerifiedObject{Object: crossed.Import.Object, VerifiedAt: crossed.AlpacaVerification.VerifiedAt}, SchemaVersion: "ani.cpu.csv.v1", RowCount: 64, FeatureCount: 16, PreparedDataset: csvProof()}
	if _, err := verifiedAdmissionInput(crossed, "GENERAL_TRAINING"); !errors.Is(err, ErrInputProofUnavailable) {
		t.Fatalf("cross-format proof returned %v", err)
	}
}

// The legality judgement and the path lookup must agree for every pair.
func TestKindFormatLegalityAgreesWithPaths(t *testing.T) {
	for _, kind := range []string{"GENERAL_TRAINING", "FINETUNING", "UNKNOWN"} {
		for _, format := range []string{"CSV", "JSON", "PARQUET", ""} {
			_, _, err := AdmissionInputPaths(kind, format)
			supported := AdmissionKindFormatSupported(kind, format)
			if (err == nil) != supported {
				t.Fatalf("%s/%s: paths err=%v supported=%v", kind, format, err, supported)
			}
			if err != nil && !errors.Is(err, ErrInputPathUnavailable) {
				t.Fatalf("%s/%s: illegal pair must fail with ErrInputPathUnavailable, got %v", kind, format, err)
			}
		}
	}
}

func TestAdmissionInputPathsAreFixedPerCombination(t *testing.T) {
	for _, tc := range []struct{ kind, format, input, output string }{
		{"GENERAL_TRAINING", "CSV", "/inputs/data.csv", "/outputs"},
		{"GENERAL_TRAINING", "JSON", "/inputs/data/finetune_data.jsonl", "/outputs"},
		{"FINETUNING", "JSON", "/workspace/inputs/data/finetune_data.jsonl", "/workspace/training"},
	} {
		input, output, err := AdmissionInputPaths(tc.kind, tc.format)
		if err != nil || input != tc.input || output != tc.output {
			t.Fatalf("%s/%s = %q %q %v", tc.kind, tc.format, input, output, err)
		}
	}
	// FINETUNING+CSV must not fall back to a zero-value path.
	if input, output, err := AdmissionInputPaths("FINETUNING", "CSV"); err == nil || input != "" || output != "" {
		t.Fatalf("illegal combination returned paths %q %q", input, output)
	}
}
