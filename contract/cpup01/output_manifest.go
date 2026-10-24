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
	if len(files) != len(contract.RequiredFiles) || uint64(len(files)) > uint64(contract.MaxFileCount) {
		return nil, "", ErrInvalidArgument
	}
	remaining := contract.MaxTotalBytes
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		if seen[file.RelativePath] || file.SizeBytes <= 0 || file.SizeBytes > remaining || len(file.SHA256) != 64 || strings.ToLower(file.SHA256) != file.SHA256 {
			return nil, "", ErrInvalidArgument
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return nil, "", ErrInvalidArgument
		}
		matched := false
		for _, required := range contract.RequiredFiles {
			if file.RelativePath == required.RelativePath && file.Role == required.Role && file.SizeBytes <= required.MaxSizeBytes {
				matched = true
				break
			}
		}
		if !matched {
			return nil, "", ErrInvalidArgument
		}
		seen[file.RelativePath] = true
		remaining -= file.SizeBytes
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
		Schema:   "ani.modeldev.output-manifest.v1",
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
