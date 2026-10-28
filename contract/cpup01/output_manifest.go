package cpup01

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// OutputFile describes independently collected bytes. It contains neither an
// object-store ETag nor a signed URL. This inventory alone proves no upload.
type OutputFile struct {
	RelativePath string `json:"relative_path"`
	Role         string `json:"role"`
	InTar        bool   `json:"in_tar"`
	SizeBytes    int64  `json:"size_bytes,string"`
	SHA256       string `json:"sha256"`
}

// OutputManifestBytes binds a complete collector inventory to the immutable
// admission. The returned manifest has no self hash and cannot claim PUBLISHED.
func OutputManifestBytes(admission AdmissionEnvelope, files []OutputFile) ([]byte, string, error) {
	if _, _, err := admission.CanonicalPayloads(); err != nil {
		return nil, "", ErrInvalidArgument
	}
	contract := admission.Snapshot.OutputContract
	if len(files) == 0 || len(files) > MaxOutputFiles {
		return nil, "", ErrInvalidArgument
	}
	var total int64
	var model, adapter bool
	seen := make(map[string]bool, len(files))
	required := make(map[string]RequiredOutput, len(contract.RequiredFiles))
	for _, file := range contract.RequiredFiles {
		required[file.RelativePath] = file
	}
	for _, file := range files {
		if seen[file.RelativePath] || !validSnapshotPath(file.RelativePath) || len(file.RelativePath) > MaxRelativePathBytes || file.SizeBytes < 0 || file.SizeBytes == 0 && file.SHA256 != EmptyContentSHA256 || (file.SizeBytes == 0 && (file.Role == "CHECKPOINT" || file.Role == "MODEL_CONFIG")) || !snapshotSHA256Pattern.MatchString(file.SHA256) {
			return nil, "", ErrInvalidArgument
		}
		expected, fixed := required[file.RelativePath]
		if fixed {
			if file.Role != expected.Role || file.InTar != expected.InTar {
				return nil, "", ErrInvalidArgument
			}
			delete(required, file.RelativePath)
		} else {
			if admission.Snapshot.Kind != "FINETUNING" {
				return nil, "", ErrInvalidArgument
			}
			isModel := strings.HasPrefix(file.RelativePath, "export/merged/") && file.Role == "HF_MODEL" && file.InTar
			isAdapter := strings.HasPrefix(file.RelativePath, "training/adapter/") && file.Role == "LORA_ADAPTER" && !file.InTar
			if !isModel && !isAdapter {
				return nil, "", ErrInvalidArgument
			}
			model, adapter = model || isModel, adapter || isAdapter
		}
		var err error
		total, err = CheckedAdd(total, file.SizeBytes)
		if err != nil {
			return nil, "", ErrInvalidArgument
		}
		seen[file.RelativePath] = true
	}
	if len(required) != 0 || admission.Snapshot.Kind == "FINETUNING" && (!model || !adapter) || admission.Snapshot.Limits.Check(ContentOutput, total) != nil {
		return nil, "", ErrInvalidArgument
	}
	ordered := append([]OutputFile{}, files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].RelativePath < ordered[j].RelativePath })
	manifest := struct {
		Schema            string       `json:"schema"`
		TenantID          string       `json:"tenant_id"`
		OperationID       string       `json:"operation_id"`
		ExecutionID       string       `json:"execution_id"`
		ExecutionSpecHash string       `json:"execution_spec_hash"`
		InputVersionID    string       `json:"input_version_id"`
		InputSHA256       string       `json:"input_sha256"`
		ImplementationID  string       `json:"implementation_id"`
		ImageDigest       string       `json:"image_digest"`
		StorageState      string       `json:"storage_state"`
		Files             []OutputFile `json:"files"`
	}{
		Schema:   "ani.modeldev.output-manifest.v3",
		TenantID: strings.ToLower(admission.TenantID), OperationID: strings.ToLower(admission.OperationID), ExecutionID: strings.ToLower(admission.ExecutionID),
		ExecutionSpecHash: admission.SpecHash, InputVersionID: strings.ToLower(admission.Snapshot.Input.InputVersionID), InputSHA256: admission.Snapshot.Input.Object.SHA256,
		ImplementationID: admission.Snapshot.Implementation.ImplementationID, ImageDigest: admission.Snapshot.Program.ImageDigest,
		StorageState: "WORKSPACE_ONLY", Files: ordered,
	}
	canonical, err := encodeCanonicalJSON(manifest)
	if err != nil {
		return nil, "", ErrInvalidArgument
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}
